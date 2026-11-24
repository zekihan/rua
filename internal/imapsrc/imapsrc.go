// Package imapsrc fetches new messages from IMAP mailboxes, read-only. It is
// the only code that talks to the mail server and the only owner of the
// mailboxes and events tables.
package imapsrc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

var (
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
	NextFetch     time.Time // zero when not scheduled
	CooldownUntil time.Time // zero when Fetch now is accepted
	Enabled       bool
	Running       bool
}

// Fetcher runs fetches. Fetches never overlap.
type Fetcher struct {
	nextFetch     time.Time
	cooldownUntil time.Time
	sink          Sink
	database      *store.Store
	metrics       *telemetry.Metrics
	logger        *slog.Logger
	now           func() time.Time
	after         func(time.Duration) (<-chan time.Time, func())
	jitterN       func() int64
	afterLoad     func(string)
	beforeSave    func(string, uint32)
	wake          chan struct{}
	cfg           Config
	limits        timeLimits
	mu            sync.Mutex
	running       bool
}

// New returns a Fetcher. An empty address disables IMAP.
//
//nolint:gocritic // hugeParam: Config is the exported constructor argument.
func New(cfg Config, database *store.Store, sink Sink, metrics *telemetry.Metrics) *Fetcher {
	fetcher := new(Fetcher)
	fetcher.cfg = cfg
	fetcher.database = database
	fetcher.sink = sink
	fetcher.metrics = metrics
	fetcher.now = time.Now
	fetcher.after = newTimer
	fetcher.jitterN = randomJitter
	fetcher.wake = make(chan struct{}, 1)

	return fetcher
}

func newTimer(delay time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(delay)

	return timer.C, func() { timer.Stop() }
}

// FetchOnce runs one fetch of every mailbox.
func (fetcher *Fetcher) FetchOnce(ctx context.Context) error {
	err := fetcher.tryStart()
	if err != nil {
		return err
	}

	defer fetcher.finish()

	started := fetcher.now()
	err = fetcher.fetch(ctx)

	if isCancel(err) {
		return err
	}

	fetcher.metrics.FetchFinished(fetcher.now().Sub(started).Seconds(), err == nil)

	if err == nil {
		fetcher.metrics.LastSuccess(fetcher.now().Unix())
	}

	return err
}

// Run fetches immediately, then on the schedule (with backoff while failing)
// and on Trigger, until ctx is done.
func (fetcher *Fetcher) Run(ctx context.Context) error {
	if fetcher.cfg.Addr == "" {
		return ErrDisabled
	}

	select {
	case <-fetcher.wake:
	default:
	}

	failures := 0

	for {
		err := ctx.Err()
		if err != nil {
			return contextError(err)
		}

		err = fetcher.FetchOnce(ctx)
		if isCancel(err) {
			return err
		}

		switch {
		case errors.Is(err, ErrRunning):
		case err != nil:
			failures++

			fetcher.log().ErrorContext(ctx, "Fetch attempt failed",
				slog.String("error", clipError(err, fetcher.secrets()...)),
			)
		default:
			failures = 0
		}

		err = fetcher.wait(ctx, fetcher.nextDelay(failures))
		if err != nil {
			return err
		}
	}
}

// Trigger requests a fetch now (the UI's "Fetch now").
func (fetcher *Fetcher) Trigger() error {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()

	if fetcher.cfg.Addr == "" {
		return ErrDisabled
	}

	if fetcher.running {
		return ErrRunning
	}

	now := fetcher.now()
	if now.Before(fetcher.cooldownUntil) {
		return ErrCooldown
	}

	fetcher.cooldownUntil = now.Add(triggerCooldown)

	select {
	case fetcher.wake <- struct{}{}:
	default:
	}

	return nil
}

// State returns the scheduler state.
func (fetcher *Fetcher) State() State {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()

	cooldown := fetcher.cooldownUntil
	if !cooldown.After(fetcher.now()) {
		cooldown = time.Time{}
	}

	return State{
		Enabled:       fetcher.cfg.Addr != "",
		Running:       fetcher.running,
		NextFetch:     fetcher.nextFetch,
		CooldownUntil: cooldown,
	}
}

func (fetcher *Fetcher) tryStart() error {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()

	if fetcher.cfg.Addr == "" {
		return ErrDisabled
	}

	if fetcher.running {
		return ErrRunning
	}

	fetcher.running = true
	fetcher.nextFetch = time.Time{}

	return nil
}

func (fetcher *Fetcher) finish() {
	fetcher.mu.Lock()
	fetcher.running = false
	fetcher.mu.Unlock()
}

func (fetcher *Fetcher) setNext(at time.Time) {
	fetcher.mu.Lock()
	fetcher.nextFetch = at
	fetcher.mu.Unlock()
}

// wait blocks until delay elapses, Trigger fires, or ctx is done.
func (fetcher *Fetcher) wait(ctx context.Context, delay time.Duration) error {
	channel, stop := fetcher.after(delay)
	fetcher.setNext(fetcher.now().Add(delay))

	defer func() {
		stop()
		fetcher.setNext(time.Time{})
	}()

	select {
	case <-ctx.Done():
		return contextError(ctx.Err())
	case <-channel:
		return nil
	case <-fetcher.wake:
		return nil
	}
}

func (fetcher *Fetcher) log() *slog.Logger {
	if fetcher != nil && fetcher.logger != nil {
		return fetcher.logger
	}

	return slog.Default()
}

func (fetcher *Fetcher) secrets() []string {
	return []string{fetcher.cfg.Username, fetcher.cfg.Password}
}

// safe drops any wrapped server text and redacts the configured username and
// password. Cancellation is preserved so a fetch can stop without recording
// a failure.
func (fetcher *Fetcher) safe(err error) error {
	if err == nil || isCancel(err) {
		return err
	}

	return redactedError(scrubText(err.Error(), fetcher.secrets()))
}

func isCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func contextError(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w", err)
}

func (fetcher *Fetcher) sleep(ctx context.Context, delay time.Duration) error {
	channel, stop := fetcher.after(delay)
	defer stop()

	select {
	case <-ctx.Done():
		return contextError(ctx.Err())
	case <-channel:
		return nil
	}
}
