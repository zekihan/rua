package ingest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
)

// sanitizeFailureMessage rebuilds a dmarc-failure message without any
// original-message body (DESIGN.md section 3: headers only). The outer
// headers and the multipart/report structure are kept, the
// message/feedback-report part is kept verbatim, and an attached original
// message (message/rfc822 or text/rfc822-headers) is reduced to its header
// block. The human-readable part is dropped entirely, even when it is a
// short text/plain part, because it commonly quotes the original message.
// Every other part is dropped. The result reparses to the same
// failure_reports row as the raw message.
func sanitizeFailureMessage(raw []byte) []byte {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && entity == nil {
		return headerBlock(raw)
	}

	mediaType, _, err := entity.Header.ContentType()
	if err != nil || !strings.EqualFold(mediaType, "multipart/report") {
		return headerBlock(raw)
	}

	reader := entity.MultipartReader()
	if reader == nil {
		return headerBlock(raw)
	}

	var out bytes.Buffer

	writer, err := message.CreateWriter(&out, entity.Header.Copy())
	if err != nil {
		return headerBlock(raw)
	}

	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}

		if partErr != nil {
			break
		}

		writeSanitizedPart(writer, part)
	}

	_ = writer.Close()

	if out.Len() == 0 {
		return headerBlock(raw)
	}

	return out.Bytes()
}

func writeSanitizedPart(writer *message.Writer, part *message.Entity) {
	mediaType, _, err := part.Header.ContentType()
	if err != nil {
		return
	}

	mediaType = strings.ToLower(mediaType)

	var body []byte

	switch mediaType {
	case typeFeedback:
		body, err = io.ReadAll(io.LimitReader(part.Body, MaxReportBytes))
		if err != nil {
			return
		}
	case typeOriginal, typeOriginalHeaders:
		body, err = readHeaderBlock(part.Body)
		if err != nil {
			return
		}
	default:
		return
	}

	header := message.HeaderFromMap(map[string][]string{
		"Content-Type": {mediaType},
	})

	partWriter, err := writer.CreatePart(header)
	if err != nil {
		return
	}

	_, err = partWriter.Write(body)
	if err != nil {
		_ = partWriter.Close()

		return
	}

	_ = partWriter.Close()
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
