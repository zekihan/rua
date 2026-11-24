package query

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/sources"
)

// Domains returns every domain of original reports.
func (svc *Service) Domains(ctx context.Context) (DomainsResponse, error) {
	response := DomainsResponse{Domains: []Domain{}}

	err := svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		var rows []struct {
			Domain string `db:"domain"`
			First  int64  `db:"first_at"`
			Last   int64  `db:"last_at"`
			DMARC  int64  `db:"dmarc_reports"`
			TLS    int64  `db:"tls_reports"`
		}

		err := tx.SelectContext(ctx, &rows, `SELECT domain AS domain,
			MIN(begin) AS first_at, MAX(end) AS last_at,
			SUM(CASE WHEN kind = 'dmarc-aggregate' THEN 1 ELSE 0 END) AS dmarc_reports,
			SUM(CASE WHEN kind = 'tls-report' THEN 1 ELSE 0 END) AS tls_reports
			FROM reports WHERE duplicate_of IS NULL GROUP BY domain ORDER BY domain`)
		if err != nil {
			return fmt.Errorf("load domains: %w", err)
		}

		for index := range rows {
			response.Domains = append(response.Domains, Domain{
				Domain:        rows[index].Domain,
				FirstReportAt: rows[index].First,
				LastReportAt:  rows[index].Last,
				DMARCReports:  rows[index].DMARC,
				TLSReports:    rows[index].TLS,
			})
		}

		return nil
	})
	if err != nil {
		return DomainsResponse{Domains: []Domain{}}, fmt.Errorf("domains: %w", err)
	}

	return response, nil
}

type sourceAgg struct {
	dkimDomains map[string]struct{}
	ips         map[string]struct{}
	firstDate   string
	lastDate    string
	source      sources.Source
	messages    int64
	pass        int64
	forwarded   int64
	fail        int64
	spfAligned  int64
	dkimAligned int64
}

func newSourceAgg(source sources.Source) *sourceAgg {
	return &sourceAgg{
		source:      source,
		dkimDomains: make(map[string]struct{}),
		ips:         make(map[string]struct{}),
		firstDate:   "",
		lastDate:    "",
		messages:    0,
		pass:        0,
		forwarded:   0,
		fail:        0,
		spfAligned:  0,
		dkimAligned: 0,
	}
}

func (agg *sourceAgg) add(record *enrichedRecord) {
	agg.messages += record.count

	switch record.verdict {
	case VerdictPass:
		agg.pass += record.count
	case VerdictForwarded:
		agg.forwarded += record.count
	default:
		agg.fail += record.count
	}

	if record.spfEval == VerdictPass {
		agg.spfAligned += record.count
	}

	if record.dkimEval == VerdictPass {
		agg.dkimAligned += record.count
	}

	for _, domain := range dkimPassDomains(record.auth) {
		if domain != "" {
			agg.dkimDomains[normalizeDomain(domain)] = struct{}{}
		}
	}

	if record.sourceIP != "" {
		agg.ips[record.sourceIP] = struct{}{}
	}

	if agg.firstDate == "" || record.date < agg.firstDate {
		agg.firstDate = record.date
	}

	if agg.lastDate == "" || record.date > agg.lastDate {
		agg.lastDate = record.date
	}
}

func (agg *sourceAgg) row() SourceRow {
	domains := sortedKeys(agg.dkimDomains)

	return SourceRow{
		SPFAligned:  rate(agg.spfAligned, agg.messages),
		DKIMAligned: rate(agg.dkimAligned, agg.messages),
		Name:        agg.source.Name,
		Kind:        agg.source.Kind,
		LastSeen:    agg.lastDate,
		FirstSeen:   agg.firstDate,
		Key:         agg.source.Key,
		Rule:        agg.source.Rule,
		DKIMDomains: domains,
		Forwarded:   agg.forwarded,
		Fail:        agg.fail,
		Pass:        agg.pass,
		IPs:         len(agg.ips),
		Messages:    agg.messages,
		Expected:    agg.source.Expected,
	}
}

func aggregateSources(records []*enrichedRecord) map[string]*sourceAgg {
	aggregates := make(map[string]*sourceAgg)

	for _, record := range records {
		key := record.source.Key

		agg, ok := aggregates[key]
		if !ok {
			agg = newSourceAgg(record.source)
			aggregates[key] = agg
		}

		agg.add(record)
	}

	return aggregates
}

func sortSourceRows(rows []SourceRow) {
	slices.SortFunc(rows, func(first, second SourceRow) int {
		if first.Messages != second.Messages {
			if first.Messages > second.Messages {
				return -1
			}

			return 1
		}

		return strings.Compare(first.Key, second.Key)
	})
}

// Overview returns headline numbers and breakdowns.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) Overview(ctx context.Context, filter Filter) (Overview, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return Overview{}, fmt.Errorf("overview: %w", err)
	}

	overview := Overview{
		Range:          rng.rangeValue(),
		Days:           []DayVerdicts{},
		PolicyChanges:  []PolicyChange{},
		TopFailing:     []SourceVolume{},
		NewUnknown:     []NewSource{},
		NewExpected:    []NewSource{},
		QuietReporters: []QuietReporter{},
		LongWindows:    0,
		Totals: OverviewTotals{
			PassRate: nil, Messages: 0, Pass: 0, Forwarded: 0, Fail: 0,
			Quarantined: 0, Rejected: 0, Reports: 0,
		},
	}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		records, loadErr := svc.loadRecords(ctx, tx, rng, filter.Domain, true)
		if loadErr != nil {
			return loadErr
		}

		totals, days := overviewTotalsAndDays(records, rng)

		reportCount, longWindows, countErr := svc.countReports(ctx, tx, rng, filter.Domain)
		if countErr != nil {
			return countErr
		}

		totals.Reports = reportCount
		overview.Totals = totals
		overview.Days = days
		overview.LongWindows = longWindows

		aggregates := aggregateSources(records)
		overview.TopFailing = topFailing(aggregates)

		changes, changeErr := svc.policyChanges(ctx, tx, rng, filter.Domain)
		if changeErr != nil {
			return changeErr
		}

		overview.PolicyChanges = changes

		newSources, newErr := svc.newSources(ctx, tx, rng, filter.Domain, aggregates)
		if newErr != nil {
			return newErr
		}

		overview.NewUnknown = newSources.unknown
		overview.NewExpected = newSources.expected

		quiet, quietErr := svc.quietReporters(ctx, tx, rng, filter.Domain)
		if quietErr != nil {
			return quietErr
		}

		overview.QuietReporters = quiet

		return nil
	})
	if err != nil {
		return Overview{}, fmt.Errorf("overview: %w", err)
	}

	if overview.Days == nil {
		overview.Days = []DayVerdicts{}
	}

	return overview, nil
}

func overviewTotalsAndDays(
	records []*enrichedRecord,
	rng *resolvedRange,
) (OverviewTotals, []DayVerdicts) {
	dayMap := make(map[string]*DayVerdicts, len(rng.days))
	days := make([]DayVerdicts, 0, len(rng.days))

	for _, date := range rng.days {
		day := DayVerdicts{Date: date, Messages: 0, Pass: 0, Forwarded: 0, Fail: 0}
		days = append(days, day)
		dayMap[date] = &days[len(days)-1]
	}

	totals := OverviewTotals{
		PassRate: nil, Messages: 0, Pass: 0, Forwarded: 0, Fail: 0,
		Quarantined: 0, Rejected: 0, Reports: 0,
	}

	for _, record := range records {
		totals.Messages += record.count

		switch record.verdict {
		case VerdictPass:
			totals.Pass += record.count
		case VerdictForwarded:
			totals.Forwarded += record.count
		default:
			totals.Fail += record.count
		}

		if record.disposition == "quarantine" {
			totals.Quarantined += record.count
		}

		if record.disposition == "reject" {
			totals.Rejected += record.count
		}

		if day, ok := dayMap[record.date]; ok {
			day.Messages += record.count

			switch record.verdict {
			case VerdictPass:
				day.Pass += record.count
			case VerdictForwarded:
				day.Forwarded += record.count
			default:
				day.Fail += record.count
			}
		}
	}

	totals.PassRate = rate(totals.Pass, totals.Messages)

	return totals, days
}

func (svc *Service) countReports(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string,
) (int64, int64, error) {
	query := `SELECT COUNT(*) AS total,
		COALESCE(SUM(CASE WHEN (end - begin) > ? THEN 1 ELSE 0 END), 0) AS long_windows
		FROM reports
		WHERE duplicate_of IS NULL AND kind = 'dmarc-aggregate'
		AND ((begin + end) / 2) >= ? AND ((begin + end) / 2) < ?`
	args := []any{int64(longWindowSecs), rng.fromUnix, rng.toUnix}

	if domain != "" {
		query += domainClause

		args = append(args, domain)
	}

	var row struct {
		Total int64 `db:"total"`
		Long  int64 `db:"long_windows"`
	}

	err := tx.GetContext(ctx, &row, query, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("count reports: %w", err)
	}

	return row.Total, row.Long, nil
}

func topFailing(aggregates map[string]*sourceAgg) []SourceVolume {
	rows := []SourceVolume{}

	for _, agg := range aggregates {
		if agg.fail+agg.forwarded == 0 {
			continue
		}

		rows = append(rows, SourceVolume{
			Key:       agg.source.Key,
			Name:      agg.source.Name,
			Kind:      agg.source.Kind,
			Expected:  agg.source.Expected,
			Messages:  agg.messages,
			Pass:      agg.pass,
			Forwarded: agg.forwarded,
			Fail:      agg.fail,
		})
	}

	slices.SortFunc(rows, func(first, second SourceVolume) int {
		firstScore := first.Fail + first.Forwarded
		secondScore := second.Fail + second.Forwarded

		if firstScore != secondScore {
			if firstScore > secondScore {
				return -1
			}

			return 1
		}

		if first.Messages != second.Messages {
			if first.Messages > second.Messages {
				return -1
			}

			return 1
		}

		return strings.Compare(first.Key, second.Key)
	})

	if len(rows) > topFailingLimit {
		rows = rows[:topFailingLimit]
	}

	return rows
}

type policyDBRow struct {
	Pct       *int   `db:"pct"`
	Testing   string `db:"testing"`
	NP        string `db:"np"`
	ADKIM     string `db:"adkim"`
	ASPF      string `db:"aspf"`
	FO        string `db:"fo"`
	P         string `db:"p"`
	Discovery string `db:"discovery"`
	Domain    string `db:"domain"`
	Org       string `db:"org"`
	SP        string `db:"sp"`
	ReportID  int64  `db:"report_id"`
	Begin     int64  `db:"begin"`
	End       int64  `db:"end"`
}

func rowToPolicy(row *policyDBRow) Policy {
	return Policy{
		P:         row.P,
		SP:        row.SP,
		NP:        row.NP,
		Pct:       row.Pct,
		ADKIM:     row.ADKIM,
		ASPF:      row.ASPF,
		FO:        row.FO,
		Testing:   row.Testing,
		Discovery: row.Discovery,
	}
}

func policyFields(before, after *Policy) []string {
	fields := []string{}

	if before.P != after.P {
		fields = append(fields, "p")
	}

	if before.SP != after.SP {
		fields = append(fields, "sp")
	}

	if before.NP != after.NP {
		fields = append(fields, "np")
	}

	if !pctEqual(before.Pct, after.Pct) {
		fields = append(fields, "pct")
	}

	if before.ADKIM != after.ADKIM {
		fields = append(fields, "adkim")
	}

	if before.ASPF != after.ASPF {
		fields = append(fields, "aspf")
	}

	if before.Testing != after.Testing {
		fields = append(fields, "testing")
	}

	return fields
}

func pctEqual(first, second *int) bool {
	if first == nil || second == nil {
		return first == second
	}

	return *first == *second
}

func (svc *Service) policyChanges(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string,
) ([]PolicyChange, error) {
	query := `SELECT r.domain AS domain, r.org AS org, r.id AS report_id, r.begin AS begin, r.end AS end,
		COALESCE(p.p, '') AS p, COALESCE(p.sp, '') AS sp, COALESCE(p.np, '') AS np,
		p.pct AS pct, COALESCE(p.adkim, '') AS adkim, COALESCE(p.aspf, '') AS aspf,
		COALESCE(p.fo, '') AS fo, COALESCE(p.testing, '') AS testing,
		COALESCE(p.discovery, '') AS discovery
		FROM reports r LEFT JOIN policies p ON p.report = r.id
		WHERE r.duplicate_of IS NULL AND r.kind = 'dmarc-aggregate'`
	args := []any{}

	if domain != "" {
		query += rDomainClause

		args = append(args, domain)
	}

	query += ` ORDER BY r.domain, r.begin, r.id`

	var rows []policyDBRow

	err := tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load policies: %w", err)
	}

	changes := []PolicyChange{}
	current := make(map[string]*Policy)
	views := make(map[[2]string]*Policy)

	for index := range rows {
		row := &rows[index]
		changes = appendPolicyChange(changes, row, current, views, rng)
	}

	// Stable order by date then domain/report for display.
	slices.SortFunc(changes, func(first, second PolicyChange) int {
		if first.Date != second.Date {
			return strings.Compare(first.Date, second.Date)
		}

		if first.Domain != second.Domain {
			return strings.Compare(first.Domain, second.Domain)
		}

		if first.ReportID < second.ReportID {
			return -1
		}

		if first.ReportID > second.ReportID {
			return 1
		}

		return 0
	})

	return changes, nil
}

// appendPolicyChange tracks the policy each reporter sees per domain. A
// change is recorded when a reporter's view changes to a policy that is not
// already the domain's current one, so reporters that pick up a DNS change
// late, or that omit fields with defaults, do not produce markers.
func appendPolicyChange(
	changes []PolicyChange,
	row *policyDBRow,
	current map[string]*Policy,
	views map[[2]string]*Policy,
	rng *resolvedRange,
) []PolicyChange {
	raw := rowToPolicy(row)
	after := normalizePolicy(&raw)
	view := [2]string{row.Domain, row.Org}
	previousView, seenByReporter := views[view]
	views[view] = &after

	before, seen := current[row.Domain]

	switch {
	case !seen:
		current[row.Domain] = &after
	case !seenByReporter, len(policyFields(previousView, &after)) == 0,
		len(policyFields(before, &after)) == 0:
		return changes
	default:
		current[row.Domain] = &after
	}

	mid := midpoint(row.Begin, row.End)
	if mid < rng.fromUnix || mid >= rng.toUnix {
		return changes
	}

	change := PolicyChange{
		Before: nil, After: after, Domain: row.Domain,
		Date:     midpointDate(row.Begin, row.End, rng.loc),
		Fields:   []string{},
		ReportID: row.ReportID,
	}

	if seen {
		beforeCopy := *before
		change.Before = &beforeCopy
		change.Fields = policyFields(before, &after)
	}

	return append(changes, change)
}

// normalizePolicy fills the defaults a reporter may leave out, so the same
// published record compares equal whichever schema reported it.
func normalizePolicy(raw *Policy) Policy {
	policy := *raw
	policy.P = strings.ToLower(policy.P)
	policy.SP = strings.ToLower(policy.SP)
	policy.NP = strings.ToLower(policy.NP)

	if policy.SP == "" {
		policy.SP = policy.P
	}

	if policy.NP == "" {
		policy.NP = policy.SP
	}

	if policy.Pct == nil {
		full := fullPct
		policy.Pct = &full
	}

	policy.ADKIM = defaultString(strings.ToLower(policy.ADKIM), "r")
	policy.ASPF = defaultString(strings.ToLower(policy.ASPF), "r")
	policy.Testing = defaultString(strings.ToLower(policy.Testing), "n")

	return policy
}

const fullPct = 100

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

type newSourcesResult struct {
	unknown  []NewSource
	expected []NewSource
}

func (svc *Service) newSources(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string,
	inRange map[string]*sourceAgg,
) (newSourcesResult, error) {
	result := newSourcesResult{unknown: []NewSource{}, expected: []NewSource{}}

	if len(inRange) == 0 {
		return result, nil
	}

	allRecords, err := svc.loadRecords(ctx, tx, rng, domain, false)
	if err != nil {
		return result, err
	}

	firstMid := make(map[string]int64)
	firstDate := make(map[string]string)

	for _, record := range allRecords {
		key := record.source.Key
		mid := midpoint(record.begin, record.end)

		if current, ok := firstMid[key]; !ok || mid < current {
			firstMid[key] = mid
			firstDate[key] = record.date
		}
	}

	for key, agg := range inRange {
		mid, ok := firstMid[key]
		if !ok || mid < rng.fromUnix || mid >= rng.toUnix {
			continue
		}

		entry := NewSource{
			Key:       key,
			Name:      agg.source.Name,
			Kind:      agg.source.Kind,
			FirstSeen: firstDate[key],
			Messages:  agg.messages,
			Pass:      agg.pass,
			Forwarded: agg.forwarded,
			Fail:      agg.fail,
			Expected:  agg.source.Expected,
		}
		if agg.source.Expected {
			result.expected = append(result.expected, entry)
		} else {
			result.unknown = append(result.unknown, entry)
		}
	}

	sortNewSources(result.unknown)
	sortNewSources(result.expected)

	return result, nil
}

func sortNewSources(rows []NewSource) {
	slices.SortFunc(rows, func(first, second NewSource) int {
		if first.FirstSeen != second.FirstSeen {
			return strings.Compare(first.FirstSeen, second.FirstSeen)
		}

		return strings.Compare(first.Key, second.Key)
	})
}

func (svc *Service) quietReporters(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string,
) ([]QuietReporter, error) {
	threshold := svc.quietThreshold(rng)

	query := `SELECT org AS org, MIN(end) AS first_end, MAX(end) AS last_end FROM reports
		WHERE duplicate_of IS NULL AND kind = 'dmarc-aggregate'`
	args := []any{}

	if domain != "" {
		query += domainClause

		args = append(args, domain)
	}

	query += ` GROUP BY org`

	var rows []struct {
		Org      string `db:"org"`
		FirstEnd int64  `db:"first_end"`
		LastEnd  int64  `db:"last_end"`
	}

	err := tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load quiet reporters: %w", err)
	}

	quiet := []QuietReporter{}

	for index := range rows {
		row := &rows[index]
		if !quietByRule(row.FirstEnd, row.LastEnd, rng.fromUnix, threshold) {
			continue
		}

		domains, err := svc.reporterDomains(ctx, tx, row.Org, domain)
		if err != nil {
			return nil, err
		}

		quiet = append(quiet, QuietReporter{
			Org:          row.Org,
			Domains:      domains,
			LastReportAt: row.LastEnd,
		})
	}

	slices.SortFunc(quiet, func(first, second QuietReporter) int {
		if first.LastReportAt != second.LastReportAt {
			if first.LastReportAt > second.LastReportAt {
				return -1
			}

			return 1
		}

		return strings.Compare(first.Org, second.Org)
	})

	return quiet, nil
}

func (svc *Service) reporterDomains(
	ctx context.Context, tx *sqlx.Tx, org, domain string,
) ([]string, error) {
	query := `SELECT DISTINCT domain FROM reports
		WHERE duplicate_of IS NULL AND kind = 'dmarc-aggregate' AND org = ?`
	args := []any{org}

	if domain != "" {
		query += domainClause

		args = append(args, domain)
	}

	query += ` ORDER BY domain`

	var domains []string

	err := tx.SelectContext(ctx, &domains, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load reporter domains: %w", err)
	}

	if domains == nil {
		domains = []string{}
	}

	return domains, nil
}

// Sources returns the source table for the range.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) Sources(ctx context.Context, filter Filter) (SourcesResponse, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return SourcesResponse{}, fmt.Errorf("sources: %w", err)
	}

	response := SourcesResponse{Range: rng.rangeValue(), Sources: []SourceRow{}}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		records, loadErr := svc.loadRecords(ctx, tx, rng, filter.Domain, true)
		if loadErr != nil {
			return loadErr
		}

		aggregates := aggregateSources(records)
		rows := make([]SourceRow, 0, len(aggregates))

		for _, agg := range aggregates {
			row := agg.row()
			if filter.UnknownOnly && row.Expected {
				continue
			}

			rows = append(rows, row)
		}

		sortSourceRows(rows)
		response.Sources = rows

		return nil
	})
	if err != nil {
		return SourcesResponse{}, fmt.Errorf("sources: %w", err)
	}

	return response, nil
}

// Source returns detail for one source key.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) Source(
	ctx context.Context, key string, filter Filter,
) (SourceDetail, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return SourceDetail{}, fmt.Errorf("source: %w", err)
	}

	detail := SourceDetail{
		Range:          rng.rangeValue(),
		IPs:            []IPRow{},
		Days:           []DayVerdicts{},
		Combinations:   []Combination{},
		EnvelopeFrom:   []Count{},
		HeaderFrom:     []Count{},
		DKIMSignatures: []Count{},
		Reporters:      []Count{},
		Source: SourceRow{
			SPFAligned: nil, DKIMAligned: nil, Name: "", Kind: "", LastSeen: "", FirstSeen: "",
			Key: "", Rule: "", DKIMDomains: []string{}, Forwarded: 0, Fail: 0, Pass: 0,
			IPs: 0, Messages: 0, Expected: false,
		},
	}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		records, loadErr := svc.loadRecords(ctx, tx, rng, filter.Domain, true)
		if loadErr != nil {
			return loadErr
		}

		var matched []*enrichedRecord

		for _, record := range records {
			if record.source.Key == key {
				matched = append(matched, record)
			}
		}

		if len(matched) == 0 {
			return fmt.Errorf("%w: source %q", ErrNotFound, key)
		}

		aggregates := aggregateSources(matched)
		agg := aggregates[key]
		detail.Source = agg.row()
		detail.IPs = buildIPRows(matched)
		detail.Days = buildDays(rng, matched)
		detail.Combinations = buildCombinations(matched)
		detail.EnvelopeFrom = buildCounts(matched, func(record *enrichedRecord) string {
			return record.envFrom
		})
		detail.HeaderFrom = buildCounts(matched, func(record *enrichedRecord) string {
			return record.headerFrom
		})
		detail.DKIMSignatures = buildDKIMSignatures(matched)
		detail.Reporters = buildCounts(matched, func(record *enrichedRecord) string {
			return record.org
		})

		return nil
	})
	if err != nil {
		return SourceDetail{}, fmt.Errorf("source: %w", err)
	}

	return detail, nil
}

func buildDays(rng *resolvedRange, records []*enrichedRecord) []DayVerdicts {
	dayMap := make(map[string]*DayVerdicts)
	days := make([]DayVerdicts, 0, len(rng.days))

	for _, date := range rng.days {
		day := DayVerdicts{Date: date, Messages: 0, Pass: 0, Forwarded: 0, Fail: 0}
		days = append(days, day)
		dayMap[date] = &days[len(days)-1]
	}

	for _, record := range records {
		day, ok := dayMap[record.date]
		if !ok {
			continue
		}

		day.Messages += record.count

		switch record.verdict {
		case VerdictPass:
			day.Pass += record.count
		case VerdictForwarded:
			day.Forwarded += record.count
		default:
			day.Fail += record.count
		}
	}

	return days
}

func buildIPRows(records []*enrichedRecord) []IPRow {
	type ipAgg struct {
		row       IPRow
		messages  int64
		pass      int64
		forwarded int64
		fail      int64
	}

	byIP := make(map[string]*ipAgg)

	for _, record := range records {
		agg, ok := byIP[record.sourceIP]
		if !ok {
			agg = &ipAgg{messages: 0, pass: 0, forwarded: 0, fail: 0, row: IPRow{
				IP:           record.sourceIP,
				PTR:          record.ptr,
				ASOrg:        record.asOrg,
				ASN:          record.asn,
				PTRConfirmed: record.ptrConf,
				Messages:     0,
				Pass:         0,
				Forwarded:    0,
				Fail:         0,
			}}
			byIP[record.sourceIP] = agg
		}

		agg.messages += record.count

		switch record.verdict {
		case VerdictPass:
			agg.pass += record.count
		case VerdictForwarded:
			agg.forwarded += record.count
		default:
			agg.fail += record.count
		}
	}

	rows := make([]IPRow, 0, len(byIP))

	for _, agg := range byIP {
		row := agg.row
		row.Messages = agg.messages
		row.Pass = agg.pass
		row.Forwarded = agg.forwarded
		row.Fail = agg.fail
		rows = append(rows, row)
	}

	slices.SortFunc(rows, func(first, second IPRow) int {
		if first.Messages != second.Messages {
			if first.Messages > second.Messages {
				return -1
			}

			return 1
		}

		return strings.Compare(first.IP, second.IP)
	})

	return rows
}

func buildCounts(
	records []*enrichedRecord, valueOf func(*enrichedRecord) string,
) []Count {
	totals := make(map[string]int64)

	for _, record := range records {
		totals[valueOf(record)] += record.count
	}

	counts := make([]Count, 0, len(totals))

	for value, messages := range totals {
		counts = append(counts, Count{Value: value, Messages: messages})
	}

	slices.SortFunc(counts, func(first, second Count) int {
		if first.Messages != second.Messages {
			if first.Messages > second.Messages {
				return -1
			}

			return 1
		}

		return strings.Compare(first.Value, second.Value)
	})

	return counts
}

func buildDKIMSignatures(records []*enrichedRecord) []Count {
	totals := make(map[string]int64)

	for _, record := range records {
		seen := make(map[string]struct{})

		for _, auth := range record.auth {
			if auth.Method != "dkim" {
				continue
			}

			value := auth.Domain
			if auth.Selector != "" {
				if auth.Domain != "" {
					value = auth.Selector + "._domainkey." + auth.Domain
				} else {
					value = auth.Selector
				}
			}

			if _, duplicate := seen[value]; duplicate {
				continue
			}

			seen[value] = struct{}{}
			totals[value] += record.count
		}
	}

	counts := make([]Count, 0, len(totals))

	for value, messages := range totals {
		counts = append(counts, Count{Value: value, Messages: messages})
	}

	slices.SortFunc(counts, func(first, second Count) int {
		if first.Messages != second.Messages {
			if first.Messages > second.Messages {
				return -1
			}

			return 1
		}

		return strings.Compare(first.Value, second.Value)
	})

	return counts
}

func buildCombinations(records []*enrichedRecord) []Combination {
	type comboKey struct {
		spf         string
		dkim        string
		disposition string
		verdict     string
		spfAligned  bool
		dkimAligned bool
	}

	totals := make(map[comboKey]int64)

	for _, record := range records {
		key := comboKey{
			spf:         bestAuthResult(record.auth, "spf"),
			dkim:        bestAuthResult(record.auth, "dkim"),
			disposition: record.disposition,
			verdict:     record.verdict,
			spfAligned:  record.spfEval == VerdictPass,
			dkimAligned: record.dkimEval == VerdictPass,
		}
		totals[key] += record.count
	}

	combos := make([]Combination, 0, len(totals))

	for key, messages := range totals {
		combos = append(combos, Combination{
			SPF:         key.spf,
			DKIM:        key.dkim,
			Disposition: key.disposition,
			Verdict:     key.verdict,
			Messages:    messages,
			SPFAligned:  key.spfAligned,
			DKIMAligned: key.dkimAligned,
		})
	}

	slices.SortFunc(combos, func(first, second Combination) int {
		if first.Messages != second.Messages {
			if first.Messages > second.Messages {
				return -1
			}

			return 1
		}

		if first.SPF != second.SPF {
			return strings.Compare(first.SPF, second.SPF)
		}

		if first.DKIM != second.DKIM {
			return strings.Compare(first.DKIM, second.DKIM)
		}

		if first.Disposition != second.Disposition {
			return strings.Compare(first.Disposition, second.Disposition)
		}

		return strings.Compare(first.Verdict, second.Verdict)
	})

	return combos
}

func bestAuthResult(auth []AuthResult, method string) string {
	best := ""
	bestRank := -1

	for _, result := range auth {
		if result.Method != method {
			continue
		}

		rank := authRank(result.Result)
		if rank > bestRank || (rank == bestRank && (best == "" || result.Result < best)) {
			best = result.Result
			bestRank = rank
		}
	}

	return best
}

func authRank(result string) int {
	switch strings.ToLower(result) {
	case VerdictPass:
		return rankPass
	case "fail":
		return rankFail
	case "softfail":
		return rankSoftFail
	case "neutral":
		return rankNeutral
	case "none":
		return rankNone
	case "temperror", "permerror":
		return rankError
	default:
		return 0
	}
}
