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
// message budgets. Extraction and sanitization both use it, so they apply
// the same transfer decoding (the entity's decoded body) and limits.
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

type failureParts struct {
	feedback    []byte
	headers     []byte
	headerError bool
}

// mimeHooks are the visitor callbacks for walkMIME. Sanitization is a
// visitor over the same walk extraction uses, so the two can never
// disagree about what is a failure report.
type mimeHooks struct {
	fail           func(error)
	leaf           func(entity *message.Entity, mediaType string) error
	multipartStart func(entity *message.Entity, isFeedback bool) error
	multipartEnd   func(entity *message.Entity, isFeedback bool) error
	failurePair    func(feedback, headers []byte, headerError bool) error
}

func (hooks *mimeHooks) failed(err error) {
	if hooks != nil && hooks.fail != nil {
		hooks.fail(err)
	}
}

func isFeedbackReport(mediaType string, params map[string]string) bool {
	return mediaType == "multipart/report" &&
		strings.EqualFold(params["report-type"], "feedback-report")
}

func feedbackIsAuth(feedback []byte) (bool, error) {
	entity, err := readHeader(feedback)
	if err != nil {
		return false, err
	}

	return strings.EqualFold(entity.Header.Get("Feedback-Type"), "auth-failure"), nil
}

func emitFailurePair(parts *failureParts, hooks *mimeHooks) error {
	if len(parts.feedback) == 0 || hooks == nil || hooks.failurePair == nil {
		return nil
	}

	return hooks.failurePair(parts.feedback, parts.headers, parts.headerError)
}

// walkMIME is the single MIME walker shared by extraction and
// sanitization: same header parsing (go-message), same content-type and
// parameter handling, same transfer decoding and limits.
func walkMIME(
	entity *message.Entity, depth int, reader *bodyReader, hooks *mimeHooks,
) error {
	mediaType, params, err := entity.Header.ContentType()
	if err != nil {
		hooks.failed(fmt.Errorf("invalid MIME content type: %w", err))

		return nil
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

	if hooks != nil && hooks.multipartStart != nil {
		startErr := hooks.multipartStart(entity, isFeedback)
		if startErr != nil {
			return startErr
		}
	}

	var feedback failureParts

	err = walkMultipartChildren(parts, depth, reader, hooks, isFeedback, &feedback)
	if err != nil {
		return err
	}

	if isFeedback {
		err := emitFailurePair(&feedback, hooks)
		if err != nil {
			return err
		}
	}

	if hooks != nil && hooks.multipartEnd != nil {
		return hooks.multipartEnd(entity, isFeedback)
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

	switch mediaType {
	case typeFeedback:
		if len(parts.feedback) > 0 {
			err := emitFailurePair(parts, hooks)
			if err != nil {
				return true, err
			}

			*parts = failureParts{feedback: nil, headers: nil, headerError: false}
		}

		data, err := reader.read(entity.Body)
		if err != nil {
			hooks.failed(err)
		} else {
			parts.feedback = data
		}
	case typeOriginal, typeOriginalHeaders:
		data, err := reader.readOriginalHeaders(entity.Body)
		if err != nil {
			hooks.failed(err)

			parts.headerError = true
		} else {
			parts.headers = data
		}
	default:
		return false, nil
	}

	return true, nil
}
