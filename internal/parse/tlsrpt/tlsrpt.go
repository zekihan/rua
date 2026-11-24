// Package tlsrpt parses SMTP TLS reports (RFC 8460).
package tlsrpt

import (
	"errors"

	"github.com/zekihan/rua/internal/report"
)

var errNotImplemented = errors.New("not implemented")

// Parse parses one decompressed TLS report JSON document.
func Parse(data []byte) (*report.TLS, error) {
	_ = data

	return nil, errNotImplemented
}
