package imapsrc

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/zekihan/go-commons/metrics"

	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

func TestScheduleDelay(t *testing.T) {
	t.Parallel()

	interval := time.Hour
	checks := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: interval},
		{failures: 1, want: interval},
		{failures: 2, want: 2 * interval},
		{failures: 3, want: 4 * interval},
		{failures: 4, want: maxBackoff},
		{failures: 10, want: maxBackoff},
	}

	for _, check := range checks {
		got := scheduleDelay(interval, check.failures)
		if got != check.want {
			t.Fatalf("failures %d: got %s, want %s", check.failures, got, check.want)
		}
	}
}

func TestScheduleDelayBounds(t *testing.T) {
	t.Parallel()

	long := 8 * time.Hour
	if scheduleDelay(7*time.Hour, 1) != maxBackoff {
		t.Fatal("short failure was not capped")
	}

	if scheduleDelay(-time.Second, 3) != 0 {
		t.Fatal("negative interval")
	}

	if scheduleDelay(long, 0) != long {
		t.Fatal("success interval was capped")
	}

	if scheduleDelay(long, 1) != maxBackoff || scheduleDelay(long, 4) != maxBackoff {
		t.Fatal("failure interval was not capped")
	}
}

func TestNextDelay(t *testing.T) {
	t.Parallel()

	fetcher := new(Fetcher)
	fetcher.cfg = filledConfig("", "", "", "", "", nil, maxBackoff, 0)
	fetcher.jitterN = func() int64 { return maxJitterPercent }

	want := applyJitter(maxBackoff, maxJitterPercent)
	if fetcher.nextDelay(0) != want {
		t.Fatalf("success delay = %s, want %s", fetcher.nextDelay(0), want)
	}

	long := 8 * time.Hour
	fetcher.cfg.Interval = long

	fetcher.jitterN = func() int64 { return 0 }
	if fetcher.nextDelay(0) != long || fetcher.nextDelay(1) != maxBackoff {
		t.Fatalf("long delay success %s failure %s", fetcher.nextDelay(0), fetcher.nextDelay(1))
	}

	fetcher.jitterN = func() int64 { return maxJitterPercent }
	if fetcher.nextDelay(1) != maxBackoff {
		t.Fatalf("failure jitter = %s", fetcher.nextDelay(1))
	}

	fetcher.cfg.Interval = maxBackoff

	fetcher.jitterN = func() int64 { return -maxJitterPercent }

	want = maxBackoff - maxBackoff/maxJitterPercent
	if fetcher.nextDelay(0) != want {
		t.Fatalf("negative jitter = %s, want %s", fetcher.nextDelay(0), want)
	}
}

func TestJitterRange(t *testing.T) {
	t.Parallel()

	base := 100 * time.Second
	if applyJitter(base, maxJitterPercent) != 110*time.Second ||
		applyJitter(base, -maxJitterPercent) != 90*time.Second ||
		applyJitter(base, 0) != base || applyJitter(0, maxJitterPercent) != 0 {
		t.Fatal("jitter")
	}

	for range jitterSteps {
		got := randomJitter()
		if got < -maxJitterPercent || got > maxJitterPercent {
			t.Fatalf("jitter %d", got)
		}
	}
}

func TestAdvanceUID(t *testing.T) {
	t.Parallel()

	const maxUID = ^uint32(0)

	if advanceUID(1) != 2 || advanceUID(maxUID) != maxUID || normalizeNext(0) != 1 ||
		normalizeNext(5) != 5 {
		t.Fatal("uid cursor")
	}
}

//nolint:gocyclo // One connect scenario checked end to end.
func TestConnectRetry(t *testing.T) {
	t.Parallel()

	dials := &atomic.Int32{}
	addr := refuse(t, dials)
	database := openDB(t)
	clock := newFakeClock(time.Unix(1_800_000_000, 0).UTC())
	registry := metrics.New()
	fetcher := New(filledConfig(
		addr, tlsNone, "", testUser, testPassword, []string{"One", "Two"}, time.Hour, 1<<20,
	), database, new(fakeSink), telemetry.New(registry))
	clock.install(fetcher)

	errCh := make(chan error, 1)

	go func() {
		errCh <- fetcher.FetchOnce(t.Context())
	}()

	for index, want := range connectBackoff {
		got := clock.read(t)
		if got != want {
			t.Fatalf("sleep %s, want %s", got, want)
		}

		if dials.Load() != int32(index+1) {
			t.Fatalf("dials = %d before sleep %d", dials.Load(), index)
		}

		clock.Advance(got)
	}

	err := waitErr(t, errCh)
	if err == nil || !strings.Contains(err.Error(), "connect IMAP") {
		t.Fatalf("err = %v", err)
	}

	if dials.Load() != int32(len(connectBackoff)+1) {
		t.Fatalf("dials = %d", dials.Load())
	}

	clock.expectIdle(t)

	for _, name := range []string{"One", "Two"} {
		row := mailboxOf(t, database, name)
		if row.LastResult != resultError ||
			row.LastError == "" ||
			strings.Contains(row.LastError, testPassword) ||
			len(row.LastError) > maxErrorLen ||
			row.LastStart == 0 ||
			row.LastEnd == 0 ||
			row.HasSuccess {
			t.Fatalf("%s = %+v", name, row)
		}
	}

	text := scrape(registry)
	if !strings.Contains(text, `rua_fetch_runs_total{result="error"}`) ||
		strings.Contains(text, "rua_last_successful_fetch_timestamp_seconds") {
		t.Fatalf("metrics = %s", text)
	}
}

func TestConnectCancel(t *testing.T) {
	t.Parallel()

	dials := &atomic.Int32{}
	addr := refuse(t, dials)
	database := openDB(t)
	clock := newFakeClock(time.Unix(1_800_000_100, 0).UTC())
	fetcher := New(filledConfig(
		addr, tlsNone, "", "", "", []string{"One"}, time.Hour, 0,
	), database, new(fakeSink), nil)
	clock.install(fetcher)

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- fetcher.FetchOnce(ctx)
	}()

	_ = clock.read(t)

	fetcher.wake <- struct{}{}

	cancel()

	err := waitErr(t, errCh)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	clock.expectIdle(t)

	if countMailboxes(t, database) != 0 {
		t.Fatal("cancel recorded a mailbox failure")
	}
}

func TestRunBackoff(t *testing.T) {
	t.Parallel()

	dials := &atomic.Int32{}
	addr := refuse(t, dials)
	database := openDB(t)
	clock := newFakeClock(time.Unix(1_800_000_200, 0).UTC())
	fetcher := New(filledConfig(
		addr, tlsNone, "", "", "", []string{"INBOX"}, time.Hour, 0,
	), database, new(fakeSink), nil)
	clock.install(fetcher)

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- fetcher.Run(ctx)
	}()

	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, time.Hour,
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 2 * time.Hour,
	}

	for index, delay := range want {
		got := clock.read(t)
		if got != delay {
			t.Fatalf("sleep %d = %s, want %s", index, got, delay)
		}

		if index == len(want)-1 {
			break
		}

		clock.Advance(got)
	}

	if dials.Load() != 10 {
		t.Fatalf("dials = %d", dials.Load())
	}

	cancel()

	err := waitErr(t, errCh)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSuccessKeepsLongInterval(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")
	built.fetcher.cfg.Interval = 8 * time.Hour

	clock := newFakeClock(time.Unix(1_800_000_350, 0).UTC())
	clock.install(built.fetcher)

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- built.fetcher.Run(ctx)
	}()

	if got := clock.read(t); got != 8*time.Hour {
		t.Fatalf("success wait %s", got)
	}

	built.deliver(t, "Reports", "late", "body", time.Unix(1_800_000_360, 0).UTC())
	built.sink.setFailAll(true)
	clock.Advance(8 * time.Hour)

	if got := clock.read(t); got != maxBackoff {
		t.Fatalf("failure wait %s", got)
	}

	cancel()

	err := waitErr(t, errCh)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunResetsBackoff(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")
	built.deliver(t, "Reports", "one", "a", time.Unix(1_800_000_300, 0).UTC())

	clock := newFakeClock(time.Unix(1_800_000_300, 0).UTC())
	clock.install(built.fetcher)

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- built.fetcher.Run(ctx)
	}()

	if got := clock.read(t); got != time.Hour {
		t.Fatalf("first wait %s", got)
	}

	built.deliver(t, "Reports", "two", "b", time.Unix(1_800_000_400, 0).UTC())
	built.sink.setFailAll(true)
	clock.Advance(time.Hour)

	if got := clock.read(t); got != time.Hour {
		t.Fatalf("first failure wait %s", got)
	}

	clock.Advance(time.Hour)

	if got := clock.read(t); got != 2*time.Hour {
		t.Fatalf("second failure wait %s", got)
	}

	cancel()

	err := waitErr(t, errCh)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestTriggerAndOverlap(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")
	built.deliver(t, "Reports", "hold", "body", time.Unix(1_800_000_500, 0).UTC())

	clock := newFakeClock(time.Unix(1_800_000_500, 0).UTC())
	clock.install(built.fetcher)
	built.sink.block = make(chan struct{})
	built.sink.started = make(chan struct{})

	fetchCtx, fetchCancel := context.WithCancel(t.Context())
	fetchErr := make(chan error, 1)

	go func() {
		fetchErr <- built.fetcher.FetchOnce(fetchCtx)
	}()

	select {
	case <-built.sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not start")
	}

	if !built.fetcher.State().Running || !built.fetcher.State().NextFetch.IsZero() {
		t.Fatalf("state = %+v", built.fetcher.State())
	}

	err := built.fetcher.Trigger()
	if !errors.Is(err, ErrRunning) {
		t.Fatalf("trigger = %v", err)
	}

	err = built.fetcher.FetchOnce(t.Context())
	if !errors.Is(err, ErrRunning) {
		t.Fatalf("fetch = %v", err)
	}

	runCtx, runCancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)

	go func() {
		runErr <- built.fetcher.Run(runCtx)
	}()

	if got := clock.read(t); got != time.Hour {
		t.Fatalf("overlap wait %s", got)
	}

	clock.Advance(time.Hour)

	if got := clock.read(t); got != time.Hour {
		t.Fatalf("second overlap wait %s", got)
	}

	if built.sink.peak() != 1 {
		t.Fatalf("peak = %d", built.sink.peak())
	}

	runCancel()

	err = waitErr(t, runErr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %v", err)
	}

	fetchCancel()

	err = waitErr(t, fetchErr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch = %v", err)
	}

	if built.fetcher.State().Running {
		t.Fatal("still running")
	}

	err = built.fetcher.Trigger()
	if err != nil {
		t.Fatal(err)
	}
}

func TestTriggerWakesRun(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	start := time.Unix(1_800_000_600, 0).UTC()
	clock := newFakeClock(start)
	clock.install(built.fetcher)

	err := built.fetcher.Trigger()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- built.fetcher.Run(ctx)
	}()

	if got := clock.read(t); got != time.Hour {
		t.Fatalf("wait %s", got)
	}

	clock.expectIdle(t)

	if built.recorder.count("EXAMINE") != 1 {
		t.Fatalf("commands = %v", built.recorder.snapshot())
	}

	if !built.fetcher.State().NextFetch.Equal(start.Add(time.Hour)) {
		t.Fatalf("next = %s", built.fetcher.State().NextFetch)
	}

	err = built.fetcher.Trigger()
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("trigger = %v", err)
	}

	clock.Advance(triggerCooldown)

	if !built.fetcher.State().CooldownUntil.IsZero() {
		t.Fatalf("cooldown = %s", built.fetcher.State().CooldownUntil)
	}

	err = built.fetcher.Trigger()
	if err != nil {
		t.Fatal(err)
	}

	if got := clock.read(t); got != time.Hour {
		t.Fatalf("woken wait %s", got)
	}

	if built.recorder.count("EXAMINE") != 2 {
		t.Fatalf("commands = %v", built.recorder.snapshot())
	}

	cancel()

	err = waitErr(t, errCh)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestDisabled(t *testing.T) {
	t.Parallel()

	fetcher := New(filledConfig("", "", "", "", "", nil, 0, 0), openDB(t), new(fakeSink), nil)

	err := fetcher.Trigger()
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("trigger = %v", err)
	}

	err = fetcher.FetchOnce(t.Context())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("fetch = %v", err)
	}

	err = fetcher.Run(t.Context())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("run = %v", err)
	}

	state := fetcher.State()
	if state.Enabled || state.Running || !state.NextFetch.IsZero() ||
		!state.CooldownUntil.IsZero() {
		t.Fatalf("state = %+v", state)
	}
}

type fakeClock struct {
	current time.Time
	waiters map[int]*fakeWait
	slept   chan time.Duration
	nextID  int
	mu      sync.Mutex
}

type fakeWait struct {
	until time.Time
	ch    chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	clock := new(fakeClock)
	clock.current = start
	clock.waiters = map[int]*fakeWait{}
	clock.slept = make(chan time.Duration, 32)

	return clock
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	return clock.current
}

func (clock *fakeClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.current = clock.current.Add(delta)
	now := clock.current

	var due []chan time.Time

	for id, waiter := range clock.waiters {
		if !waiter.until.After(now) {
			due = append(due, waiter.ch)

			delete(clock.waiters, id)
		}
	}

	clock.mu.Unlock()

	for _, channel := range due {
		channel <- now
	}
}

func (clock *fakeClock) install(fetcher *Fetcher) {
	fetcher.now = clock.Now
	fetcher.after = clock.after
	fetcher.jitterN = func() int64 { return 0 }
}

func (clock *fakeClock) after(delay time.Duration) (<-chan time.Time, func()) {
	clock.mu.Lock()

	channel := make(chan time.Time, 1)
	id := clock.nextID
	clock.nextID++
	now := clock.current

	if delay <= 0 {
		channel <- now
	} else {
		clock.waiters[id] = &fakeWait{until: now.Add(delay), ch: channel}
	}

	clock.mu.Unlock()

	clock.slept <- delay

	stop := func() {
		clock.mu.Lock()
		delete(clock.waiters, id)
		clock.mu.Unlock()
	}

	return channel, stop
}

func (clock *fakeClock) read(t *testing.T) time.Duration {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	select {
	case delay := <-clock.slept:
		return delay
	case <-ctx.Done():
		t.Fatal("timed out waiting for sleep")
	}

	return 0
}

func (clock *fakeClock) expectIdle(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	select {
	case delay := <-clock.slept:
		t.Fatalf("unexpected sleep %s", delay)
	case <-ctx.Done():
	}
}

func refuse(t *testing.T, dials *atomic.Int32) string {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			dials.Add(1)

			_ = conn.Close()
		}
	}()

	t.Cleanup(func() { _ = listener.Close() })

	return listener.Addr().String()
}

func openDB(t *testing.T) *store.Store {
	t.Helper()

	database, err := store.Open(t.Context(), store.Memory)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = database.Close() })

	return database
}

func waitErr(t *testing.T, errCh <-chan error) error {
	t.Helper()

	return waitErrFor(t, errCh, 5*time.Second)
}

func waitErrFor(t *testing.T, errCh <-chan error, limit time.Duration) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		t.Fatal("timed out")
	}

	return nil
}

func filledConfig(
	addr, mode, caFile, user, password string,
	mailboxes []string,
	interval time.Duration,
	maxBytes int64,
) Config {
	cfg := new(Config)
	cfg.Addr = addr
	cfg.TLSMode = mode
	cfg.CAFile = caFile
	cfg.Username = user
	cfg.Password = password
	cfg.Mailboxes = mailboxes
	cfg.Interval = interval
	cfg.MaxMessageBytes = maxBytes

	return *cfg
}

func mailboxOf(t *testing.T, database *store.Store, name string) mailboxRow {
	t.Helper()

	built := new(rig)
	built.database = database

	return built.mailbox(t, name)
}

func countMailboxes(t *testing.T, database *store.Store) int {
	t.Helper()

	var total int

	err := database.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &total, `SELECT count(*) FROM mailboxes`)
	})
	if err != nil {
		t.Fatal(err)
	}

	return total
}
