// Package imapsrc fetches new messages from IMAP mailboxes, read-only. It is
// the only code that talks to the mail server and the only owner of the
// mailboxes and events tables.
package imapsrc

import (
	"context"
	"errors"
	"time"

	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

var (
	errNotImplemented = errors.New("not implemented")
	// ErrRunning is returned by Trigger while a fetch runs.
	ErrRunning = errors.New("a fetch is running")
	// ErrCooldown is returned by Trigger within 5 minutes of the last
	// accepted trigger.
	ErrCooldown = errors.New("fetch requested too recently")
	// ErrDisabled is returned by Trigger when IMAP is not configured.
	ErrDisabled = errors.New("IMAP is not configured")
)

// Sink receives fetched messages. *ingest.Pipeline implements it.
type Sink interface {
	Ingest(ctx context.Context, raw []byte, src ingest.Source) (ingest.Result, error)
	Skip(ctx context.Context, header []byte, size int64, src ingest.Source) (ingest.Result, error)
}

// Config is the IMAP part of the configuration.
type Config struct {
	Addr      string
	TLSMode   string // tls, starttls, none
	CAFile    string
	Username  string
	Password  string
	Mailboxes []string
	// Interval is the base fetch interval (±10% jitter).
	Interval        time.Duration
	MaxMessageBytes int64
}

// State is the scheduler state shown on the Status page.
type State struct {
	Enabled       bool
	Running       bool
	NextFetch     time.Time // zero when not scheduled
	CooldownUntil time.Time // zero when Fetch now is accepted
}

// Fetcher runs fetches. Fetches never overlap.
type Fetcher struct {
	cfg     Config
	store   *store.Store
	sink    Sink
	metrics *telemetry.Metrics
}

// New returns a Fetcher. cfg.Addr empty means disabled.
func New(cfg Config, st *store.Store, sink Sink, m *telemetry.Metrics) *Fetcher {
	return &Fetcher{cfg: cfg, store: st, sink: sink, metrics: m}
}

// FetchOnce runs one fetch of every mailbox.
func (f *Fetcher) FetchOnce(ctx context.Context) error {
	_ = ctx

	return errNotImplemented
}

// Run fetches immediately, then on the schedule (with backoff while failing)
// and on Trigger, until ctx is done.
func (f *Fetcher) Run(ctx context.Context) error {
	_ = ctx

	return errNotImplemented
}

// Trigger requests a fetch now (the UI's "Fetch now").
func (f *Fetcher) Trigger() error { return ErrDisabled }

// State returns the scheduler state.
func (f *Fetcher) State() State { return State{} }
