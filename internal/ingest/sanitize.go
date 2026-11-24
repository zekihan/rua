package ingest

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/quotedprintable"
	"slices"
	"strings"
)

// sanitizeMessage builds the stored form of a message that contains failure
// content (DESIGN.md section 3: headers only). It walks the original MIME
// tree and copies every leaf part that is not inside a failure report
// verbatim, still transfer-encoded and still compressed, so the stored copy
// is never materially larger than the raw message and a reparse sees exactly
// the same parts (including attachments that failed to parse). Each
// multipart/report; report-type=feedback-report subtree is replaced by the
// sanitized pair: message/feedback-report holding only the feedback fields,
// plus text/rfc822-headers holding only the original message's header block.
// The caller keeps messages.sha256 as the digest of the raw bytes.
func sanitizeMessage(raw []byte) []byte {
	var out bytes.Buffer

	writeEntity(&out, raw, 0)

	return out.Bytes()
}

// sanitizeFailureMessage sanitizes a message known to hold failure content.
// The extracted failure documents are no longer needed: the walker finds
// each failure subtree in the raw tree itself.
func sanitizeFailureMessage(raw []byte, _ []document) []byte {
	return sanitizeMessage(raw)
}

type mimePart struct { //nolint:govet // Header, body, type: logical grouping.
	header []byte
	body   []byte
	media  string
	params map[string]string
}

func parsePart(raw []byte) mimePart {
	part := mimePart{
		header: nil, body: nil, media: "", params: map[string]string{},
	}

	index, length := headerSeparator(raw)
	if index < 0 {
		part.header = raw

		return part
	}

	part.header = raw[:index+length]
	part.body = raw[index+length:]

	contentType := headerFieldValue(part.header, "Content-Type")
	if contentType == "" {
		return part
	}

	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		part.media = strings.ToLower(
			strings.TrimSpace(strings.Split(contentType, ";")[0]),
		)

		return part
	}

	part.media = strings.ToLower(media)
	part.params = params

	return part
}

func headerSeparator(data []byte) (int, int) {
	best, bestLen := -1, 0

	for _, sep := range []string{"\r\n\r\n", "\n\n", "\r\n\n", "\n\r\n"} {
		if index := bytes.Index(data, []byte(sep)); index >= 0 &&
			(best < 0 || index < best) {
			best, bestLen = index, len(sep)
		}
	}

	return best, bestLen
}

func (part *mimePart) isMultipart() bool {
	return strings.HasPrefix(part.media, "multipart/") && part.params["boundary"] != ""
}

func (part *mimePart) isFeedbackReport() bool {
	return part.media == "multipart/report" &&
		strings.EqualFold(part.params["report-type"], "feedback-report")
}

func writeEntity(out *bytes.Buffer, raw []byte, depth int) {
	part := parsePart(raw)

	if !part.isMultipart() {
		out.Write(part.header)
		out.Write(part.body)

		return
	}

	if depth >= maxMIMEDepth {
		// Keep only the multipart header: a reparse still reaches this
		// multipart at the same depth and records the depth error, but no
		// body below it is stored.
		out.Write(part.header)

		return
	}

	if part.isFeedbackReport() {
		writeFailureReport(out, &part)

		return
	}

	// Keep the original boundary: it already delimits the copied bodies,
	// and reusing it makes sanitizing a sanitized message a no-op.
	boundary := part.params["boundary"]
	writeMultipartHeader(out, &part, boundary)

	for _, child := range splitMultipart(part.body, boundary) {
		fmt.Fprintf(out, "--%s\r\n", boundary)
		writeEntity(out, child, depth+1)
		out.WriteString("\r\n")
	}

	fmt.Fprintf(out, "--%s--\r\n", boundary)
}

func writeFailureReport(out *bytes.Buffer, part *mimePart) {
	boundary := part.params["boundary"]
	writeMultipartHeader(out, part, boundary)

	collector := &failureCollector{
		done: nil, current: failurePair{feedback: nil, headers: nil}, has: false,
	}
	collector.walk(part)

	for _, pair := range collector.pairs() {
		writeStoredPart(out, boundary, typeFeedback, feedbackFieldBlock(pair.feedback))
		writeStoredPart(
			out, boundary, typeOriginalHeaders, headerOnly(pair.headers),
		)
	}

	fmt.Fprintf(out, "--%s--\r\n", boundary)
}

// writeMultipartHeader copies the original header fields except
// Content-Type, then writes a fresh Content-Type carrying the original
// parameters (report-type included) with the new boundary.
func writeMultipartHeader(out *bytes.Buffer, part *mimePart, boundary string) {
	for _, field := range headerFields(part.header) {
		name, _, found := strings.Cut(field[0], ":")
		if found && strings.EqualFold(name, "Content-Type") {
			continue
		}

		for _, line := range field {
			out.WriteString(line)
			out.WriteString("\r\n")
		}
	}

	params := make([]string, 0, len(part.params))

	for _, key := range slices.Sorted(maps.Keys(part.params)) {
		if key != "boundary" {
			params = append(params, fmt.Sprintf("%s=%q", key, part.params[key]))
		}
	}

	contentType := part.media
	if len(params) > 0 {
		contentType += "; " + strings.Join(params, "; ")
	}

	fmt.Fprintf(out, "Content-Type: %s; boundary=%q\r\n\r\n", contentType, boundary)
}

type failurePair struct {
	feedback []byte
	headers  []byte
}

type failureCollector struct {
	done    []failurePair
	current failurePair
	has     bool
}

func (collector *failureCollector) pairs() []failurePair {
	collector.flush()

	return collector.done
}

func (collector *failureCollector) flush() {
	if !collector.has {
		return
	}

	collector.done = append(collector.done, collector.current)
	collector.current = failurePair{feedback: nil, headers: nil}
	collector.has = false
}

func (collector *failureCollector) walk(part *mimePart) {
	for _, childRaw := range splitMultipart(part.body, part.params["boundary"]) {
		child := parsePart(childRaw)
		collector.walkPart(&child)
	}
}

func (collector *failureCollector) walkPart(part *mimePart) {
	switch {
	case part.media == typeFeedback:
		if collector.has {
			collector.flush()
		}

		if data, ok := decodedPartBody(part); ok {
			collector.current.feedback = data
			collector.has = len(data) > 0
		}
	case part.media == typeOriginal || part.media == typeOriginalHeaders:
		if data, ok := decodedPartBody(part); ok {
			collector.current.headers = headerOnly(data)
		}
	case part.isMultipart():
		collector.walk(part)
	default:
		// Human-readable and any other part inside a failure report is
		// dropped.
	}
}

func decodedPartBody(part *mimePart) ([]byte, bool) {
	encoding := strings.ToLower(headerFieldValue(part.header, "Content-Transfer-Encoding"))

	switch encoding {
	case "base64":
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(part.body)))
		if err != nil {
			return nil, false
		}

		return data, true
	case "quoted-printable":
		data, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(part.body)))
		if err != nil {
			return nil, false
		}

		return data, true
	default:
		return part.body, true
	}
}

func headerFieldValue(header []byte, name string) string {
	for _, field := range headerFields(header) {
		fieldName, value, found := strings.Cut(field[0], ":")
		if !found || !strings.EqualFold(fieldName, name) {
			continue
		}

		lines := append([]string{strings.TrimSpace(value)}, field[1:]...)

		return strings.Join(lines, " ")
	}

	return ""
}

// splitMultipart splits a multipart body into its raw part segments, each
// holding the part's headers and body exactly as received. The CRLF before
// a delimiter belongs to the delimiter, not the preceding part. A missing
// closing delimiter (truncated input) ends the last part at EOF.
func splitMultipart(body []byte, boundary string) [][]byte {
	delimiters := findDelimiters(body, boundary)
	if len(delimiters) == 0 {
		return nil
	}

	var parts [][]byte

	for index, delimiter := range delimiters {
		if delimiter.closing {
			break
		}

		end := len(body)
		if index+1 < len(delimiters) {
			end = contentEnd(body, delimiters[index+1].start)
		}

		start := min(delimiter.lineEnd, end)
		parts = append(parts, body[start:end])
	}

	return parts
}

func contentEnd(body []byte, delimiterStart int) int {
	if delimiterStart >= len("\r\n") &&
		bytes.Equal(body[delimiterStart-len("\r\n"):delimiterStart], []byte("\r\n")) {
		return delimiterStart - len("\r\n")
	}

	if delimiterStart >= 1 && body[delimiterStart-1] == '\n' {
		return delimiterStart - 1
	}

	return delimiterStart
}

type delimiter struct {
	start   int
	lineEnd int
	closing bool
}

func findDelimiters(body []byte, boundary string) []delimiter {
	marker := []byte("--" + boundary)

	var found []delimiter

	for offset := 0; offset < len(body); {
		index := bytes.Index(body[offset:], marker)
		if index < 0 {
			break
		}

		index += offset

		candidate, ok := delimiterAt(body, index, marker)
		if ok {
			found = append(found, candidate)
			if candidate.closing {
				break
			}

			offset = candidate.lineEnd

			continue
		}

		offset = index + len(marker)
	}

	return found
}

func delimiterAt(body []byte, start int, marker []byte) (delimiter, bool) {
	none := delimiter{start: 0, lineEnd: 0, closing: false}
	if start > 0 && body[start-1] != '\n' {
		return none, false
	}

	position, closing := afterMarker(body, start+len(marker))

	lineEnd, ok := delimiterLineEnd(body, position)
	if !ok {
		return none, false
	}

	return delimiter{start: start, lineEnd: lineEnd, closing: closing}, true
}

// afterMarker skips transport padding and a closing "--", if present.
func afterMarker(body []byte, position int) (int, bool) {
	position = skipPadding(body, position)
	if position+1 >= len(body) || body[position] != '-' || body[position+1] != '-' {
		return position, false
	}

	return skipPadding(body, position+len("--")), true
}

func skipPadding(body []byte, position int) int {
	for position < len(body) && (body[position] == ' ' || body[position] == '\t') {
		position++
	}

	return position
}

func delimiterLineEnd(body []byte, position int) (int, bool) {
	switch {
	case position >= len(body):
		return len(body), true
	case body[position] == '\n':
		return position + 1, true
	case body[position] == '\r' && position+1 < len(body) && body[position+1] == '\n':
		return position + len("\r\n"), true
	default:
		return 0, false
	}
}

func writeStoredPart(out *bytes.Buffer, boundary, mediaType string, body []byte) {
	fmt.Fprintf(out, "--%s\r\nContent-Type: %s\r\n\r\n", boundary, mediaType)
	out.Write(body)

	if !bytes.HasSuffix(body, []byte("\r\n")) {
		out.WriteString("\r\n")
	}

	out.WriteString("\r\n")
}

// feedbackFieldBlock returns only the header field block of a feedback
// report part; any text after the blank line ending the fields is dropped.
func feedbackFieldBlock(data []byte) []byte {
	return headerOnly(data)
}

func headerOnly(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	block, err := readHeaderBlock(bytes.NewReader(data))
	if err != nil {
		return headerBlock(data)
	}

	return block
}

// headerFields splits a header block into fields, each a slice of lines
// without line endings; continuation lines stay with their field.
func headerFields(block []byte) [][]string {
	var fields [][]string

	scanner := bufio.NewScanner(bytes.NewReader(block))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), MaxReportBytes)

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			break
		}

		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(fields) > 0 {
			fields[len(fields)-1] = append(fields[len(fields)-1], line)

			continue
		}

		fields = append(fields, []string{line})
	}

	return fields
}

// readHeaderBlock reads up to and including the blank line that ends a
// header block; anything after it (a message body) is discarded.
func readHeaderBlock(body io.Reader) ([]byte, error) {
	buffered := bufio.NewReader(io.LimitReader(body, MaxReportBytes))

	var data []byte

	lineStart := true

	for {
		line, err := buffered.ReadSlice('\n')
		data = append(data, line...)

		continuation := errors.Is(err, bufio.ErrBufferFull)
		if !continuation && err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read header block: %w", err)
		}

		if err != nil && !continuation ||
			lineStart && (bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n"))) {
			break
		}

		lineStart = !continuation
	}

	return data, nil
}

// headerBlock returns only the outer header block of raw (privacy-safe
// fallback when the MIME structure cannot be rebuilt).
func headerBlock(raw []byte) []byte {
	for _, sep := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n"), []byte("\r\n\n"), []byte("\n\r\n")} {
		if idx := bytes.Index(raw, sep); idx >= 0 {
			return raw[:idx+len(sep)]
		}
	}

	trimmed := bytes.TrimRight(raw, "\r\n")

	out := make([]byte, 0, len(trimmed)+len("\r\n\r\n"))
	out = append(out, trimmed...)
	out = append(out, "\r\n\r\n"...)

	return out
}
