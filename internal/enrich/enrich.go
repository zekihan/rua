// Package enrich looks up PTR (forward-confirmed) and origin ASN for record
// source IPs in the background and caches them in ip_info.
package enrich

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

var errNotImplemented = errors.New("not implemented")

// TTL is how long a lookup result is reused.
const TTL = 30 * 24 * time.Hour

// Options configures a Worker.
type Options struct {
	// Enabled false makes Run return immediately (LOOKUPS=off).
	Enabled  bool
	Resolver *net.Resolver // nil means net.DefaultResolver
	Metrics  *telemetry.Metrics
	Now      func() time.Time
}

// Worker resolves IPs that have no fresh ip_info row.
type Worker struct {
	store *store.Store
	opts  Options
}

// New returns a Worker.
func New(st *store.Store, opts Options) *Worker {
	return &Worker{store: st, opts: opts}
}

// Run processes the queue until ctx is done, rate-limited, never blocking
// ingestion or page loads.
func (w *Worker) Run(ctx context.Context) error {
	_ = ctx

	return errNotImplemented
}

// Notify wakes the worker after new records were stored. Never blocks.
func (w *Worker) Notify() {}

// QueueLength returns the number of IPs waiting for a lookup.
func (w *Worker) QueueLength() int { return 0 }
