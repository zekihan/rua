// Package query computes every /api view from individual records. Nothing
// is pre-aggregated: SQL selects rows by unix time and Go buckets them into
// days of the requested time zone (DESIGN.md section 7).
package query

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/zekihan/rua/internal/imapsrc"
	"github.com/zekihan/rua/internal/sources"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

var errNotImplemented = errors.New("not implemented")

// ErrBadRequest wraps invalid parameters; ErrNotFound unknown keys or ids.
var (
	ErrBadRequest = errors.New("bad request")
	ErrNotFound   = errors.New("not found")
)

// Deps are the Service's collaborators. Func fields may be nil.
type Deps struct {
	Store       *store.Store
	Sources     func() *sources.Set
	Fetch       func() imapsrc.State
	Reparsing   func() bool
	EnrichQueue func() int
	Now         func() time.Time
	StaleAfter  time.Duration
	IMAPEnabled bool
}

// Filter is the parsed form of the common query parameters.
type Filter struct {
	TZ          *time.Location
	Domain      string
	From        string
	To          string
	Reporter    string
	Kind        string
	Cursor      string
	Limit       int
	UnknownOnly bool
}

// Service answers the /api queries.
type Service struct {
	deps Deps
}

// New returns a Service.
func New(deps Deps) *Service {
	return &Service{deps: deps}
}

// ParseFilter validates the common query parameters. Defaults: tz UTC, the
// last 30 days ending today in tz.
func (s *Service) ParseFilter(values url.Values) (Filter, error) {
	_ = values

	return Filter{}, errNotImplemented
}

func (s *Service) Domains(ctx context.Context) (DomainsResponse, error) {
	_ = ctx

	return DomainsResponse{}, errNotImplemented
}

func (s *Service) Overview(ctx context.Context, f Filter) (Overview, error) {
	_, _ = ctx, f

	return Overview{}, errNotImplemented
}

func (s *Service) Sources(ctx context.Context, f Filter) (SourcesResponse, error) {
	_, _ = ctx, f

	return SourcesResponse{}, errNotImplemented
}

func (s *Service) Source(ctx context.Context, key string, f Filter) (SourceDetail, error) {
	_, _, _ = ctx, key, f

	return SourceDetail{}, errNotImplemented
}

func (s *Service) Reports(ctx context.Context, f Filter) (ReportsResponse, error) {
	_, _ = ctx, f

	return ReportsResponse{}, errNotImplemented
}

func (s *Service) Report(ctx context.Context, id int64, f Filter) (ReportDetail, error) {
	_, _, _ = ctx, id, f

	return ReportDetail{}, errNotImplemented
}

// ReportRaw returns the extracted report document and a file name.
func (s *Service) ReportRaw(ctx context.Context, id int64) ([]byte, string, error) {
	_, _ = ctx, id

	return nil, "", errNotImplemented
}

// MessageRaw returns the original message bytes and a file name.
func (s *Service) MessageRaw(ctx context.Context, id int64) ([]byte, string, error) {
	_, _ = ctx, id

	return nil, "", errNotImplemented
}

func (s *Service) TLS(ctx context.Context, f Filter) (TLSResponse, error) {
	_, _ = ctx, f

	return TLSResponse{}, errNotImplemented
}

func (s *Service) TLSDomain(ctx context.Context, domain string, f Filter) (TLSDomainDetail, error) {
	_, _, _ = ctx, domain, f

	return TLSDomainDetail{}, errNotImplemented
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	_ = ctx

	return Status{}, errNotImplemented
}

// CollectMetrics refreshes the stored-data gauges (rua_messages,
// rua_records_messages, rua_ingest_stale, rua_enrich_queue_length).
func (s *Service) CollectMetrics(ctx context.Context, m *telemetry.Metrics) error {
	_, _ = ctx, m

	return errNotImplemented
}
