// Package ingest classifies messages, extracts reports under hard limits,
// parses and deduplicates them, and commits each message in its own
// transaction. It owns the messages, reports, policies, records,
// auth_results, tls_policies, tls_failures and failure_reports tables, plus
// reparse and retention.
package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

var errNotImplemented = errors.New("not implemented")

// Extraction limits (DESIGN.md section 5).
const (
	MaxReportBytes  = 50 << 20  // decompressed bytes per report document
	MaxMessageBytes = 200 << 20 // decompressed bytes per message, all parts
	MaxZipEntries   = 20
)

// Message statuses.
const (
	StatusOK      = "ok"
	StatusPartial = "partial"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Source says where a message came from. Mailbox is empty for imports.
type Source struct {
	Mailbox      string
	UID          uint32
	InternalDate time.Time
}

// Result describes what Ingest stored.
type Result struct {
	MessageID int64
	// Known is true when a message with the same SHA-256 was already stored;
	// nothing was written.
	Known   bool
	Class   report.Class
	Status  string
	Reports int // report rows written, duplicates included
}

// Options configures a Pipeline.
type Options struct {
	Metrics *telemetry.Metrics
	Now     func() time.Time
	// NewIPs is called (without blocking) after a commit that added records,
	// so enrichment can pick up new source IPs. May be nil.
	NewIPs func()
}

// Pipeline is the single write path for reports.
type Pipeline struct {
	store *store.Store
	opts  Options
}

// New returns a Pipeline writing to st.
func New(st *store.Store, opts Options) *Pipeline {
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Pipeline{store: st, opts: opts}
}

// Ingest classifies, parses and stores one raw message (RFC 5322 bytes, or
// for imports a bare report document) in one transaction. A message that
// cannot be parsed is still stored with its class, status and error. It is
// safe to call again with the same bytes.
func (p *Pipeline) Ingest(ctx context.Context, raw []byte, src Source) (Result, error) {
	_, _, _ = ctx, raw, src

	return Result{}, errNotImplemented
}

// Skip records a message that was too large to fetch, from its header bytes
// and its full size, so it is listed on the Status page and not refetched.
func (p *Pipeline) Skip(
	ctx context.Context,
	header []byte,
	size int64,
	src Source,
) (Result, error) {
	_, _, _, _ = ctx, header, size, src

	return Result{}, errNotImplemented
}

// ImportSummary counts the outcome of Import.
type ImportSummary struct {
	Files    int
	Messages int
	Known    int
	Failed   int
}

// Import ingests local .xml, .xml.gz, .zip, .json, .json.gz, .eml and mbox
// files (and directories of them) through Ingest with an empty mailbox.
func (p *Pipeline) Import(ctx context.Context, paths []string) (ImportSummary, error) {
	_, _ = ctx, paths

	return ImportSummary{}, errNotImplemented
}

// ReparseIfNeeded reparses every stored message when the stored parser
// version differs from report.ParserVersion. It reports whether it ran.
func (p *Pipeline) ReparseIfNeeded(ctx context.Context) (bool, error) {
	_ = ctx

	return false, errNotImplemented
}

// Reparse rebuilds every derived row from the stored blobs, in one write
// transaction so readers never see a half-rebuilt state, and records the
// parser version.
func (p *Pipeline) Reparse(ctx context.Context) (int, error) {
	_ = ctx

	return 0, errNotImplemented
}

// Reparsing reports whether a reparse is running.
func (p *Pipeline) Reparsing() bool { return false }

// Prune deletes reports whose window ended more than days ago, with their
// derived rows and unreferenced report blobs. 0 does nothing.
func (p *Pipeline) Prune(ctx context.Context, days int) (int, error) {
	_, _ = ctx, days

	return 0, errNotImplemented
}
