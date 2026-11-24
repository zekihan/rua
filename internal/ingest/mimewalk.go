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

// bodyReader reads decoded entity bodies under the shared report and
// message budgets, applying the entity's transfer decoding and limits.
type bodyReader struct {
	remaining int64
}

func newBodyReader() *bodyReader {
	return &bodyReader{remaining: int64(MaxMessageBytes)}
}

func (reader *bodyReader) read(input io.Reader) ([]byte, error) {
	limit := min(MaxReportBytes, reader.remaining)
	data, err := io.ReadAll(io.LimitReader(input, limit+1))

	return reader.account(data, limit, err)
}

func (reader *bodyReader) account(data []byte, limit int64, err error) ([]byte, error) {
	reader.remaining -= int64(len(data))
	if reader.remaining < 0 {
		return nil, errMessageLimit
	}

	if int64(len(data)) > limit {
		return nil, errReportLimit
	}

	if err != nil {
		return nil, fmt.Errorf("read report data: %w", err)
	}

	return data, nil
}

var (
	errHeaderSeparator = errors.New("attached message has no blank line after its headers")
	errHeaderSyntax    = errors.New("attached message headers are malformed")
)

// readOriginalHeaders reads only the attached message's header block and
// fails closed: every line must be a header field or a continuation, so a
// broken attachment never lets body text in. A full message
// (message/rfc822) must also end its headers with a blank line; a
// text/rfc822-headers part may end at EOF. Errors never carry the bytes.
func (reader *bodyReader) readOriginalHeaders(
	body io.Reader,
	requireSeparator bool,
) ([]byte, error) {
	limit := min(MaxReportBytes, reader.remaining)
	limited := io.LimitReader(body, limit+1)
	buffered := bufio.NewReader(limited)

	var data []byte

	for {
		line, err := buffered.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, errHeaderSyntax
		}

		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read attached message headers: %w", err)
		}

		if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
			break
		}

		if len(line) > 0 && !headerLine(line, len(data) == 0) {
			return nil, errHeaderSyntax
		}

		data = append(data, line...)

		if err != nil {
			if requireSeparator {
				return nil, errHeaderSeparator
			}

			break
		}
	}

	return reader.account(data, limit, nil)
}

// headerLine reports whether line is a header field ("Name: value") or,
// after the first line, a folded continuation.
func headerLine(line []byte, first bool) bool {
	if line[0] == ' ' || line[0] == '\t' {
		return !first
	}

	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}

	for _, char := range bytes.TrimRight(line[:colon], " \t") {
		if char < '!' || char > '~' {
			return false
		}
	}

	return true
}

type failureParts struct { //nolint:govet // Feedback, headers, presence: logical grouping.
	feedback    []byte
	feedbackErr error
	headers     []byte
	headerErr   error
	hasFeedback bool
	hasHeaders  bool
}

// mimeHooks are the visitor callbacks for walkMIME, including a seen
// hook called for every entity whose content type parsed, so
// structural failure detection sees exactly what extraction sees.
type mimeHooks struct {
	fail        func(error)
	seen        func(mediaType string, params map[string]string)
	leaf        func(entity *message.Entity, mediaType string) error
	failurePair func(parts *failureParts) error
}

func (hooks *mimeHooks) failed(err error) {
	if hooks != nil && hooks.fail != nil {
		hooks.fail(err)
	}
}

func isFeedbackReport(mediaType string, params map[string]string) bool {
	if !strings.EqualFold(mediaType, "multipart/report") {
		return false
	}

	for key, value := range params {
		if strings.EqualFold(key, "report-type") {
			return strings.EqualFold(value, "feedback-report")
		}
	}

	return false
}

func emitFailurePair(parts *failureParts, hooks *mimeHooks) error {
	if !parts.hasFeedback && !parts.hasHeaders {
		return nil
	}

	if hooks == nil || hooks.failurePair == nil {
		return nil
	}

	return hooks.failurePair(parts)
}

// walkMIME is the single MIME walker extraction uses: header parsing
// (go-message), content-type and parameter handling, transfer
// decoding and limits all live here.
func walkMIME(
	entity *message.Entity, depth int, reader *bodyReader, hooks *mimeHooks,
) error {
	mediaType, params, err := entity.Header.ContentType()
	if err != nil {
		hooks.failed(fmt.Errorf("invalid MIME content type: %w", err))

		return nil
	}

	if hooks != nil && hooks.seen != nil {
		hooks.seen(mediaType, params)
	}

	parts := entity.MultipartReader()
	if parts == nil {
		if hooks != nil && hooks.leaf != nil {
			return hooks.leaf(entity, mediaType)
		}

		return nil
	}

	if depth >= maxMIMEDepth {
		hooks.failed(errMIMEDepth)

		return nil
	}

	isFeedback := isFeedbackReport(mediaType, params)

	var feedback failureParts

	err = walkMultipartChildren(parts, depth, reader, hooks, isFeedback, &feedback)
	if err != nil {
		return err
	}

	if isFeedback {
		return emitFailurePair(&feedback, hooks)
	}

	return nil
}

func walkMultipartChildren(
	parts message.MultipartReader, depth int, reader *bodyReader,
	hooks *mimeHooks, isFeedback bool, feedback *failureParts,
) error {
	for reader.remaining >= 0 {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil && (part == nil || !message.IsUnknownCharset(err)) {
			hooks.failed(fmt.Errorf("read MIME part: %w", err))

			break
		}

		if isFeedback {
			handled, partErr := readFailurePart(feedback, part, reader, hooks)
			if partErr != nil {
				return partErr
			}

			if handled {
				continue
			}
		}

		err = walkMIME(part, depth+1, reader, hooks)
		if err != nil {
			return err
		}
	}

	return nil
}

func readFailurePart(
	parts *failureParts, entity *message.Entity, reader *bodyReader, hooks *mimeHooks,
) (bool, error) {
	mediaType, _, err := entity.Header.ContentType()
	if err != nil {
		return false, nil //nolint:nilerr // The generic walk reports the invalid Content-Type.
	}

	switch {
	case strings.EqualFold(mediaType, typeFeedback):
		if parts.hasFeedback {
			err := emitFailurePair(parts, hooks)
			if err != nil {
				return true, err
			}

			*parts = failureParts{
				feedback: nil, feedbackErr: nil, headers: nil, headerErr: nil,
				hasFeedback: false, hasHeaders: false,
			}
		}

		parts.hasFeedback = true

		data, err := reader.read(entity.Body)
		if err != nil {
			parts.feedbackErr = err
		} else {
			parts.feedback = data
		}
	case strings.EqualFold(mediaType, typeOriginal),
		strings.EqualFold(mediaType, typeOriginalHeaders):
		parts.hasHeaders = true

		data, err := reader.readOriginalHeaders(
			entity.Body, strings.EqualFold(mediaType, typeOriginal),
		)
		if err != nil {
			parts.headerErr = err
		} else {
			parts.headers = data
			parts.headerErr = nil
		}
	default:
		return false, nil
	}

	return true, nil
}
