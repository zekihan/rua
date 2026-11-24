package query

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/store"
)

type reportDBRow struct {
	Kind      string        `db:"kind"`
	Org       string        `db:"org"`
	Email     string        `db:"email"`
	Extra     string        `db:"extra_contact"`
	ReportID  string        `db:"report_id"`
	Domain    string        `db:"domain"`
	Schema    string        `db:"schema"`
	Blob      []byte        `db:"blob"`
	Duplicate sql.NullInt64 `db:"duplicate_of"`
	ID        int64         `db:"id"`
	Message   int64         `db:"message"`
	Begin     int64         `db:"begin"`
	End       int64         `db:"end"`
	Conflict  int64         `db:"conflict"`
}

func (row *reportDBRow) summary(
	rng *resolvedRange, messages int64, passRate *float64, tlsFailed int64,
) ReportSummary {
	var duplicateOf *int64

	if row.Duplicate.Valid {
		value := row.Duplicate.Int64
		duplicateOf = &value
	}

	return ReportSummary{
		PassRate:    passRate,
		DuplicateOf: duplicateOf,
		Domain:      row.Domain,
		ReportID:    row.ReportID,
		Date:        midpointDate(row.Begin, row.End, rng.loc),
		Org:         row.Org,
		Kind:        row.Kind,
		ID:          row.ID,
		Begin:       row.Begin,
		End:         row.End,
		Messages:    messages,
		TLSFailed:   tlsFailed,
		Duplicate:   row.Duplicate.Valid,
		Conflict:    row.Conflict != 0,
		LongWindow:  isLongWindow(row.Begin, row.End),
	}
}

// reportStats computes messages/passRate/tlsFailed for one report row.
func (svc *Service) reportStats(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, row *reportDBRow,
) (int64, *float64, int64, error) {
	if row.Duplicate.Valid {
		return 0, nil, 0, nil
	}

	if row.Kind == kindTLS {
		var stats struct {
			Success int64 `db:"success"`
			Failure int64 `db:"failure"`
		}

		err := tx.GetContext(ctx, &stats, `SELECT COALESCE(SUM(success), 0) AS success,
			COALESCE(SUM(failure), 0) AS failure FROM tls_policies WHERE report = ?`, row.ID)
		if err != nil {
			return 0, nil, 0, fmt.Errorf("load TLS report stats: %w", err)
		}

		return stats.Success + stats.Failure, nil, stats.Failure, nil
	}

	// DMARC: verdict per record of this report.
	var rows []struct {
		DKIM     string `db:"dkim"`
		SPF      string `db:"spf"`
		Header   string `db:"header_from"`
		Reasons  string `db:"reasons"`
		ADKIM    string `db:"adkim"`
		RecordID int64  `db:"record_id"`
		Count    int64  `db:"count"`
	}

	err := tx.SelectContext(ctx, &rows, `SELECT rec.id AS record_id, rec.count AS count,
		rec.dkim AS dkim, rec.spf AS spf, rec.header_from AS header_from,
		rec.reasons AS reasons, COALESCE(p.adkim, '') AS adkim
		FROM records rec
		LEFT JOIN policies p ON p.report = rec.report
		WHERE rec.report = ? ORDER BY rec.id`, row.ID)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("load report records stats: %w", err)
	}

	if len(rows) == 0 {
		return 0, nil, 0, nil
	}

	authByRecord, err := svc.authForReport(ctx, tx, row.ID)
	if err != nil {
		return 0, nil, 0, err
	}

	_ = rng

	var messages, pass int64

	for index := range rows {
		auth := authByRecord[rows[index].RecordID]
		verdict := recordVerdict(
			rows[index].DKIM, rows[index].SPF, rows[index].Header, rows[index].ADKIM,
			dkimPassDomains(auth), parseReasons(rows[index].Reasons),
		)
		messages += rows[index].Count

		if verdict == VerdictPass {
			pass += rows[index].Count
		}
	}

	return messages, rate(pass, messages), 0, nil
}

// authForReport loads auth results for every record of one report. The
// report-scoped subquery binds a single parameter however many records
// the report has, unlike an IN list over record ids.
func (svc *Service) authForReport(
	ctx context.Context, tx *sqlx.Tx, reportID int64,
) (map[int64][]AuthResult, error) {
	result := make(map[int64][]AuthResult)

	var rows []authDBRow

	err := tx.SelectContext(
		ctx,
		&rows,
		`SELECT record AS record_id, method, domain, selector, scope, result
		FROM auth_results
		WHERE record IN (SELECT id FROM records WHERE report = ?)
		ORDER BY record, method, domain`,
		reportID,
	)
	if err != nil {
		return nil, fmt.Errorf("load auth results: %w", err)
	}

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

func encodeCursor(begin, id int64) string {
	raw := strconv.FormatInt(begin, 10) + "," + strconv.FormatInt(id, 10)

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (int64, int64, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			return 0, 0, badRequest("invalid cursor")
		}
	}

	parts := strings.Split(string(decoded), ",")
	if len(parts) != cursorParts {
		parts = strings.Split(string(decoded), ":")
	}

	if len(parts) != cursorParts {
		return 0, 0, badRequest("invalid cursor")
	}

	begin, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return 0, 0, badRequest("invalid cursor")
	}

	id, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return 0, 0, badRequest("invalid cursor")
	}

	return begin, id, nil
}

// Reports lists reports in the range.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) Reports(ctx context.Context, filter Filter) (ReportsResponse, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return ReportsResponse{}, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	if limit > maxLimit {
		limit = maxLimit
	}

	var cursorBegin, cursorID int64

	hasCursor := false

	if filter.Cursor != "" {
		cursorBegin, cursorID, err = decodeCursor(filter.Cursor)
		if err != nil {
			return ReportsResponse{}, err
		}

		hasCursor = true
	}

	response := ReportsResponse{NextCursor: "", Reports: []ReportSummary{}}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		allowed, loadErr := svc.sourceReportIDs(ctx, tx, rng, &filter)
		if loadErr != nil {
			return loadErr
		}

		rows, listErr := svc.listReportRows(
			ctx,
			tx,
			rng,
			&filter,
			allowed,
			hasCursor,
			cursorBegin,
			cursorID,
			limit,
		)
		if listErr != nil {
			return listErr
		}

		return svc.fillReports(ctx, tx, rng, rows, limit, &response)
	})
	if err != nil {
		return ReportsResponse{
			NextCursor: "",
			Reports:    []ReportSummary{},
		}, fmt.Errorf(
			"reports: %w",
			err,
		)
	}

	return response, nil
}

func (svc *Service) sourceReportIDs(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, filter *Filter,
) (map[int64]bool, error) {
	if filter.Source == "" {
		return make(map[int64]bool), nil
	}

	records, err := svc.loadRecords(ctx, tx, rng, filter.Domain, true)
	if err != nil {
		return nil, err
	}

	allowed := make(map[int64]bool)

	for _, record := range records {
		if record.source.Key == filter.Source {
			allowed[record.reportID] = true
		}
	}

	return allowed, nil
}

func (svc *Service) listReportRows(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, filter *Filter,
	allowed map[int64]bool, hasCursor bool, cursorBegin, cursorID int64, limit int,
) ([]reportDBRow, error) {
	baseQuery := `SELECT id, kind, message, blob, org, email, extra_contact, report_id,
		begin, end, domain, schema, duplicate_of, conflict
		FROM reports
		WHERE ((begin + end) / 2) >= ? AND ((begin + end) / 2) < ?`
	baseArgs := []any{rng.fromUnix, rng.toUnix}

	if filter.Domain != "" {
		baseQuery += domainClause

		baseArgs = append(baseArgs, filter.Domain)
	}

	if filter.Reporter != "" {
		baseQuery += ` AND org = ?`

		baseArgs = append(baseArgs, filter.Reporter)
	}

	if filter.Kind != "" {
		baseQuery += ` AND kind = ?`

		baseArgs = append(baseArgs, filter.Kind)
	}

	page := func(
		withCursor bool, begin, id int64, count int,
	) ([]reportDBRow, error) {
		query := baseQuery

		args := append([]any{}, baseArgs...)

		if withCursor {
			query += ` AND (begin < ? OR (begin = ? AND id < ?))`

			args = append(args, begin, begin, id)
		}

		query += ` ORDER BY begin DESC, id DESC LIMIT ?`

		args = append(args, count)

		var rows []reportDBRow

		err := tx.SelectContext(ctx, &rows, query, args...)
		if err != nil {
			return nil, fmt.Errorf("list reports: %w", err)
		}

		return rows, nil
	}

	if filter.Source == "" {
		return page(hasCursor, cursorBegin, cursorID, limit+1)
	}

	// Source-filtered: walk keyset pages in SQL until limit+1 matches
	// are collected, instead of loading every report in the range.
	collected := make([]reportDBRow, 0, limit+1)
	withCursor, begin, id := hasCursor, cursorBegin, cursorID

	for {
		batch, err := page(withCursor, begin, id, limit+1)
		if err != nil {
			return nil, err
		}

		if len(batch) == 0 {
			break
		}

		for index := range batch {
			if allowed[batch[index].ID] {
				collected = append(collected, batch[index])
			}
		}

		if len(collected) > limit || len(batch) < limit+1 {
			break
		}

		last := batch[len(batch)-1]
		withCursor, begin, id = true, last.Begin, last.ID
	}

	return collected, nil
}

func (svc *Service) fillReports(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange,
	rows []reportDBRow, limit int, response *ReportsResponse,
) error {
	total := len(rows)
	page := rows

	if total > limit {
		page = rows[:limit]
	}

	summaries := make([]ReportSummary, 0, len(page))

	for index := range page {
		messages, passRate, tlsFailed, err := svc.reportStats(ctx, tx, rng, &page[index])
		if err != nil {
			return err
		}

		summaries = append(summaries, page[index].summary(rng, messages, passRate, tlsFailed))
	}

	response.Reports = summaries

	if total > limit && len(page) > 0 {
		last := page[len(page)-1]
		response.NextCursor = encodeCursor(last.Begin, last.ID)
	}

	return nil
}

func (svc *Service) loadReportRow(
	ctx context.Context, tx *sqlx.Tx, id int64,
) (*reportDBRow, error) {
	var row reportDBRow

	err := tx.GetContext(ctx, &row, `SELECT id, kind, message, blob, org, email, extra_contact,
		report_id, begin, end, domain, schema, duplicate_of, conflict
		FROM reports WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: report %d", ErrNotFound, id)
	}

	if err != nil {
		return nil, fmt.Errorf("load report: %w", err)
	}

	return &row, nil
}

// Report returns one report's detail.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) Report(
	ctx context.Context, id int64, filter Filter,
) (ReportDetail, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return ReportDetail{}, fmt.Errorf("report: %w", err)
	}

	detail := ReportDetail{
		Policy: nil, Email: "", ExtraContact: "", Schema: "",
		Records: []RecordDetail{}, TLSPolicies: []TLSPolicy{}, Copies: []ReportSummary{},
		Report: ReportSummary{
			PassRate: nil, DuplicateOf: nil, Domain: "", ReportID: "", Date: "", Org: "",
			Kind: "", ID: 0, Begin: 0, End: 0, Messages: 0, TLSFailed: 0,
			Duplicate: false, Conflict: false, LongWindow: false,
		},
		MessageID: 0,
	}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		row, loadErr := svc.loadReportRow(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}

		messages, passRate, tlsFailed, statsErr := svc.reportStats(ctx, tx, rng, row)
		if statsErr != nil {
			return statsErr
		}

		detail.Report = row.summary(rng, messages, passRate, tlsFailed)
		detail.MessageID = row.Message
		detail.Email = row.Email
		detail.ExtraContact = row.Extra
		detail.Schema = row.Schema

		policy, found, policyErr := svc.loadPolicy(ctx, tx, row.ID)
		if policyErr != nil {
			return policyErr
		}

		if found {
			detail.Policy = &policy
		}

		records, recErr := svc.loadReportRecords(ctx, tx, rng, row)
		if recErr != nil {
			return recErr
		}

		detail.Records = records

		tlsPolicies, tlsErr := svc.loadReportTLSPolicies(ctx, tx, row)
		if tlsErr != nil {
			return tlsErr
		}

		detail.TLSPolicies = tlsPolicies

		copies, copyErr := svc.loadCopies(ctx, tx, rng, row)
		if copyErr != nil {
			return copyErr
		}

		detail.Copies = copies

		return nil
	})
	if err != nil {
		return ReportDetail{}, fmt.Errorf("report: %w", err)
	}

	return detail, nil
}

func (svc *Service) loadPolicy(
	ctx context.Context, tx *sqlx.Tx, reportID int64,
) (Policy, bool, error) {
	var row policyDBRow

	err := tx.GetContext(ctx, &row, `SELECT COALESCE(p, '') AS p, COALESCE(sp, '') AS sp,
		COALESCE(np, '') AS np, pct AS pct, COALESCE(adkim, '') AS adkim,
		COALESCE(aspf, '') AS aspf, COALESCE(fo, '') AS fo,
		COALESCE(testing, '') AS testing, COALESCE(discovery, '') AS discovery,
		'' AS domain, 0 AS report_id, 0 AS begin, 0 AS end
		FROM policies WHERE report = ?`, reportID)
	if errors.Is(err, sql.ErrNoRows) {
		return Policy{
			P:         "",
			SP:        "",
			NP:        "",
			Pct:       nil,
			ADKIM:     "",
			ASPF:      "",
			FO:        "",
			Testing:   "",
			Discovery: "",
		}, false, nil
	}

	if err != nil {
		return Policy{
			P:         "",
			SP:        "",
			NP:        "",
			Pct:       nil,
			ADKIM:     "",
			ASPF:      "",
			FO:        "",
			Testing:   "",
			Discovery: "",
		}, false, fmt.Errorf("load policy: %w", err)
	}

	return rowToPolicy(&row), true, nil
}

func (svc *Service) loadReportRecords(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, reportRow *reportDBRow,
) ([]RecordDetail, error) {
	records := []RecordDetail{}

	if reportRow.Duplicate.Valid || reportRow.Kind != kindAggregate {
		return records, nil
	}

	// Load enriched records for this report only, reusing classification.
	all, err := svc.loadRecordsForReport(ctx, tx, rng, reportRow.ID)
	if err != nil {
		return nil, err
	}

	for _, record := range all {
		records = append(records, RecordDetail{
			HeaderFrom:   record.headerFrom,
			SourceKey:    record.source.Key,
			SourceName:   record.source.Name,
			Disposition:  record.disposition,
			DKIM:         record.dkimEval,
			SPF:          record.spfEval,
			Verdict:      record.verdict,
			SourceIP:     record.sourceIP,
			EnvelopeFrom: record.envFrom,
			EnvelopeTo:   record.envTo,
			Reasons:      record.reasons,
			Auth:         record.auth,
			Count:        record.count,
		})
	}

	return records, nil
}

func (svc *Service) loadRecordsForReport(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, reportID int64,
) ([]*enrichedRecord, error) {
	var rows []recordDBRow

	err := tx.SelectContext(ctx, &rows, `SELECT r.id AS report_id, r.begin AS begin,
		r.end AS end, r.domain AS domain, r.org AS org, COALESCE(p.adkim, '') AS adkim,
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
		WHERE rec.report = ? ORDER BY rec.id`, reportID)
	if err != nil {
		return nil, fmt.Errorf("load report records: %w", err)
	}

	authByRecord, err := svc.authForReport(ctx, tx, reportID)
	if err != nil {
		return nil, err
	}

	return enrichRows(rows, authByRecord, svc.sourceSet(), rng.loc), nil
}

func (svc *Service) loadReportTLSPolicies(
	ctx context.Context, tx *sqlx.Tx, reportRow *reportDBRow,
) ([]TLSPolicy, error) {
	policies := []TLSPolicy{}

	if reportRow.Duplicate.Valid || reportRow.Kind != kindTLS {
		return policies, nil
	}

	var rows []struct {
		Strings string `db:"strings"`
		MXHosts string `db:"mx_hosts"`
		Type    string `db:"type"`
		Domain  string `db:"domain"`
		ID      int64  `db:"id"`
		Success int64  `db:"success"`
		Failure int64  `db:"failure"`
	}

	err := tx.SelectContext(
		ctx,
		&rows,
		`SELECT id, type, domain, strings, mx_hosts, success, failure
		FROM tls_policies WHERE report = ? ORDER BY id`,
		reportRow.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("load TLS policies: %w", err)
	}

	for index := range rows {
		row := &rows[index]

		failures, err := svc.loadTLSFailureDetails(ctx, tx, row.ID)
		if err != nil {
			return nil, err
		}

		policies = append(policies, TLSPolicy{
			Type:       row.Type,
			Domain:     row.Domain,
			Strings:    parseJSONStringArray(row.Strings),
			MXHosts:    parseJSONStringArray(row.MXHosts),
			Failures:   failures,
			Success:    row.Success,
			Failure:    row.Failure,
			Reports:    1,
			LastSeenAt: reportRow.End,
		})
	}

	return policies, nil
}

func (svc *Service) loadTLSFailureDetails(
	ctx context.Context, tx *sqlx.Tx, policyID int64,
) ([]TLSFailureDetail, error) {
	var rows []struct {
		ResultType string `db:"result_type"`
		SendingIP  string `db:"sending_ip"`
		Receiving  string `db:"receiving_ip"`
		MX         string `db:"receiving_mx"`
		Reason     string `db:"reason_code"`
		Sessions   int64  `db:"failed_sessions"`
	}

	err := tx.SelectContext(ctx, &rows, `SELECT result_type, sending_ip, receiving_ip,
		receiving_mx, reason_code, failed_sessions
		FROM tls_failures WHERE policy = ? ORDER BY result_type, receiving_mx`, policyID)
	if err != nil {
		return nil, fmt.Errorf("load TLS failures: %w", err)
	}

	details := make([]TLSFailureDetail, 0, len(rows))

	for index := range rows {
		row := &rows[index]
		details = append(details, TLSFailureDetail{
			ResultType:     row.ResultType,
			SendingIP:      row.SendingIP,
			ReceivingIP:    row.Receiving,
			ReceivingMX:    row.MX,
			ReasonCode:     row.Reason,
			FailedSessions: row.Sessions,
		})
	}

	if details == nil {
		details = []TLSFailureDetail{}
	}

	return details, nil
}

func (svc *Service) loadCopies(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, reportRow *reportDBRow,
) ([]ReportSummary, error) {
	rootID := reportRow.ID
	if reportRow.Duplicate.Valid {
		rootID = reportRow.Duplicate.Int64
	}

	var rows []reportDBRow

	err := tx.SelectContext(ctx, &rows, `SELECT id, kind, message, blob, org, email, extra_contact,
		report_id, begin, end, domain, schema, duplicate_of, conflict
		FROM reports WHERE (id = ? OR duplicate_of = ?) AND id != ?
		ORDER BY begin DESC, id DESC`, rootID, rootID, reportRow.ID)
	if err != nil {
		return nil, fmt.Errorf("load copies: %w", err)
	}

	copies := make([]ReportSummary, 0, len(rows))

	for index := range rows {
		messages, passRate, tlsFailed, err := svc.reportStats(ctx, tx, rng, &rows[index])
		if err != nil {
			return nil, err
		}

		copies = append(copies, rows[index].summary(rng, messages, passRate, tlsFailed))
	}

	return copies, nil
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeFilename(name string) string {
	cleaned := unsafeFilenameChars.ReplaceAllString(name, "-")
	cleaned = strings.Trim(cleaned, "-.")

	if cleaned == "" {
		cleaned = "report"
	}

	return cleaned
}

func rawFilename(org, reportID, kind string) string {
	ext := "xml"
	if kind == kindTLS {
		ext = "json"
	}

	base := sanitizeFilename(org + "-" + reportID)
	suffix := "." + ext

	const maxName = 100

	if len(base)+len(suffix) > maxName {
		base = base[:maxName-len(suffix)]
		base = strings.TrimRight(base, "-.")
	}

	return base + suffix
}

// ReportRaw returns the extracted report document and a file name.
func (svc *Service) ReportRaw(ctx context.Context, id int64) ([]byte, string, error) {
	var (
		data     []byte
		filename string
	)

	err := svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		row, err := svc.loadReportRow(ctx, tx, id)
		if err != nil {
			return err
		}

		if len(row.Blob) == 0 {
			return fmt.Errorf("%w: report blob", ErrNotFound)
		}

		blob, err := store.GetBlob(ctx, tx, row.Blob)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: report blob", ErrNotFound)
		}

		if err != nil {
			return fmt.Errorf("read report blob: %w", err)
		}

		data = blob
		filename = rawFilename(row.Org, row.ReportID, row.Kind)

		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("report raw: %w", err)
	}

	return data, filename, nil
}

// MessageRaw returns the original message bytes and a file name.
func (svc *Service) MessageRaw(ctx context.Context, id int64) ([]byte, string, error) {
	var data []byte

	err := svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		var blob []byte

		err := tx.GetContext(ctx, &blob, `SELECT blob FROM messages WHERE id = ?`, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: message %d", ErrNotFound, id)
		}

		if err != nil {
			return fmt.Errorf("load message: %w", err)
		}

		if len(blob) == 0 {
			return fmt.Errorf("%w: message blob", ErrNotFound)
		}

		raw, err := store.GetBlob(ctx, tx, blob)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: message blob", ErrNotFound)
		}

		if err != nil {
			return fmt.Errorf("read message blob: %w", err)
		}

		data = raw

		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("message raw: %w", err)
	}

	return data, "message-" + strconv.FormatInt(id, 10) + ".eml", nil
}
