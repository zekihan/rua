package enrich

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/zekihan/go-commons/metrics"
	"golang.org/x/time/rate"

	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

func TestRunDisabled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	worker := New(nil, Options{Resolver: nil, Metrics: nil, Now: nil, Enabled: false})

	err := worker.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if worker.passes.Load() != 0 {
		t.Fatal("disabled worker ran")
	}
}

func TestNotifyDoesNotBlock(t *testing.T) {
	t.Parallel()

	worker := New(nil, Options{Resolver: nil, Metrics: nil, Now: nil, Enabled: true})
	done := make(chan struct{})

	go func() {
		worker.Notify()
		worker.Notify()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked")
	}
}

func TestQueueSelection(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()

	seedRecords(t, st, []string{
		"192.0.2.1", "192.0.2.1", "192.0.2.2", "198.51.100.3",
		"203.0.113.4", "203.0.113.5", "203.0.113.6", "",
	})
	insertInfo(t, st, "192.0.2.2", fixed.Add(-time.Minute).Unix(), "")
	insertInfo(t, st, "198.51.100.3", fixed.Add(-TTL-time.Second).Unix(), "")
	insertInfo(t, st, "203.0.113.4", fixed.Add(-retryAfter-time.Second).Unix(), "timeout")
	insertInfo(t, st, "203.0.113.5", fixed.Add(-time.Minute).Unix(), "timeout")
	insertInfo(t, st, "203.0.113.6", fixed.Add(-TTL).Unix(), "")

	registry := metrics.New()
	worker := newWorker(st, fixed, nil)
	worker.metrics = telemetry.New(registry)

	got := mustDue(t, worker)
	want := []string{"192.0.2.1", "198.51.100.3", "203.0.113.4"}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queue = %v", got)
	}

	if worker.QueueLength() != len(want) {
		t.Fatalf("length = %d", worker.QueueLength())
	}

	var body bytes.Buffer

	registry.WritePrometheus(&body)

	needle := "rua_enrich_queue_length " + strconv.Itoa(len(want))
	if !bytes.Contains(body.Bytes(), []byte(needle)) {
		t.Fatalf("metrics = %s", body.String())
	}
}

func TestPTRConfirmation(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	confirmedIP := "192.0.2.10"
	rejectedIP := "192.0.2.11"
	seedRecords(t, st, []string{confirmedIP, rejectedIP})

	resolver := newScript(map[string][]string{
		confirmedIP: {"mail.example.com."},
		rejectedIP:  {"other.example.com."},
	}, map[string][]string{
		"mail.example.com":  {"192.0.2.10"},
		"other.example.com": {"198.51.100.1"},
	}, map[string][]string{}, map[string]error{})
	worker := newWorker(st, fixed, resolver)

	err := worker.process(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	confirmed := readInfo(t, st, confirmedIP)
	if confirmed.ptr != "mail.example.com" || confirmed.confirmed != 1 || confirmed.problem != "" {
		t.Fatalf("confirmed = %+v", confirmed)
	}

	rejected := readInfo(t, st, rejectedIP)
	if rejected.ptr != "other.example.com" || rejected.confirmed != 0 || rejected.problem != "" {
		t.Fatalf("rejected = %+v", rejected)
	}

	if len(mustDue(t, worker)) != 0 {
		t.Fatal("fresh rows were queued again")
	}
}

func TestCymruLookup(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	first := "216.90.108.31"
	second := "216.90.108.32"
	v6 := "2001:4860:b002::68"
	seedRecords(t, st, []string{first, second, v6})

	origin4 := "31.108.90.216.origin.asn.cymru.com"
	origin4b := "32.108.90.216.origin.asn.cymru.com"
	origin6 := "8.6.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.2.0.0.b.0.6.8.4.1.0.0.2.origin6.asn.cymru.com"
	as23028 := "AS23028.asn.cymru.com"
	as15169 := "AS15169.asn.cymru.com"
	resolver := newScript(map[string][]string{}, map[string][]string{}, map[string][]string{
		origin4:               {"701 1239 | 216.90.108.0/24 | US | arin | 1998-09-25"},
		origin4b:              {"701 1239 | 216.90.108.0/24 | US | arin | 1998-09-25"},
		origin6:               {"15169 | 2001:4860::/32 | US | arin | 2005-03-14"},
		as23028:               {"701 | US | arin | 1998-09-25 | EXAMPLE - Example Net, US"},
		"AS701.asn.cymru.com": {"701 | US | arin | 1998-09-25 | EXAMPLE - Example Net, US"},
		as15169:               {"15169 | US | arin | 2000-03-30 | GOOGLE - Google LLC, US"},
	}, map[string]error{})
	worker := newWorker(st, fixed, resolver)

	err := worker.process(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got := readInfo(t, st, first)
	if got.asn != 701 || got.org != "EXAMPLE - Example Net" || got.problem != "" {
		t.Fatalf("ipv4 = %+v", got)
	}

	v6info := readInfo(t, st, v6)
	if v6info.asn != 15169 || v6info.org != "GOOGLE - Google LLC" {
		t.Fatalf("ipv6 = %+v", v6info)
	}

	if countNames(resolver.seen(), "AS701.asn.cymru.com") != 1 {
		t.Fatalf("asn queries = %v", resolver.seen())
	}
}

func TestLookupFailureRetries(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	ip := "192.0.2.20"
	missing := "192.0.2.21"
	seedRecords(t, st, []string{ip, missing})

	current := fixed
	resolver := newScript(
		map[string][]string{},
		map[string][]string{},
		map[string][]string{},
		map[string]error{
			"ptr:" + ip: dnsFailure{
				Err: "server misbehaving", Name: ip, IsTemporary: true, IsNotFound: false,
			}.dns(),
			"ptr:" + missing: dnsFailure{
				Err: "no such host", Name: missing, IsTemporary: false, IsNotFound: true,
			}.dns(),
			"txt:" + cymruOriginName(netip.MustParseAddr(missing)): dnsFailure{
				Err: "no such host", Name: missing, IsTemporary: false, IsNotFound: true,
			}.dns(),
		},
	)
	worker := newWorker(st, fixed, resolver)
	worker.now = func() time.Time { return current }

	err := worker.process(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	failed := readInfo(t, st, ip)
	if failed.problem == "" || failed.lookedUp != current.Unix() {
		t.Fatalf("failure = %+v", failed)
	}

	quiet := readInfo(t, st, missing)
	if quiet.problem != "" || quiet.ptr != "" || quiet.asn != 0 {
		t.Fatalf("nxdomain = %+v", quiet)
	}

	if strings.Join(mustDue(t, worker), ",") != "" {
		t.Fatalf("retried too soon: %v", mustDue(t, worker))
	}

	current = fixed.Add(retryAfter + time.Second)

	if strings.Join(mustDue(t, worker), ",") != ip {
		t.Fatalf("retry queue = %v", mustDue(t, worker))
	}
}

func TestRateLimitRespectsCancel(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()

	seedRecords(t, st, []string{"192.0.2.30"})

	worker := newWorker(st, fixed, newScript(
		map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string]error{},
	))
	worker.limiter = rate.NewLimiter(rate.Every(time.Hour), lookupBurst)

	if !worker.limiter.Allow() {
		t.Fatal("expected a burst token")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- worker.process(ctx)
	}()

	time.AfterFunc(20*time.Millisecond, cancel)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("process: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rate limit blocked cancellation")
	}
}

func TestNotifyWakesRun(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	worker := newWorker(st, fixed, newScript(
		map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string]error{},
	))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- worker.Run(ctx)
	}()

	waitFor(t, func() bool { return worker.passes.Load() >= 1 })
	seedRecords(t, st, []string{"192.0.2.8"})
	worker.Notify()
	waitFor(t, func() bool { return infoCount(t, st, "192.0.2.8") == 1 })
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestLongPTRIsCapped(t *testing.T) {
	t.Parallel()

	st := openStore(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	ip := "192.0.2.40"
	seedRecords(t, st, []string{ip})

	name := strings.Repeat("a", maxTextBytes+20) + ".example.com"
	resolver := newScript(map[string][]string{
		ip: {name + "."},
	}, map[string][]string{
		name: {ip},
	}, map[string][]string{}, map[string]error{})
	worker := newWorker(st, fixed, resolver)

	err := worker.process(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got := readInfo(t, st, ip)
	if len(got.ptr) != maxTextBytes || got.confirmed != 0 {
		t.Fatalf("ptr len = %d confirmed = %d", len(got.ptr), got.confirmed)
	}
}

type infoRow struct {
	ptr       string
	org       string
	problem   string
	asn       int64
	lookedUp  int64
	confirmed int
}

func newWorker(st *store.Store, now time.Time, resolver dnsResolver) *Worker {
	worker := New(st, Options{
		Resolver: nil,
		Metrics:  nil,
		Now:      func() time.Time { return now },
		Enabled:  true,
	})
	worker.limiter = rate.NewLimiter(rate.Inf, lookupBurst)

	if resolver != nil {
		worker.resolver = resolver
	}

	return worker
}

func mustDue(t *testing.T, worker *Worker) []string {
	t.Helper()

	ips, err := worker.dueIPs(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return ips
}

func openStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(t.Context(), store.Memory)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		closeErr := st.Close()
		if closeErr != nil {
			t.Error(closeErr)
		}
	})

	return st
}

var seedNonce atomic.Int64

func seedRecords(t *testing.T, st *store.Store, ips []string) {
	t.Helper()

	nonce := seedNonce.Add(1)
	payload := []byte(t.Name() + "/" + strconv.FormatInt(nonce, 10) + "/" + strings.Join(ips, ","))

	err := st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		blob, putErr := store.PutBlob(ctx, tx, payload)
		if putErr != nil {
			return wrapDB(putErr)
		}

		result, execErr := tx.ExecContext(ctx, `
			INSERT INTO messages (
				sha256, internaldate, size, class, status, parser_version, ingested_at
			) VALUES (?, 0, 1, 'dmarc-aggregate', 'ok', 1, 0)`, blob)
		if execErr != nil {
			return wrapDB(execErr)
		}

		messageID, idErr := result.LastInsertId()
		if idErr != nil {
			return wrapDB(idErr)
		}

		result, execErr = tx.ExecContext(ctx, `
			INSERT INTO reports (
				kind, message, blob, org, report_id, begin, end, domain
			) VALUES ('dmarc-aggregate', ?, ?, 'reporter', ?, 0, 1, 'example.com')`,
			messageID, blob, hex.EncodeToString(blob)+strconv.FormatInt(nonce, 10))
		if execErr != nil {
			return wrapDB(execErr)
		}

		reportID, idErr := result.LastInsertId()
		if idErr != nil {
			return wrapDB(idErr)
		}

		for _, ip := range ips {
			_, execErr = tx.ExecContext(
				ctx,
				`INSERT INTO records (report, source_ip, count) VALUES (?, ?, 1)`,
				reportID,
				ip,
			)
			if execErr != nil {
				return wrapDB(execErr)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertInfo(t *testing.T, st *store.Store, ip string, lookedUp int64, problem string) {
	t.Helper()

	err := st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO ip_info (ip, looked_up_at, error) VALUES (?, ?, ?)`,
			ip, lookedUp, problem)
		if execErr != nil {
			return wrapDB(execErr)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readInfo(t *testing.T, st *store.Store, ip string) infoRow {
	t.Helper()

	var row infoRow

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		getErr := tx.QueryRowContext(ctx, `
			SELECT ptr, ptr_confirmed, asn, as_org, looked_up_at, error
			FROM ip_info WHERE ip = ?`, ip).Scan(
			&row.ptr, &row.confirmed, &row.asn, &row.org, &row.lookedUp, &row.problem,
		)
		if getErr != nil {
			return wrapDB(getErr)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return row
}

func infoCount(t *testing.T, st *store.Store, ip string) int {
	t.Helper()

	var count int

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		getErr := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM ip_info WHERE ip = ?`, ip)
		if getErr != nil {
			return wrapDB(getErr)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return count
}

func wrapDB(err error) error {
	return fmt.Errorf("database: %w", err)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()

	for {
		if cond() {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("condition not met")
		case <-tick.C:
		}
	}
}

type scriptedResolver struct {
	ptr      map[string][]string
	ips      map[string][]string
	txt      map[string][]string
	failures map[string]error
	queries  []string
	mu       sync.Mutex
}

func newScript(
	ptr map[string][]string,
	ips map[string][]string,
	txt map[string][]string,
	failures map[string]error,
) *scriptedResolver {
	return &scriptedResolver{
		ptr:      ptr,
		ips:      ips,
		txt:      txt,
		failures: failures,
		queries:  nil,
		mu:       sync.Mutex{},
	}
}

func (resolver *scriptedResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	err := resolver.fail(ctx, "ptr:"+addr)
	if err != nil {
		return nil, err
	}

	return resolver.ptr[addr], nil
}

func (resolver *scriptedResolver) LookupIPAddr(
	ctx context.Context,
	host string,
) ([]net.IPAddr, error) {
	err := resolver.fail(ctx, "ip:"+host)
	if err != nil {
		return nil, err
	}

	raw := resolver.ips[host]
	addrs := make([]net.IPAddr, 0, len(raw))

	for _, item := range raw {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(item), Zone: ""})
	}

	return addrs, nil
}

func (resolver *scriptedResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	resolver.mu.Lock()
	resolver.queries = append(resolver.queries, name)
	resolver.mu.Unlock()

	err := resolver.fail(ctx, "txt:"+name)
	if err != nil {
		return nil, err
	}

	return resolver.txt[name], nil
}

func (resolver *scriptedResolver) fail(ctx context.Context, key string) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}

	return resolver.failures[key]
}

func (resolver *scriptedResolver) seen() []string {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	return append([]string(nil), resolver.queries...)
}

func countNames(names []string, want string) int {
	count := 0

	for _, name := range names {
		if name == want {
			count++
		}
	}

	return count
}

type dnsFailure struct {
	Err         string
	Name        string
	IsTemporary bool
	IsNotFound  bool
}

func (failure dnsFailure) dns() *net.DNSError {
	return &net.DNSError{
		UnwrapErr:   nil,
		Err:         failure.Err,
		Name:        failure.Name,
		Server:      "",
		IsTimeout:   false,
		IsTemporary: failure.IsTemporary,
		IsNotFound:  failure.IsNotFound,
	}
}
