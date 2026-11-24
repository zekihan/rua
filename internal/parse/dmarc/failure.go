package dmarc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"

	"github.com/zekihan/rua/internal/parse/text"
	"github.com/zekihan/rua/internal/report"
)

var errFeedbackType = errors.New("expected auth-failure feedback type")

// ParseFailure parses a feedback report and optional original message headers.
func ParseFailure(feedback, originalHeaders []byte) (*report.Failure, error) {
	header, err := readHeaders(feedback)
	if err != nil {
		return nil, fmt.Errorf("read feedback headers: %w", err)
	}

	if !strings.EqualFold(strings.TrimSpace(header.Get("Feedback-Type")), "auth-failure") {
		return nil, errFeedbackType
	}

	var original mail.Header

	if len(originalHeaders) > 0 {
		original, err = readHeaders(originalHeaders)
		if err != nil {
			return nil, fmt.Errorf("read original message headers: %w", err)
		}
	}

	var arrival int64

	date, err := mail.ParseDate(strings.TrimSpace(header.Get("Arrival-Date")))
	if err == nil {
		arrival = date.Unix()
	}

	return &report.Failure{
		Domain:       text.LowerShort(header.Get("Reported-Domain")),
		SourceIP:     text.Limit(header.Get("Source-IP"), report.MaxShort),
		AuthFailure:  text.LowerShort(header.Get("Auth-Failure")),
		OriginalFrom: decodeHeader(original.Get("From")),
		OriginalTo:   decodeHeader(original.Get("To")),
		Subject:      decodeHeader(original.Get("Subject")),
		Arrival:      arrival,
	}, nil
}

func readHeaders(data []byte) (mail.Header, error) {
	// Supply a header terminator without appending into the caller's backing array.
	reader := io.MultiReader(
		bytes.NewReader(bytes.TrimRight(data, "\r\n")),
		strings.NewReader("\r\n\r\n"),
	)

	message, err := mail.ReadMessage(reader)
	if err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}

	return message.Header, nil
}

func decodeHeader(value string) string {
	var decoder mime.WordDecoder

	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		decoded = value
	}

	return text.Limit(decoded, report.MaxMedium)
}
