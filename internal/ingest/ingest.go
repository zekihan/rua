// Package ingest classifies messages, extracts reports under hard limits,
// parses and deduplicates them, and commits each message in its own
// transaction. It owns the messages, reports, policies, records,
// auth_results, tls_policies, tls_failures and failure_reports tables, plus
// reparse and retention.
package ingest

import (
	"sync/atomic"
	"time"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

// Extraction limits (DESIGN.md section 5).
const (
	MaxReportBytes = 50 << 20 // decompressed bytes per report document
	MaxZipEntries  = 20
)

// MaxMessageBytes is the decompressed budget per message, all parts. It is
// a variable so tests can reproduce budget-boundary failures with a reduced
// limit instead of a 200 MiB fixture.
var MaxMessageBytes = 200 << 20

// Message statuses.
const (
	StatusOK      = "ok"
	StatusPartial = "partial"
	StatusError   = "error"
	StatusSkipped = "skipped"
	// StatusPruned marks a message whose reports all expired under
	// retention. It keeps its digest, so it is not refetched, but no blob.
	StatusPruned = "pruned"
)

// Source says where a message came from. Mailbox is empty for imports.
// UIDValidity is the mailbox generation observed when the message was
// fetched. Skip includes it in the digest so identical headers from
// different messages stay distinct.
type Source struct { //nolint:govet // Preserve the public field order of Source literals.
	Mailbox      string
	UID          uint32
	UIDValidity  uint32
	InternalDate time.Time
}

// Result describes what Ingest stored.
type Result struct { //nolint:govet // Preserve the public field order of Result literals.
	MessageID int64
	// Known is true when the message digest was already stored.
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
	// RetentionDays, when positive, is reapplied after every reparse so
	// reports of partly pruned messages do not come back.
	RetentionDays int
}

// Pipeline is the single write path for reports.
type Pipeline struct {
	store     *store.Store
	opts      Options
	reparsing atomic.Int32
}

// New returns a Pipeline writing to st.
func New(st *store.Store, opts Options) *Pipeline {
	if opts.Now == nil {
		opts.Now = time.Now
	}

	pipe := new(Pipeline)
	pipe.store = st
	pipe.opts = opts

	return pipe
}

// ImportSummary counts the outcome of Import.
type ImportSummary struct {
	Files    int
	Messages int
	Known    int
	Failed   int
}

// Reparsing reports whether a reparse is running.
func (pipe *Pipeline) Reparsing() bool { return pipe.reparsing.Load() > 0 }
