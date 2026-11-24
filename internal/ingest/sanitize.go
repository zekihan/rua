package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

const sanitizedBoundaryBase = "rua-failure-report"

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
	if len(failures) == 0 {
		return headerBlock(raw)
	}

	outer := filteredOuterHeaders(raw)
	boundary := sanitizedBoundary(outer, failures)

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

func sanitizedBoundary(outer []byte, failures []document) string {
	boundary := sanitizedBoundaryBase
	for boundaryInUse(boundary, outer, failures) {
		boundary += "-x"
	}

	return boundary
}

func boundaryInUse(boundary string, outer []byte, failures []document) bool {
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
