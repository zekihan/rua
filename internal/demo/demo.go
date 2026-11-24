// Package demo generates a realistic synthetic dataset: aggregate reports
// from several receivers over a year, forwarded mail, a spoofer, a policy
// change, TLS reports with failures, duplicates and a few broken messages.
// The data is fed through the ingest pipeline so it exercises the real code.
package demo

import (
	"context"
	"errors"
	"time"

	"github.com/zekihan/rua/internal/ingest"
)

var errNotImplemented = errors.New("not implemented")

// Sources is the SOURCES_FILE content matching the generated data.
const Sources = ``

// Generate ingests the dataset, ending at now, into p. It is deterministic
// for a given now.
func Generate(ctx context.Context, p *ingest.Pipeline, now time.Time) error {
	_, _, _ = ctx, p, now

	return errNotImplemented
}
