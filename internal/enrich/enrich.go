// Package enrich looks up PTR (forward-confirmed) and origin ASN for record
// source IPs in the background and caches them in ip_info.
package enrich

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/time/rate"

	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

const (
	// TTL is how long a successful lookup result is reused.
	TTL = 30 * 24 * time.Hour

	retryAfter        = time.Hour
	lookupTimeout     = 5 * time.Second
	queuePollInterval = 10 * time.Minute
	// Each IP costs up to four DNS queries (PTR, forward, two TXT).
	lookupsPerSecond = 2
	lookupBurst      = 1
	notifyCapacity   = 1
	confirmedFlag    = 1
)

const queueQuery = `
SELECT DISTINCT records.source_ip
FROM records
LEFT JOIN ip_info ON ip_info.ip = records.source_ip
WHERE records.source_ip != ''
  AND (
        ip_info.ip IS NULL
        OR ip_info.looked_up_at < ?
        OR (ip_info.error != '' AND ip_info.looked_up_at < ?)
  )
ORDER BY records.source_ip`

const upsertInfo = `
INSERT INTO ip_info (
    ip, ptr, ptr_confirmed, asn, as_org, looked_up_at, error
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (ip) DO UPDATE SET
    ptr = excluded.ptr,
    ptr_confirmed = excluded.ptr_confirmed,
    asn = excluded.asn,
    as_org = excluded.as_org,
    looked_up_at = excluded.looked_up_at,
    error = excluded.error`

// Options configures a Worker.
type Options struct {
	Resolver *net.Resolver
	Metrics  *telemetry.Metrics
	Now      func() time.Time
	// Enabled false makes Run return immediately (LOOKUPS=off).
	Enabled bool
}

// Worker resolves IPs that have no fresh ip_info row.
type Worker struct {
	store     *store.Store
	resolver  dnsResolver
	metrics   *telemetry.Metrics
	now       func() time.Time
	notify    chan struct{}
	limiter   *rate.Limiter
	asnOrgs   map[uint32]string
	queueLen  atomic.Int64
	passes    atomic.Int64
	pollEvery time.Duration
	enabled   bool
}

// dnsResolver is the subset of *net.Resolver used for enrichment. Tests
// substitute a fake; production uses Options.Resolver.
type dnsResolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// New returns a Worker. A nil Options.Resolver uses net.DefaultResolver.
func New(st *store.Store, opts Options) *Worker {
	resolver := dnsResolver(net.DefaultResolver)
	if opts.Resolver != nil {
		resolver = opts.Resolver
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Worker{
		store:     st,
		resolver:  resolver,
		metrics:   opts.Metrics,
		now:       now,
		notify:    make(chan struct{}, notifyCapacity),
		limiter:   rate.NewLimiter(rate.Every(time.Second/lookupsPerSecond), lookupBurst),
		asnOrgs:   make(map[uint32]string),
		queueLen:  atomic.Int64{},
		passes:    atomic.Int64{},
		pollEvery: queuePollInterval,
		enabled:   opts.Enabled,
	}
}

// Run processes the queue until ctx is done. Lookups are rate-limited and
// never block ingestion or page loads. When Options.Enabled is false, Run
// returns immediately.
func (worker *Worker) Run(ctx context.Context) error {
	if !worker.enabled {
		return nil
	}

	ticker := time.NewTicker(worker.pollEvery)
	defer ticker.Stop()

	for {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("enrich: %w", err)
		}

		worker.passes.Add(1)

		err = worker.process(ctx)
		if err != nil {
			err = stopOrLog(ctx, err)
			if err != nil {
				return err
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("enrich: %w", ctx.Err())
		case <-worker.notify:
		case <-ticker.C:
		}
	}
}

func stopOrLog(ctx context.Context, err error) error {
	parent := ctx.Err()
	if parent != nil {
		return fmt.Errorf("enrich: %w", parent)
	}

	slog.ErrorContext(
		ctx,
		"Enrichment queue failed",
		slog.Any("error", err),
	)

	return nil
}

// Notify wakes the worker after new records were stored. It never blocks.
func (worker *Worker) Notify() {
	select {
	case worker.notify <- struct{}{}:
	default:
	}
}

// QueueLength returns the number of IPs waiting for a lookup at the last
// queue computation.
func (worker *Worker) QueueLength() int {
	return int(worker.queueLen.Load())
}

func (worker *Worker) process(ctx context.Context) error {
	ips, err := worker.dueIPs(ctx)
	if err != nil {
		return err
	}

	for _, raw := range ips {
		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("enrich: %w", err)
		}

		err = worker.limiter.Wait(ctx)
		if err != nil {
			return fmt.Errorf("wait for lookup rate: %w", err)
		}

		err = worker.enrichIP(ctx, raw)
		if err != nil {
			return err
		}
	}

	return nil
}

func (worker *Worker) dueIPs(ctx context.Context) ([]string, error) {
	now := worker.now()
	ttlBefore := now.Add(-TTL).Unix()
	retryBefore := now.Add(-retryAfter).Unix()

	var ips []string

	err := worker.store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		selectErr := tx.SelectContext(ctx, &ips, queueQuery, ttlBefore, retryBefore)
		if selectErr != nil {
			return fmt.Errorf("list enrichment queue: %w", selectErr)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read enrichment queue: %w", err)
	}

	worker.setQueueLength(len(ips))

	return ips, nil
}

func (worker *Worker) setQueueLength(length int) {
	worker.queueLen.Store(int64(length))
	worker.metrics.EnrichQueue(length)
}

func (worker *Worker) enrichIP(ctx context.Context, raw string) error {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return worker.save(ctx, raw, failure(err.Error()))
	}

	info, err := worker.resolve(ctx, addr.Unmap().WithZone(""))
	if err != nil {
		return err
	}

	return worker.save(ctx, raw, info)
}

func (worker *Worker) save(ctx context.Context, ip string, info resolved) error {
	confirmed := 0
	if info.confirmed {
		confirmed = confirmedFlag
	}

	err := worker.store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(
			ctx,
			upsertInfo,
			ip,
			sanitize(info.ptr),
			confirmed,
			int64(info.asn),
			sanitize(info.org),
			worker.now().Unix(),
			sanitize(info.problem),
		)
		if execErr != nil {
			return fmt.Errorf("upsert ip_info: %w", execErr)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("write ip_info: %w", err)
	}

	return nil
}
