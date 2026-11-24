package ingest

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/zekihan/rua/internal/report"
)

const (
	sanitizedBoundaryBase = "rua-failure-report"
	mixedBoundaryBase     = "rua-mixed-report"
)

// sanitizeFailureMessage builds the stored form of a dmarc-failure message
// from the failure documents the extraction recognized (DESIGN.md section 3:
// headers only). The result is a fresh RFC 5322 message carrying the original
// outer header fields and, per failure found, a message/feedback-report part
// with only the feedback field block plus a text/rfc822-headers part with
// only the original message's header block, all under one
// multipart/report; report-type=feedback-report. Human-readable parts and
// original-message bodies never reach the stored blob, however deeply the
// report was nested, and the result reparses to the same failure_reports
// rows. The caller keeps messages.sha256 as the digest of the raw bytes.
func sanitizeFailureMessage(raw []byte, failures []document) []byte {
	return sanitizeMessage(raw, failures, nil, failures)
}

// sanitizeMessage is sanitizeFailureMessage plus every recognized
// aggregate/TLS document, for messages that mix failure content with other
// reports. Each report document is stored as its own base64 attachment
// holding the exact extracted bytes, so a reparse yields the same rows.
// Ordered preserves the extraction order so the message class is stable.
func sanitizeMessage(
	raw []byte, failures, reports, ordered []document,
) []byte {
	if len(failures) == 0 && len(reports) == 0 {
		return headerBlock(raw)
	}

	if len(reports) == 0 {
		return buildFailureMessage(raw, failures)
	}

	if len(ordered) == 0 {
		ordered = append(append([]document{}, failures...), reports...)
	}

	return buildMixedMessage(raw, failures, reports, ordered)
}

func buildFailureMessage(raw []byte, failures []document) []byte {
	outer := filteredOuterHeaders(raw)
	boundary := sanitizedBoundary(outer, failures, nil)

	var out bytes.Buffer

	out.Write(outer)
	out.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(
		&out,
		"Content-Type: multipart/report; report-type=\"feedback-report\"; boundary=%q\r\n\r\n",
		boundary,
	)

	for _, failure := range failures {
		writeStoredPart(&out, boundary, typeFeedback, feedbackFieldBlock(failure.data))
		writeStoredPart(&out, boundary, typeOriginalHeaders, headerOnly(failure.originalHeaders))
	}

	fmt.Fprintf(&out, "--%s--\r\n", boundary)

	return out.Bytes()
}

func buildMixedMessage(
	raw []byte, failures, reports, ordered []document,
) []byte {
	outer := filteredOuterHeaders(raw)
	mixedBoundary := chooseBoundary(mixedBoundaryBase, outer, failures, reports)
	reportBoundary := chooseBoundary(sanitizedBoundaryBase, outer, failures, reports)

	var out bytes.Buffer

	out.Write(outer)
	out.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&out, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mixedBoundary)

	for _, doc := range ordered {
		if doc.class == report.ClassFailure {
			fmt.Fprintf(
				&out,
				"--%s\r\nContent-Type: multipart/report; "+
					"report-type=\"feedback-report\"; boundary=%q\r\n\r\n",
				mixedBoundary, reportBoundary,
			)
			writeStoredPart(
				&out, reportBoundary, typeFeedback, feedbackFieldBlock(doc.data),
			)
			writeStoredPart(
				&out, reportBoundary, typeOriginalHeaders, headerOnly(doc.originalHeaders),
			)
			fmt.Fprintf(&out, "--%s--\r\n", reportBoundary)

			continue
		}

		writeReportPart(&out, mixedBoundary, doc)
	}

	fmt.Fprintf(&out, "--%s--\r\n", mixedBoundary)

	return out.Bytes()
}

func writeReportPart(out *bytes.Buffer, boundary string, doc document) {
	mediaType := "application/xml"
	if doc.class == report.ClassTLS {
		mediaType = typeTLSJSON
	}

	fmt.Fprintf(
		out,
		"--%s\r\nContent-Type: %s\r\nContent-Transfer-Encoding: base64\r\n"+
			"Content-Disposition: attachment\r\n\r\n",
		boundary, mediaType,
	)

	encoded := base64.StdEncoding.EncodeToString(doc.data)
	for len(encoded) > 76 {
		out.WriteString(encoded[:76])
		out.WriteString("\r\n")

		encoded = encoded[76:]
	}

	out.WriteString(encoded)
	out.WriteString("\r\n")
}

func sanitizedBoundary(outer []byte, failures, reports []document) string {
	return chooseBoundary(sanitizedBoundaryBase, outer, failures, reports)
}

func chooseBoundary(
	base string, outer []byte, failures, reports []document,
) string {
	boundary := base
	for boundaryInUse(boundary, outer, failures, reports) {
		boundary += "-x"
	}

	return boundary
}

func boundaryInUse(
	boundary string, outer []byte, failures, reports []document,
) bool {
	needle := []byte(boundary)
	if bytes.Contains(outer, needle) {
		return true
	}

	for _, failure := range failures {
		if bytes.Contains(feedbackFieldBlock(failure.data), needle) ||
			bytes.Contains(headerOnly(failure.originalHeaders), needle) {
			return true
		}
	}

	for _, doc := range reports {
		if bytes.Contains(doc.data, needle) ||
			bytes.Contains([]byte(base64.StdEncoding.EncodeToString(doc.data)), needle) {
			return true
		}
	}

	return false
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

var storedOuterHeaders = []string{"From", "To", "Subject", "Date", "Message-ID"}

// filteredOuterHeaders returns the allowlisted fields of the raw message's
// outer header block, with their original lines (including folding) kept.
func filteredOuterHeaders(raw []byte) []byte {
	var out bytes.Buffer

	for _, field := range headerFields(headerBlock(raw)) {
		name, _, found := strings.Cut(field[0], ":")
		if !found || !isStoredOuterHeader(name) {
			continue
		}

		for _, line := range field {
			out.WriteString(line)
			out.WriteString("\r\n")
		}
	}

	return out.Bytes()
}

func isStoredOuterHeader(name string) bool {
	for _, allowed := range storedOuterHeaders {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}

	return false
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
