// Package query computes every /api view from individual records. Nothing
// is pre-aggregated: SQL selects rows by unix time and Go buckets them into
// days of the requested time zone (DESIGN.md section 7).
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/net/publicsuffix"

	"github.com/zekihan/rua/internal/imapsrc"
	"github.com/zekihan/rua/internal/sources"
	"github.com/zekihan/rua/internal/store"
)

const (
	dateLayout       = time.DateOnly
	defaultRangeDays = 30
	defaultLimit     = 50
	maxLimit         = 200
	longWindowSecs   = 48 * 3600
	daySeconds       = 24 * 3600
	quietDays        = 7
	topFailingLimit  = 10
	cursorParts      = 2
	midpointDivisor  = 2
	dayStartWindow   = 48 * time.Hour
	dayStartStep     = 24 * time.Hour
	kindAggregate    = "dmarc-aggregate"
	kindTLS          = "tls-report"
	domainClause     = " AND domain = ?"
	rDomainClause    = " AND r.domain = ?"
	rankPass         = 100
	rankFail         = 50
	rankSoftFail     = 40
	rankNeutral      = 30
	rankNone         = 20
	rankError        = 10
)

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
	Source      string
	Cursor      string
	Limit       int
	UnknownOnly bool
	// FromUnix and ToUnix are the resolved bounds [FromUnix, ToUnix) in
	// unix seconds, set by ParseFilter.
	FromUnix int64
	ToUnix   int64
}

// Service answers the /api queries.
// The maxRangeDays limit bounds the span of an aggregating request (ten years).
const maxRangeDays = 3660

type Service struct {
	// Label sets written by the last CollectMetrics, so series that
	// disappeared are reset to zero.
	gauges   map[gaugeKey]bool
	deps     Deps
	gaugesMu sync.Mutex
}

type gaugeKey struct {
	metric string
	first  string
	second string
}

// New returns a Service.
func New(deps Deps) *Service {
	return &Service{deps: deps, gauges: map[gaugeKey]bool{}, gaugesMu: sync.Mutex{}}
}

func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, args...))
}

// ParseFilter validates the common query parameters. Defaults: tz UTC, the
// last 30 days ending today in tz.
func (svc *Service) ParseFilter(values url.Values) (Filter, error) {
	tzName := values.Get("tz")
	if tzName == "" {
		tzName = "UTC"
	}

	if tzName == "Local" {
		return Filter{}, badRequest("tz %q is not allowed", tzName)
	}

	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return Filter{}, badRequest("invalid tz %q", tzName)
	}

	toStr := values.Get("to")
	if toStr == "" {
		toStr = civilFromTime(svc.now(), loc).String()
	}

	toDate, err := parseCivilDate(toStr)
	if err != nil {
		return Filter{}, badRequest("invalid to date %q", toStr)
	}

	fromStr := values.Get("from")
	if fromStr == "" {
		fromStr = toDate.addDays(-(defaultRangeDays - 1)).String()
	}

	fromDate, err := parseCivilDate(fromStr)
	if err != nil {
		return Filter{}, badRequest("invalid from date %q", fromStr)
	}

	if fromDate.after(toDate) {
		return Filter{}, badRequest("from %q is after to %q", fromStr, toStr)
	}

	if toDate.ordinal()-fromDate.ordinal() > maxRangeDays {
		return Filter{}, badRequest("range longer than %d days", maxRangeDays)
	}

	kind := values.Get("kind")
	if kind != "" && kind != kindAggregate && kind != kindTLS {
		return Filter{}, badRequest("invalid kind %q", kind)
	}

	limit, err := parseLimit(values.Get("limit"))
	if err != nil {
		return Filter{}, err
	}

	filter := Filter{
		TZ:          loc,
		Domain:      strings.ToLower(strings.TrimSpace(values.Get("domain"))),
		From:        fromStr,
		To:          toStr,
		Reporter:    values.Get("reporter"),
		Kind:        kind,
		Source:      values.Get("source"),
		Cursor:      values.Get("cursor"),
		Limit:       limit,
		UnknownOnly: values.Get("unknown") == "1",
		FromUnix:    fromDate.dayStart(loc),
		ToUnix:      toDate.addDays(1).dayStart(loc),
	}

	return filter, nil
}

func (svc *Service) now() time.Time {
	if svc.deps.Now != nil {
		return svc.deps.Now()
	}

	return time.Now()
}

func (svc *Service) sourceSet() *sources.Set {
	if svc.deps.Sources == nil {
		return sources.Empty()
	}

	set := svc.deps.Sources()
	if set == nil {
		return sources.Empty()
	}

	return set
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, badRequest("invalid limit %q", raw)
	}

	if limit > maxLimit {
		limit = maxLimit
	}

	return limit, nil
}

// A civilDate is a calendar date (year, month, day) with no clock time
// and no zone. Day buckets are civil dates: iterating them directly
// keeps a day whose local midnight does not exist (a DST transition at
// 00:00) from shifting into its neighbor.
type civilDate struct {
	year  int
	month time.Month
	day   int
}

func parseCivilDate(value string) (civilDate, error) {
	parsed, err := time.Parse(dateLayout, value)
	if err != nil {
		return civilDate{year: 0, month: 0, day: 0}, fmt.Errorf("parse date: %w", err)
	}

	if parsed.Format(dateLayout) != value {
		return civilDate{year: 0, month: 0, day: 0}, fmt.Errorf("parse date: %w", errBadDate)
	}

	return civilDate{year: parsed.Year(), month: parsed.Month(), day: parsed.Day()}, nil
}

var errBadDate = errors.New("invalid date")

func civilFromTime(instant time.Time, loc *time.Location) civilDate {
	local := instant.In(loc)

	return civilDate{year: local.Year(), month: local.Month(), day: local.Day()}
}

func (date civilDate) String() string {
	return time.Date(date.year, date.month, date.day, 0, 0, 0, 0, time.UTC).Format(dateLayout)
}

func (date civilDate) addDays(count int) civilDate {
	shifted := time.Date(date.year, date.month, date.day, 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, count)

	return civilDate{year: shifted.Year(), month: shifted.Month(), day: shifted.Day()}
}

func (date civilDate) ordinal() int64 {
	return time.Date(date.year, date.month, date.day, 0, 0, 0, 0, time.UTC).Unix() / daySeconds
}

func (date civilDate) after(other civilDate) bool {
	return date.ordinal() > other.ordinal()
}

// dayStart returns the unix time of the first instant of the civil date
// in loc. When 00:00 does not exist (DST starts at midnight) time.Date
// lands on a neighboring day, so search around it for the first
// instant whose civil date is this one; a wholly skipped date resolves
// to the start of the next existing date, leaving an empty bucket.
func (date civilDate) dayStart(loc *time.Location) int64 {
	base := time.Date(date.year, date.month, date.day, 0, 0, 0, 0, loc)
	if civilFromTime(base, loc) == date {
		return base.Unix()
	}

	target := date.ordinal()
	low := base.Add(-dayStartWindow)
	high := base.Add(dayStartWindow)

	for civilFromTime(low, loc).ordinal() >= target {
		low = low.Add(-dayStartStep)
	}

	for civilFromTime(high, loc).ordinal() < target {
		high = high.Add(dayStartStep)
	}

	for high.Unix()-low.Unix() > 1 {
		middle := low.Add(high.Sub(low) / midpointDivisor)
		if civilFromTime(middle, loc).ordinal() >= target {
			high = middle
		} else {
			low = middle
		}
	}

	return high.Unix()
}

// resolvedRange is a Filter with defaults applied.
type resolvedRange struct {
	loc      *time.Location
	fromStr  string
	toStr    string
	days     []string
	fromUnix int64
	toUnix   int64
}

func (svc *Service) resolveRange(filter *Filter) (*resolvedRange, error) {
	loc := filter.TZ
	if loc == nil {
		loc = time.UTC
	}

	toStr := filter.To
	if toStr == "" {
		toStr = civilFromTime(svc.now(), loc).String()
	}

	toDate, err := parseCivilDate(toStr)
	if err != nil {
		return nil, badRequest("invalid to date %q", toStr)
	}

	fromStr := filter.From
	if fromStr == "" {
		fromStr = toDate.addDays(-(defaultRangeDays - 1)).String()
	}

	fromDate, err := parseCivilDate(fromStr)
	if err != nil {
		return nil, badRequest("invalid from date %q", fromStr)
	}

	if fromDate.after(toDate) {
		return nil, badRequest("from %q is after to %q", fromStr, toStr)
	}

	fromUnix := filter.FromUnix
	toUnix := filter.ToUnix

	if fromUnix == 0 && toUnix == 0 {
		fromUnix = fromDate.dayStart(loc)
		toUnix = toDate.addDays(1).dayStart(loc)
	}

	days := make([]string, 0, toDate.ordinal()-fromDate.ordinal()+1)

	for day := fromDate; !day.after(toDate); day = day.addDays(1) {
		days = append(days, day.String())
	}

	return &resolvedRange{
		loc:      loc,
		fromStr:  fromStr,
		toStr:    toStr,
		fromUnix: fromUnix,
		toUnix:   toUnix,
		days:     days,
	}, nil
}

func (rng *resolvedRange) rangeValue() Range {
	return Range{From: rng.fromStr, To: rng.toStr, TZ: rng.loc.String()}
}

func midpoint(begin, end int64) int64 {
	return (begin + end) / midpointDivisor
}

func midpointDate(begin, end int64, loc *time.Location) string {
	return time.Unix(midpoint(begin, end), 0).In(loc).Format(dateLayout)
}

func isLongWindow(begin, end int64) bool {
	return end-begin > longWindowSecs
}

// recordVerdict is the pure verdict rule of DESIGN section 7.
func recordVerdict(
	evaluatedDKIM, evaluatedSPF, headerFrom, adkimMode string,
	dkimPassDomains []string, reasons []Reason,
) string {
	if evaluatedDKIM == VerdictPass || evaluatedSPF == VerdictPass {
		return VerdictPass
	}

	for _, reason := range reasons {
		if reason.Type == "forwarded" || reason.Type == "mailing_list" {
			return VerdictForwarded
		}
	}

	header := normalizeDomain(headerFrom)
	if header == "" {
		return VerdictFail
	}

	relaxed := !strings.EqualFold(strings.TrimSpace(adkimMode), "s")

	for _, domain := range dkimPassDomains {
		candidate := normalizeDomain(domain)
		if candidate == "" {
			continue
		}

		if candidate == header {
			return VerdictForwarded
		}

		if relaxed && sameOrgDomain(candidate, header) {
			return VerdictForwarded
		}
	}

	return VerdictFail
}

func normalizeDomain(value string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(value), "."))
}

func orgDomain(host string) (string, bool) {
	host = normalizeDomain(host)
	if host == "" {
		return "", false
	}

	base, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", false
	}

	return base, true
}

func sameOrgDomain(first, second string) bool {
	firstBase, firstOK := orgDomain(first)
	secondBase, secondOK := orgDomain(second)

	return firstOK && secondOK && firstBase == secondBase
}

func rate(numerator, denominator int64) *float64 {
	if denominator == 0 {
		return nil
	}

	value := float64(numerator) / float64(denominator)

	return &value
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// enrichedRecord is one stored record with its verdict and source.
type enrichedRecord struct {
	envFrom     string
	adkim       string
	asOrg       string
	ptr         string
	date        string
	envTo       string
	org         string
	headerFrom  string
	domain      string
	verdict     string
	dkimEval    string
	disposition string
	spfEval     string
	sourceIP    string
	source      sources.Source
	auth        []AuthResult
	reasons     []Reason
	end         int64
	count       int64
	begin       int64
	recordID    int64
	reportID    int64
	asn         uint32
	ptrConf     bool
}

type recordDBRow struct {
	Reasons     string `db:"reasons"`
	Domain      string `db:"domain"`
	Org         string `db:"org"`
	ADKIM       string `db:"adkim"`
	SourceIP    string `db:"source_ip"`
	Disposition string `db:"disposition"`
	DKIMEval    string `db:"dkim"`
	SPFEval     string `db:"spf"`
	HeaderFrom  string `db:"header_from"`
	EnvFrom     string `db:"envelope_from"`
	EnvTo       string `db:"envelope_to"`
	PTR         string `db:"ptr"`
	ASOrg       string `db:"as_org"`
	ReportID    int64  `db:"report_id"`
	RecordID    int64  `db:"record_id"`
	Begin       int64  `db:"begin"`
	End         int64  `db:"end"`
	Count       int64  `db:"count"`
	ASN         int64  `db:"asn"`
	PTRConf     int64  `db:"ptr_confirmed"`
}

type authDBRow struct {
	Method   string `db:"method"`
	Domain   string `db:"domain"`
	Selector string `db:"selector"`
	Scope    string `db:"scope"`
	Result   string `db:"result"`
	RecordID int64  `db:"record_id"`
}

// loadRecords loads original DMARC records. When bounded is true only
// reports whose midpoint is in rng are returned.
func (svc *Service) loadRecords(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string, bounded bool,
) ([]*enrichedRecord, error) {
	query := `SELECT r.id AS report_id, r.begin AS begin, r.end AS end,
		r.domain AS domain, r.org AS org, COALESCE(p.adkim, '') AS adkim,
		rec.id AS record_id, rec.source_ip AS source_ip, rec.count AS count,
		rec.disposition AS disposition, rec.dkim AS dkim, rec.spf AS spf,
		rec.reasons AS reasons, rec.header_from AS header_from,
		rec.envelope_from AS envelope_from, rec.envelope_to AS envelope_to,
		COALESCE(ii.ptr, '') AS ptr, COALESCE(ii.ptr_confirmed, 0) AS ptr_confirmed,
		COALESCE(ii.asn, 0) AS asn, COALESCE(ii.as_org, '') AS as_org
		FROM records rec
		JOIN reports r ON r.id = rec.report
		LEFT JOIN policies p ON p.report = r.id
		LEFT JOIN ip_info ii ON ii.ip = rec.source_ip
		WHERE r.duplicate_of IS NULL AND r.kind = 'dmarc-aggregate'`

	args := []any{}

	if bounded {
		query += ` AND ((r.begin + r.end) / 2) >= ? AND ((r.begin + r.end) / 2) < ?`

		args = append(args, rng.fromUnix, rng.toUnix)
	}

	if domain != "" {
		query += rDomainClause

		args = append(args, domain)
	}

	query += ` ORDER BY r.begin, r.id, rec.id`

	var rows []recordDBRow

	err := tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load records: %w", err)
	}

	authByRecord, err := svc.loadAuth(ctx, tx, rng, domain, bounded)
	if err != nil {
		return nil, err
	}

	set := svc.sourceSet()

	return enrichRows(rows, authByRecord, set, rng.loc), nil
}

func enrichRows(
	rows []recordDBRow, authByRecord map[int64][]AuthResult, set *sources.Set, loc *time.Location,
) []*enrichedRecord {
	records := make([]*enrichedRecord, 0, len(rows))

	for index := range rows {
		row := &rows[index]

		auth := authByRecord[row.RecordID]
		if auth == nil {
			auth = []AuthResult{}
		}

		reasons := parseReasons(row.Reasons)
		passDomains := dkimPassDomains(auth)

		asn := uint32(0)
		if row.ASN > 0 {
			asn = uint32(row.ASN) //nolint:gosec // ASN from DB is non-negative.
		}

		var addr netip.Addr

		parsedAddr, parseErr := netip.ParseAddr(strings.TrimSpace(row.SourceIP))
		if parseErr == nil {
			addr = parsedAddr
		}

		source := set.Classify(sources.Input{
			IP:           addr,
			PTR:          row.PTR,
			PTRConfirmed: row.PTRConf != 0,
			ASN:          asn,
			ASOrg:        row.ASOrg,
			DKIMDomains:  passDomains,
		})
		records = append(records, &enrichedRecord{
			source:   source,
			auth:     auth,
			reasons:  reasons,
			reportID: row.ReportID,
			recordID: row.RecordID,
			begin:    row.Begin,
			end:      row.End,
			count:    row.Count,
			asn:      asn,
			verdict: recordVerdict(
				row.DKIMEval,
				row.SPFEval,
				row.HeaderFrom,
				row.ADKIM,
				passDomains,
				reasons,
			),
			domain:      row.Domain,
			org:         row.Org,
			adkim:       row.ADKIM,
			sourceIP:    row.SourceIP,
			disposition: row.Disposition,
			dkimEval:    row.DKIMEval,
			spfEval:     row.SPFEval,
			headerFrom:  row.HeaderFrom,
			envFrom:     row.EnvFrom,
			envTo:       row.EnvTo,
			date:        midpointDate(row.Begin, row.End, loc),
			ptr:         row.PTR,
			asOrg:       row.ASOrg,
			ptrConf:     row.PTRConf != 0,
		})
	}

	return records
}

func (svc *Service) loadAuth(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string, bounded bool,
) (map[int64][]AuthResult, error) {
	query := `SELECT ar.record AS record_id, ar.method AS method,
		ar.domain AS domain, ar.selector AS selector, ar.scope AS scope,
		ar.result AS result
		FROM auth_results ar
		JOIN records rec ON rec.id = ar.record
		JOIN reports r ON r.id = rec.report
		WHERE r.duplicate_of IS NULL AND r.kind = 'dmarc-aggregate'`

	args := []any{}

	if bounded {
		query += ` AND ((r.begin + r.end) / 2) >= ? AND ((r.begin + r.end) / 2) < ?`

		args = append(args, rng.fromUnix, rng.toUnix)
	}

	if domain != "" {
		query += rDomainClause

		args = append(args, domain)
	}

	query += ` ORDER BY ar.record, ar.method, ar.domain, ar.selector`

	var rows []authDBRow

	err := tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load auth results: %w", err)
	}

	result := make(map[int64][]AuthResult)

	for index := range rows {
		row := &rows[index]
		result[row.RecordID] = append(result[row.RecordID], AuthResult{
			Method:   row.Method,
			Domain:   row.Domain,
			Selector: row.Selector,
			Scope:    row.Scope,
			Result:   row.Result,
		})
	}

	return result, nil
}

func parseReasons(raw string) []Reason {
	if strings.TrimSpace(raw) == "" {
		return []Reason{}
	}

	var reasons []Reason

	err := json.Unmarshal([]byte(raw), &reasons)
	if err != nil {
		return []Reason{}
	}

	if reasons == nil {
		return []Reason{}
	}

	return reasons
}

func dkimPassDomains(auth []AuthResult) []string {
	domains := []string{}

	for _, result := range auth {
		if result.Method == "dkim" && result.Result == VerdictPass {
			domains = append(domains, result.Domain)
		}
	}

	return domains
}

func parseJSONStringArray(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}

	var values []string

	err := json.Unmarshal([]byte(raw), &values)
	if err != nil || values == nil {
		return []string{}
	}

	return values
}
