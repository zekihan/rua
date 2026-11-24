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

// readOriginalHeaders reads only the attached message's header block.
func (reader *bodyReader) readOriginalHeaders(body io.Reader) ([]byte, error) {
	limit := min(MaxReportBytes, reader.remaining)
	limited := io.LimitReader(body, limit+1)
	buffered := bufio.NewReader(limited)

	var data []byte

	lineStart := true

	for {
		line, err := buffered.ReadSlice('\n')
		data = append(data, line...)

		continuation := errors.Is(err, bufio.ErrBufferFull)
		if !continuation && err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read attached message headers: %w", err)
		}

		if err != nil && !continuation || lineStart && (bytes.Equal(line, []byte("\r\n")) ||
			bytes.Equal(line, []byte("\n"))) {
			break
		}

		lineStart = !continuation
	}

	return reader.account(data, limit, nil)
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

		data, err := reader.readOriginalHeaders(entity.Body)
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
