// Package tlsrpt parses SMTP TLS reports (RFC 8460).
package tlsrpt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zekihan/rua/internal/parse/text"
	"github.com/zekihan/rua/internal/report"
)

var (
	errMetadata        = errors.New("missing or invalid TLS report metadata")
	errDomain          = errors.New("missing TLS policy domain")
	errSessionCount    = errors.New("invalid TLS session count")
	errTooManyFailures = errors.New("too many TLS failure details")
	errFailureArray    = errors.New("TLS failure details must be an array")
)

type stringsOrString []string

func (hosts *stringsOrString) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*hosts = nil

		return nil
	}

	var single string

	err := json.Unmarshal(data, &single)
	if err == nil {
		*hosts = []string{single}

		return nil
	}

	err = json.Unmarshal(data, (*[]string)(hosts))
	if err != nil {
		return fmt.Errorf("decode mx-host: %w", err)
	}

	return nil
}

//nolint:tagliatelle // The JSON keys are defined by RFC 8460.
type document struct {
	Org     string `json:"organization-name"`
	Contact string `json:"contact-info"`
	ID      string `json:"report-id"`
	Date    struct {
		Start string `json:"start-datetime"`
		End   string `json:"end-datetime"`
	} `json:"date-range"`
	Policies []policy `json:"policies"`
}

//nolint:tagliatelle // The JSON keys are defined by RFC 8460.
type policy struct {
	Policy struct {
		Type    string          `json:"policy-type"`
		Domain  string          `json:"policy-domain"`
		Strings []string        `json:"policy-string"`
		MX      stringsOrString `json:"mx-host"`
	} `json:"policy"`
	Failures failureDetails `json:"failure-details"`
	Summary  struct {
		Success int64 `json:"total-successful-session-count"`
		Failure int64 `json:"total-failure-session-count"`
	} `json:"summary"`
}

//nolint:tagliatelle // The JSON keys follow the RFC 8460 schema and field descriptions.
type failure struct {
	Type    string `json:"result-type"`
	Sending string `json:"sending-mta-ip"`
	IP      string `json:"receiving-ip"`
	Host    string `json:"receiving-mx-hostname"`
	Helo    string `json:"receiving-mx-helo"`
	Code    string `json:"failure-reason-code"`
	URI     string `json:"additional-information"`
	InfoURI string `json:"additional-info-uri"`
	Count   int64  `json:"failed-session-count"`
}

type failureDetails []failure

func (details *failureDetails) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))

	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode failure details: %w", err)
	}

	*details = nil

	switch token {
	case nil:
		return nil
	case json.Delim('['):
	default:
		return errFailureArray
	}

	for decoder.More() {
		if len(*details) == report.MaxRecords {
			return errTooManyFailures
		}

		var entry failure

		err = decoder.Decode(&entry)
		if err != nil {
			return fmt.Errorf("decode failure detail: %w", err)
		}

		*details = append(*details, entry)
	}

	return nil
}

// Parse parses one decompressed TLS report JSON document.
func Parse(data []byte) (*report.TLS, error) {
	var doc document

	err := json.Unmarshal(data, &doc)
	if err != nil {
		return nil, fmt.Errorf("decode TLS report: %w", err)
	}

	start, startErr := time.Parse(time.RFC3339, strings.TrimSpace(doc.Date.Start))
	end, endErr := time.Parse(time.RFC3339, strings.TrimSpace(doc.Date.End))

	if strings.TrimSpace(doc.Org) == "" || strings.TrimSpace(doc.ID) == "" ||
		startErr != nil || endErr != nil || start.After(end) || len(doc.Policies) == 0 {
		return nil, errMetadata
	}

	out := &report.TLS{
		OrgName:     text.Limit(doc.Org, report.MaxMedium),
		ContactInfo: text.Limit(doc.Contact, report.MaxMedium),
		ReportID:    text.Limit(doc.ID, report.MaxMedium),
		Begin:       start.Unix(),
		End:         end.Unix(),
		Policies:    make([]report.TLSPolicy, len(doc.Policies)),
	}
	for index := range doc.Policies {
		out.Policies[index], err = doc.Policies[index].parsed()
		if err != nil {
			return nil, fmt.Errorf("TLS policy %d: %w", index+1, err)
		}
	}

	return out, nil
}

func (wire *policy) parsed() (report.TLSPolicy, error) {
	domain := text.LowerShort(wire.Policy.Domain)
	if domain == "" {
		return report.TLSPolicy{}, errDomain
	}

	if !validCount(wire.Summary.Success) || !validCount(wire.Summary.Failure) {
		return report.TLSPolicy{}, errSessionCount
	}

	out := report.TLSPolicy{
		Type:     text.LowerShort(wire.Policy.Type),
		Domain:   domain,
		Success:  wire.Summary.Success,
		Failure:  wire.Summary.Failure,
		Strings:  nil,
		MXHosts:  nil,
		Failures: nil,
	}
	for _, value := range wire.Policy.Strings {
		out.Strings = append(out.Strings, text.Limit(value, report.MaxLong))
	}

	for _, host := range wire.Policy.MX {
		out.MXHosts = append(out.MXHosts, text.LowerShort(host))
	}

	for index := range wire.Failures {
		entry := &wire.Failures[index]
		if !validCount(entry.Count) {
			return report.TLSPolicy{}, errSessionCount
		}

		out.Failures = append(out.Failures, entry.parsed())
	}

	return out, nil
}

func (wire *failure) parsed() report.TLSFailure {
	uri := strings.TrimSpace(wire.URI)
	if uri == "" {
		uri = wire.InfoURI
	}

	return report.TLSFailure{
		ResultType:          text.LowerShort(wire.Type),
		SendingMTAIP:        text.Limit(wire.Sending, report.MaxShort),
		ReceivingIP:         text.Limit(wire.IP, report.MaxShort),
		ReceivingMXHostname: text.LowerShort(wire.Host),
		ReceivingMXHelo:     text.LowerShort(wire.Helo),
		FailureReasonCode:   text.Limit(wire.Code, report.MaxShort),
		AdditionalInfoURI:   text.Limit(uri, report.MaxLong),
		FailedSessions:      wire.Count,
	}
}

func validCount(count int64) bool {
	return count >= 0 && count <= report.MaxCount
}
