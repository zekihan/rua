// Package dmarc parses DMARC aggregate reports (RFC 7489 appendix C and the
// DMARCbis dmarc-2.0 schema) and the header fields of failure reports.
package dmarc

import (
	"errors"

	"github.com/zekihan/rua/internal/report"
)

var errNotImplemented = errors.New("not implemented")

// ParseAggregate parses one decompressed aggregate report XML document.
// Unknown elements are ignored; a missing required element (report_id,
// org_name, date_range, policy_published/domain) fails the report.
func ParseAggregate(data []byte) (*report.Aggregate, error) {
	_ = data

	return nil, errNotImplemented
}

// ParseFailure parses a failure (ruf) report from its
// message/feedback-report part and, when present, the headers of the
// original message (text/rfc822-headers or the header of message/rfc822).
func ParseFailure(feedback, originalHeaders []byte) (*report.Failure, error) {
	_, _ = feedback, originalHeaders

	return nil, errNotImplemented
}
