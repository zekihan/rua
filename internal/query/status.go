package query

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/telemetry"
)

type mailboxDBRow struct {
	Name        string        `db:"name"`
	LastError   string        `db:"last_error"`
	LastResult  string        `db:"last_result"`
	Pending     sql.NullInt64 `db:"pending"`
	LastStart   sql.NullInt64 `db:"last_fetch_start"`
	LastEnd     sql.NullInt64 `db:"last_fetch_end"`
	LastSuccess sql.NullInt64 `db:"last_success"`
	UIDValidity int64         `db:"uidvalidity"`
	NextUID     int64         `db:"next_uid"`
}

func nullIntPtr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}

	result := value.Int64

	return &result
}

func unixPtr(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}

	result := value.Unix()

	return &result
}

func (svc *Service) loadMailboxes(
	ctx context.Context, tx *sqlx.Tx,
) ([]mailboxDBRow, error) {
	var rows []mailboxDBRow

	err := tx.SelectContext(ctx, &rows, `SELECT name, uidvalidity, next_uid,
		last_fetch_start, last_fetch_end, last_result, last_error, last_success, pending
		FROM mailboxes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("load mailboxes: %w", err)
	}

	if rows == nil {
		rows = []mailboxDBRow{}
	}

	return rows, nil
}

// mailboxStatuses lists every stored mailbox plus any configured
// mailbox that has no row yet (with null fields), sorted by name.
func mailboxStatuses(mailboxes []mailboxDBRow, configured []string) []MailboxStatus {
	configuredFlags := configuredSet(configured)
	seen := make(map[string]bool, len(mailboxes))
	statuses := make([]MailboxStatus, 0, len(mailboxes)+len(configured))

	for index := range mailboxes {
		row := &mailboxes[index]
		seen[row.Name] = true
		statuses = append(statuses, MailboxStatus{
			Pending:        nullIntPtr(row.Pending),
			LastFetchStart: nullIntPtr(row.LastStart),
			LastFetchEnd:   nullIntPtr(row.LastEnd),
			LastSuccessAt:  nullIntPtr(row.LastSuccess),
			Name:           row.Name,
			LastResult:     row.LastResult,
			LastError:      row.LastError,
			UIDValidity:    uint32(row.UIDValidity), //nolint:gosec // Stored as non-negative.
			NextUID:        uint32(row.NextUID),     //nolint:gosec // Stored as non-negative.
			Configured:     configuredFlags[row.Name],
		})
	}

	for _, name := range configured {
		if seen[name] {
			continue
		}

		seen[name] = true
		statuses = append(statuses, MailboxStatus{
			Pending:        nil,
			LastFetchStart: nil,
			LastFetchEnd:   nil,
			LastSuccessAt:  nil,
			Name:           name,
			LastResult:     "",
			LastError:      "",
			UIDValidity:    0,
			NextUID:        0,
			Configured:     true,
		})
	}

	slices.SortFunc(statuses, func(first, second MailboxStatus) int {
		return strings.Compare(first.Name, second.Name)
	})

	return statuses
}

// staleState reports staleness over configured mailboxes only: rows
// for mailboxes no longer configured are ignored, and a configured
// mailbox with no row or no successful fetch yet counts as missing.
// When no configured names are known, the stored rows are used.
func staleState(
	mailboxes []mailboxDBRow,
	configured []string,
	now time.Time,
	staleAfter time.Duration,
	enabled bool,
) (bool, *int64) {
	byName := make(map[string]*mailboxDBRow, len(mailboxes))

	for index := range mailboxes {
		byName[mailboxes[index].Name] = &mailboxes[index]
	}

	names := configured
	if len(names) == 0 {
		names = make([]string, 0, len(mailboxes))

		for index := range mailboxes {
			names = append(names, mailboxes[index].Name)
		}
	}

	var oldest *int64

	hasMissing := false

	for _, name := range names {
		row, ok := byName[name]
		if !ok || !row.LastSuccess.Valid {
			hasMissing = true

			continue
		}

		value := row.LastSuccess.Int64
		if oldest == nil || value < *oldest {
			copyValue := value
			oldest = &copyValue
		}
	}

	if !enabled {
		return false, oldest
	}

	if hasMissing || oldest == nil {
		return true, oldest
	}

	threshold := now.Add(-staleAfter).Unix()

	return *oldest < threshold, oldest
}

func configuredSet(configured []string) map[string]bool {
	set := make(map[string]bool, len(configured))

	for _, name := range configured {
		set[name] = true
	}

	return set
}

// Status returns ingest state.
func (svc *Service) Status(ctx context.Context) (Status, error) {
	status := Status{
		Fetch: FetchState{
			NextFetchAt: nil, CooldownUntilAt: nil, Enabled: false, Running: false,
		},
		LastSuccessAt:     nil,
		Problems:          []MessageRow{},
		Mailboxes:         []MailboxStatus{},
		Classes:           []ClassCount{},
		Other:             []MessageRow{},
		Conflicts:         []ReportSummary{},
		Events:            []Event{},
		FailureReports:    []FailureReport{},
		StaleAfterSeconds: 0,
		Duplicates:        0,
		ParserVersion:     report.ParserVersion,
		EnrichQueue:       0,
		Stale:             false,
		Reparsing:         false,
	}

	if svc.deps.Reparsing != nil {
		status.Reparsing = svc.deps.Reparsing()
	}

	if svc.deps.EnrichQueue != nil {
		status.EnrichQueue = svc.deps.EnrichQueue()
	}

	status.StaleAfterSeconds = int64(svc.deps.StaleAfter / time.Second)

	if svc.deps.Fetch != nil {
		fetch := svc.deps.Fetch()
		status.Fetch = FetchState{
			NextFetchAt:     unixPtr(fetch.NextFetch),
			CooldownUntilAt: unixPtr(fetch.CooldownUntil),
			Enabled:         fetch.Enabled,
			Running:         fetch.Running,
		}
	} else {
		status.Fetch = FetchState{
			NextFetchAt:     nil,
			CooldownUntilAt: nil,
			Enabled:         false,
			Running:         false,
		}
	}

	// Use a range covering all time for conflict summaries.
	rng := &resolvedRange{
		loc:      time.UTC,
		fromStr:  "",
		toStr:    "",
		fromUnix: 0,
		toUnix:   0,
		days:     []string{},
	}

	// For summaries we only need a location; default UTC unless a test sets
	// Now in another zone is irrelevant.
	err := svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		mailboxes, err := svc.loadMailboxes(ctx, tx)
		if err != nil {
			return err
		}

		status.Mailboxes = mailboxStatuses(mailboxes, svc.deps.Mailboxes)

		stale, lastSuccess := staleState(
			mailboxes,
			svc.deps.Mailboxes,
			svc.now(),
			svc.deps.StaleAfter,
			svc.deps.IMAPEnabled,
		)
		status.Stale = stale
		status.LastSuccessAt = lastSuccess

		classes, err := svc.loadClasses(ctx, tx)
		if err != nil {
			return err
		}

		status.Classes = classes

		problems, err := svc.loadMessages(ctx, tx, `status IN ('error', 'partial', 'skipped')`, nil)
		if err != nil {
			return err
		}

		status.Problems = problems

		other, err := svc.loadMessages(ctx, tx, `class = 'other'`, nil)
		if err != nil {
			return err
		}

		status.Other = other

		var duplicates int64

		err = tx.GetContext(
			ctx,
			&duplicates,
			`SELECT COUNT(*) FROM reports WHERE duplicate_of IS NOT NULL`,
		)
		if err != nil {
			return fmt.Errorf("count duplicates: %w", err)
		}

		status.Duplicates = duplicates

		conflicts, err := svc.loadConflicts(ctx, tx, rng)
		if err != nil {
			return err
		}

		status.Conflicts = conflicts

		events, err := svc.loadEvents(ctx, tx)
		if err != nil {
			return err
		}

		status.Events = events

		failures, err := svc.loadFailureReports(ctx, tx)
		if err != nil {
			return err
		}

		status.FailureReports = failures

		return nil
	})
	if err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}

	return status, nil
}

func (svc *Service) loadClasses(ctx context.Context, tx *sqlx.Tx) ([]ClassCount, error) {
	var rows []struct {
		Class    string `db:"class"`
		Status   string `db:"status"`
		Messages int64  `db:"messages"`
	}

	err := tx.SelectContext(ctx, &rows, `SELECT class AS class, status AS status,
		COUNT(*) AS messages FROM messages GROUP BY class, status ORDER BY class, status`)
	if err != nil {
		return nil, fmt.Errorf("load classes: %w", err)
	}

	result := make([]ClassCount, 0, len(rows))

	for index := range rows {
		result = append(result, ClassCount{
			Class: rows[index].Class, Status: rows[index].Status, Messages: rows[index].Messages,
		})
	}

	return result, nil
}

type messageDBRow struct {
	Mailbox string `db:"mailbox"`
	From    string `db:"from_addr"`
	Subject string `db:"subject"`
	Class   string `db:"class"`
	Status  string `db:"status"`
	Error   string `db:"error"`
	Blob    []byte `db:"blob"`
	ID      int64  `db:"id"`
	Date    int64  `db:"internaldate"`
	Size    int64  `db:"size"`
	UID     int64  `db:"uid"`
	HasDocs bool   `db:"has_docs"`
}

func (svc *Service) loadMessages(
	ctx context.Context, tx *sqlx.Tx, where string, args []any,
) ([]MessageRow, error) {
	query := `SELECT id, mailbox, uid, internaldate, size, from_addr, subject, class, status, error, blob,
		EXISTS (SELECT 1 FROM documents WHERE message = messages.id) AS has_docs
		FROM messages WHERE ` + where + ` ORDER BY internaldate DESC, id DESC LIMIT 200`

	var rows []messageDBRow

	err := tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}

	result := make([]MessageRow, 0, len(rows))

	for index := range rows {
		row := &rows[index]
		result = append(result, MessageRow{
			Mailbox:      row.Mailbox,
			From:         row.From,
			Subject:      row.Subject,
			Class:        row.Class,
			Status:       row.Status,
			Error:        row.Error,
			ID:           row.ID,
			InternalDate: row.Date,
			Size:         row.Size,
			UID:          uint32(row.UID), //nolint:gosec // Stored UID is non-negative.
			HasRaw:       len(row.Blob) > 0 || row.HasDocs,
		})
	}

	return result, nil
}

func (svc *Service) loadConflicts(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange,
) ([]ReportSummary, error) {
	var rows []reportDBRow

	err := tx.SelectContext(ctx, &rows, `SELECT id, kind, message, blob, org, email, extra_contact,
		report_id, begin, end, domain, schema, duplicate_of, conflict
		FROM reports WHERE conflict = 1 ORDER BY begin DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("load conflicts: %w", err)
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

func (svc *Service) loadEvents(ctx context.Context, tx *sqlx.Tx) ([]Event, error) {
	var rows []struct {
		Kind    string `db:"kind"`
		Mailbox string `db:"mailbox"`
		Detail  string `db:"detail"`
		At      int64  `db:"at"`
	}

	err := tx.SelectContext(ctx, &rows, `SELECT kind, mailbox, detail, at FROM events
		ORDER BY at DESC, id DESC LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}

	events := make([]Event, 0, len(rows))

	for index := range rows {
		events = append(events, Event{
			Kind:    rows[index].Kind,
			Mailbox: rows[index].Mailbox,
			Detail:  rows[index].Detail,
			At:      rows[index].At,
		})
	}

	return events, nil
}

func (svc *Service) loadFailureReports(
	ctx context.Context, tx *sqlx.Tx,
) ([]FailureReport, error) {
	var rows []struct {
		Domain  string `db:"domain"`
		Source  string `db:"source_ip"`
		Auth    string `db:"auth_failure"`
		From    string `db:"orig_from"`
		To      string `db:"orig_to"`
		Subject string `db:"orig_subject"`
		Message int64  `db:"message"`
		Arrival int64  `db:"arrival"`
	}

	err := tx.SelectContext(ctx, &rows, `SELECT domain, source_ip, auth_failure,
		orig_from, orig_to, orig_subject, message, arrival
		FROM failure_reports ORDER BY arrival DESC, id DESC LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("load failure reports: %w", err)
	}

	reports := make([]FailureReport, 0, len(rows))

	for index := range rows {
		reports = append(reports, FailureReport{
			Domain:      rows[index].Domain,
			SourceIP:    rows[index].Source,
			AuthFailure: rows[index].Auth,
			From:        rows[index].From,
			To:          rows[index].To,
			Subject:     rows[index].Subject,
			MessageID:   rows[index].Message,
			ArrivalAt:   rows[index].Arrival,
		})
	}

	return reports, nil
}

// CollectMetrics refreshes the stored-data gauges.
func (svc *Service) CollectMetrics(ctx context.Context, metrics *telemetry.Metrics) error {
	if metrics == nil {
		return nil
	}

	svc.gaugesMu.Lock()
	defer svc.gaugesMu.Unlock()

	seen := map[gaugeKey]bool{}

	err := svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		var classRows []struct {
			Class  string `db:"class"`
			Status string `db:"status"`
			Count  int64  `db:"cnt"`
		}

		err := tx.SelectContext(ctx, &classRows, `SELECT class, status, COUNT(*) AS cnt
			FROM messages GROUP BY class, status`)
		if err != nil {
			return fmt.Errorf("collect message metrics: %w", err)
		}

		for index := range classRows {
			row := &classRows[index]
			metrics.Messages(row.Class, row.Status, row.Count)
			seen[gaugeKey{metric: "messages", first: row.Class, second: row.Status}] = true
		}

		rng := &resolvedRange{
			loc: time.UTC, fromStr: "", toStr: "", fromUnix: 0, toUnix: 0, days: []string{},
		}

		records, err := svc.loadRecords(ctx, tx, rng, "", false)
		if err != nil {
			return err
		}

		type domainVerdict struct {
			domain  string
			verdict string
		}

		totals := make(map[domainVerdict]int64)

		for _, record := range records {
			totals[domainVerdict{domain: record.domain, verdict: record.verdict}] += record.count
		}

		keys := make([]domainVerdict, 0, len(totals))
		for key := range totals {
			keys = append(keys, key)
		}

		slices.SortFunc(keys, func(first, second domainVerdict) int {
			if first.domain != second.domain {
				return strings.Compare(first.domain, second.domain)
			}

			return strings.Compare(first.verdict, second.verdict)
		})

		for _, key := range keys {
			metrics.Records(key.domain, key.verdict, totals[key])
			seen[gaugeKey{metric: "records", first: key.domain, second: key.verdict}] = true
		}

		mailboxes, err := svc.loadMailboxes(ctx, tx)
		if err != nil {
			return err
		}

		stale, lastSuccess := staleState(
			mailboxes,
			svc.deps.Mailboxes,
			svc.now(),
			svc.deps.StaleAfter,
			svc.deps.IMAPEnabled,
		)
		metrics.Stale(stale)

		if lastSuccess != nil {
			metrics.LastSuccess(*lastSuccess)
		} else {
			metrics.LastSuccess(0)
		}

		queue := 0
		if svc.deps.EnrichQueue != nil {
			queue = svc.deps.EnrichQueue()
		}

		metrics.EnrichQueue(queue)

		return nil
	})
	if err != nil {
		return fmt.Errorf("collect metrics: %w", err)
	}

	svc.resetVanished(metrics, seen)

	return nil
}

// resetVanished zeroes the gauges set last time but not this time.
func (svc *Service) resetVanished(metrics *telemetry.Metrics, seen map[gaugeKey]bool) {
	for key := range svc.gauges {
		if seen[key] {
			continue
		}

		if key.metric == "messages" {
			metrics.Messages(key.first, key.second, 0)
		} else {
			metrics.Records(key.first, key.second, 0)
		}
	}

	svc.gauges = seen
}
