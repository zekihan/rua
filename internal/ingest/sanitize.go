package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/emersion/go-message"
)

var (
	errFailureHeaders = errors.New("attached message headers could not be read")
	errNoBoundary     = errors.New("multipart without boundary")
)

const (
	minimalBoundary = "rua-minimal-9c1e"
	failureBoundary = "rua-failure-9c1e"
)

// sanitizeMessage builds the stored form of a message that contains
// failure content (DESIGN.md section 3: headers only). It is a visitor
// over walkMIME, the same walk extraction uses.
func sanitizeMessage(raw []byte) []byte {
	return sanitizeViaWalker(raw).data
}

// sanitizeFailureMessage sanitizes a message known to hold failure content.
func sanitizeFailureMessage(raw []byte, _ []document) []byte {
	return sanitizeMessage(raw)
}

type sanitizeOutcome struct { //nolint:govet // Data, error, counts: logical grouping.
	data     []byte
	err      error
	redacted int
	failed   bool
}

type sanitizeFrame struct {
	boundary   string
	isFeedback bool
	suppressed bool
}

type sanitizer struct { //nolint:govet // Output, reader, stack: logical grouping.
	out      bytes.Buffer
	reader   *bodyReader
	stack    []*sanitizeFrame
	err      error
	redacted int
	failed   bool
}

func (san *sanitizer) fail(err error) {
	if err == nil {
		return
	}

	san.failed = true
	if san.err == nil {
		san.err = err
	}
}

func (san *sanitizer) insideFeedback() bool {
	for _, frame := range san.stack {
		if frame.isFeedback {
			return true
		}
	}

	return false
}

func (san *sanitizer) nearestFrame() *sanitizeFrame {
	for _, frame := range slices.Backward(san.stack) {
		if !frame.suppressed {
			return frame
		}
	}

	return nil
}

func sanitizeViaWalker(raw []byte) sanitizeOutcome {
	if looksBare(raw) || isZip(raw) {
		return sanitizeOutcome{
			data: nil, err: errNotReport, redacted: 0, failed: true,
		}
	}

	entity, headerErr := message.Read(bytes.NewReader(raw))
	if entity == nil {
		return sanitizeOutcome{
			data: nil, err: fmt.Errorf("read MIME message: %w", headerErr),
			redacted: 0, failed: true,
		}
	}

	if headerErr != nil && !message.IsUnknownCharset(headerErr) {
		return sanitizeOutcome{
			data: nil, err: fmt.Errorf("read MIME message: %w", headerErr),
			redacted: 0, failed: true,
		}
	}

	san := &sanitizer{
		out: bytes.Buffer{}, reader: newBodyReader(), stack: nil,
		err: nil, redacted: 0, failed: false,
	}
	hooks := &mimeHooks{
		fail:           san.fail,
		leaf:           san.leaf,
		multipartStart: san.multipartStart,
		multipartEnd:   san.multipartEnd,
		failurePair:    san.failurePair,
	}

	err := walkMIME(entity, 0, san.reader, hooks)
	if err != nil {
		san.fail(err)
	}

	return sanitizeOutcome{
		data: san.out.Bytes(), err: san.err, redacted: san.redacted, failed: san.failed,
	}
}

func (san *sanitizer) multipartStart(entity *message.Entity, isFeedback bool) error {
	mediaType, params, err := entity.Header.ContentType()
	if err != nil {
		san.fail(fmt.Errorf("invalid MIME content type: %w", err))

		mediaType = "multipart/mixed"
		params = map[string]string{}
	}

	boundary := params["boundary"]
	if boundary == "" {
		san.fail(errNoBoundary)

		boundary = "rua-sanitized"
	}

	suppressed := !isFeedback && san.insideFeedback()
	frame := &sanitizeFrame{boundary: boundary, isFeedback: isFeedback, suppressed: suppressed}

	if !suppressed {
		writeParams := copyParams(params)
		writeParams["boundary"] = boundary

		if len(san.stack) == 0 {
			writeStoredHeader(&san.out, &entity.Header, mediaType, writeParams, false)
		} else if parent := san.nearestFrame(); parent != nil {
			fmt.Fprintf(&san.out, "--%s\r\n", parent.boundary)
			writeStoredHeader(&san.out, &entity.Header, mediaType, writeParams, false)
		} else {
			writeStoredHeader(&san.out, &entity.Header, mediaType, writeParams, false)
		}
	}

	san.stack = append(san.stack, frame)

	return nil
}

func (san *sanitizer) multipartEnd(_ *message.Entity, _ bool) error {
	if len(san.stack) == 0 {
		return nil
	}

	frame := san.stack[len(san.stack)-1]
	san.stack = san.stack[:len(san.stack)-1]

	if frame.suppressed {
		return nil
	}

	fmt.Fprintf(&san.out, "--%s--\r\n", frame.boundary)

	return nil
}

func (san *sanitizer) leaf(entity *message.Entity, mediaType string) error {
	if san.insideFeedback() {
		return nil
	}

	data, err := san.reader.read(entity.Body)
	if err != nil {
		san.fail(err)

		return nil
	}

	_, params, ctErr := entity.Header.ContentType()
	if ctErr != nil {
		params = map[string]string{}
	}

	writeParams := leafParams(mediaType, params)

	if len(san.stack) == 0 {
		writeStoredHeader(&san.out, &entity.Header, mediaType, writeParams, true)
		san.out.Write(data)

		return nil
	}

	frame := san.stack[len(san.stack)-1]
	if frame.suppressed {
		return nil
	}

	fmt.Fprintf(&san.out, "--%s\r\n", frame.boundary)
	writeStoredHeader(&san.out, &entity.Header, mediaType, writeParams, true)
	san.out.Write(data)
	san.out.WriteString("\r\n")

	return nil
}

func (san *sanitizer) failurePair(feedback, headers []byte, headerError bool) error {
	auth, err := feedbackIsAuth(feedback)
	if err != nil {
		san.fail(fmt.Errorf("read feedback fields: %w", err))

		return nil
	}

	if auth {
		san.redacted++
	}

	if headerError {
		san.fail(errFailureHeaders)
	}

	frame := san.nearestFrame()
	if frame == nil || !frame.isFeedback {
		san.fail(errFailureHeaders)

		return nil
	}

	writeStoredPart(&san.out, frame.boundary, typeFeedback, feedbackFieldBlock(feedback))
	writeStoredPart(&san.out, frame.boundary, typeOriginalHeaders, headerOnly(headers))

	return nil
}

func copyParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	maps.Copy(out, params)

	return out
}

func leafParams(mediaType string, params map[string]string) map[string]string {
	out := copyParams(params)
	if strings.HasPrefix(mediaType, "text/") {
		if charset, ok := out["charset"]; ok &&
			!strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "us-ascii") {
			// The entity body is already decoded to UTF-8 by go-message.
			out["charset"] = "utf-8"
		}
	}

	return out
}

func formatContentType(mediaType string, params map[string]string) string {
	var header message.Header

	header.SetContentType(mediaType, params)

	return header.Get("Content-Type")
}

func isRuaHeader(key string) bool {
	return strings.HasPrefix(strings.ToLower(key), "x-rua-")
}

// writeStoredHeader copies header fields via go-message parsing, strips
// any X-Rua-* field, and writes a fresh Content-Type when mediaType is set.
func writeStoredHeader(
	out *bytes.Buffer, header *message.Header, mediaType string,
	params map[string]string, leaf bool,
) {
	fields := header.Fields()

	for fields.Next() {
		key := fields.Key()
		if isRuaHeader(key) {
			continue
		}

		if mediaType != "" && strings.EqualFold(key, "Content-Type") {
			continue
		}

		if strings.EqualFold(key, "Content-Transfer-Encoding") {
			continue
		}

		raw, err := fields.Raw()
		if err != nil || len(raw) == 0 {
			fmt.Fprintf(out, "%s: %s\r\n", key, fields.Value())

			continue
		}

		out.Write(raw)

		if !bytes.HasSuffix(raw, []byte("\r\n")) {
			out.WriteString("\r\n")
		}
	}

	if mediaType != "" {
		fmt.Fprintf(out, "Content-Type: %s\r\n", formatContentType(mediaType, params))
	}

	out.WriteString("\r\n")

	_ = leaf // Leaf and multipart headers are written the same way.
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
	for _, sep := range [][]byte{
		[]byte("\r\n\r\n"), []byte("\n\n"), []byte("\r\n\n"), []byte("\n\r\n"),
	} {
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

var allowlistedOuter = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-Id", "Mime-Version",
}

func isAllowlistedOuter(key string) bool {
	for _, name := range allowlistedOuter {
		if strings.EqualFold(key, name) {
			return true
		}
	}

	return false
}

type minimalItem struct { //nolint:govet // Header, bodies, type: logical grouping.
	header    *message.Header
	feedback  []byte
	headers   []byte
	data      []byte
	mediaType string
	params    map[string]string
	isFailure bool
}

type minimalCollector struct {
	reader        *bodyReader
	items         []minimalItem
	feedbackDepth int
	failed        bool
}

func (collector *minimalCollector) fail(err error) {
	if err != nil {
		collector.failed = true
	}
}

func (collector *minimalCollector) multipartStart(_ *message.Entity, isFeedback bool) error {
	if isFeedback {
		collector.feedbackDepth++
	}

	return nil
}

func (collector *minimalCollector) multipartEnd(_ *message.Entity, isFeedback bool) error {
	if isFeedback && collector.feedbackDepth > 0 {
		collector.feedbackDepth--
	}

	return nil
}

func (collector *minimalCollector) leaf(entity *message.Entity, mediaType string) error {
	if collector.feedbackDepth > 0 {
		return nil
	}

	data, err := collector.reader.read(entity.Body)
	if err != nil {
		collector.failed = true

		return nil //nolint:nilerr // Unreadable leaves are dropped from the minimal copy.
	}

	_, params, ctErr := entity.Header.ContentType()
	if ctErr != nil {
		params = map[string]string{}
	}

	header := entity.Header.Copy()
	collector.items = append(collector.items, minimalItem{
		header: &header, feedback: nil, headers: nil, data: data,
		mediaType: mediaType, params: leafParams(mediaType, params), isFailure: false,
	})

	return nil
}

func (collector *minimalCollector) failurePair(
	feedback, headers []byte, headerError bool,
) error {
	auth, err := feedbackIsAuth(feedback)
	if err != nil || !auth {
		return nil //nolint:nilerr // Non-auth pairs produce no failure row.
	}

	if headerError {
		collector.failed = true
	}

	collector.items = append(collector.items, minimalItem{
		header: nil, feedback: feedbackFieldBlock(feedback),
		headers: headerOnly(headers), data: nil, mediaType: "", params: nil, isFailure: true,
	})

	return nil
}

// buildMinimalCopy builds the fail-closed stored form: allowlisted outer
// headers, one feedback/original-headers pair per parsed failure, and
// every non-failure leaf that was read successfully.
func buildMinimalCopy(raw []byte, orig *evalOutcome) []byte {
	collector := &minimalCollector{
		reader: newBodyReader(), items: nil, feedbackDepth: 0, failed: false,
	}

	if !looksBare(raw) && !isZip(raw) {
		entity, headerErr := message.Read(bytes.NewReader(raw))
		if entity != nil && (headerErr == nil || message.IsUnknownCharset(headerErr)) {
			hooks := &mimeHooks{
				fail:           collector.fail,
				leaf:           collector.leaf,
				multipartStart: collector.multipartStart,
				multipartEnd:   collector.multipartEnd,
				failurePair:    collector.failurePair,
			}

			err := walkMIME(entity, 0, collector.reader, hooks)
			if err != nil {
				collector.failed = true
			}
		}
	}

	items := collector.items
	if len(items) == 0 && orig != nil {
		for _, doc := range orig.failureDocs {
			items = append(items, minimalItem{
				header: nil, feedback: feedbackFieldBlock(doc.data),
				headers: headerOnly(doc.originalHeaders), data: nil,
				mediaType: "", params: nil, isFailure: true,
			})
		}
	}

	var out bytes.Buffer

	writeMinimalOuter(&out, raw)
	writeMinimalItems(&out, items)
	fmt.Fprintf(&out, "--%s--\r\n", minimalBoundary)

	return out.Bytes()
}

func writeMinimalOuter(out *bytes.Buffer, raw []byte) {
	wroteMIME := false

	entity, err := readHeader(raw)
	if err == nil && entity != nil {
		fields := entity.Header.Fields()
		for fields.Next() {
			key := fields.Key()
			if isRuaHeader(key) || !isAllowlistedOuter(key) {
				continue
			}

			if strings.EqualFold(key, "Mime-Version") {
				wroteMIME = true
			}

			rawField, rawErr := fields.Raw()
			if rawErr != nil || len(rawField) == 0 {
				fmt.Fprintf(out, "%s: %s\r\n", key, fields.Value())

				continue
			}

			out.Write(rawField)

			if !bytes.HasSuffix(rawField, []byte("\r\n")) {
				out.WriteString("\r\n")
			}
		}
	}

	if !wroteMIME {
		out.WriteString("MIME-Version: 1.0\r\n")
	}

	fmt.Fprintf(out, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", minimalBoundary)
}

func writeMinimalItems(out *bytes.Buffer, items []minimalItem) {
	for _, item := range items {
		if item.isFailure {
			fmt.Fprintf(
				out,
				"--%s\r\nContent-Type: multipart/report; report-type=feedback-report; boundary=%q\r\n\r\n",
				minimalBoundary,
				failureBoundary,
			)
			writeStoredPart(out, failureBoundary, typeFeedback, item.feedback)
			writeStoredPart(out, failureBoundary, typeOriginalHeaders, item.headers)
			fmt.Fprintf(out, "--%s--\r\n", failureBoundary)

			continue
		}

		fmt.Fprintf(out, "--%s\r\n", minimalBoundary)
		writeStoredHeader(out, item.header, item.mediaType, item.params, true)
		out.Write(item.data)
		out.WriteString("\r\n")
	}
}
