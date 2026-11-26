package query

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
)

// quietByRule is the Overview quiet-reporter rule: reported before the
// range and nothing in the last quietDays before the cutoff.
func quietByRule(firstEnd, lastEnd, fromUnix, threshold int64) bool {
	return firstEnd < fromUnix && lastEnd < threshold
}

func (svc *Service) quietCutoff(rng *resolvedRange) int64 {
	return min(rng.toUnix, svc.now().Unix())
}

// Reporters returns every org that ever sent an original aggregate
// report for the domain filter, with its in-range activity.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) Reporters(ctx context.Context, filter Filter) (ReportersResponse, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return ReportersResponse{}, fmt.Errorf("reporters: %w", err)
	}

	response := ReportersResponse{Range: rng.rangeValue(), Reporters: []Reporter{}}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		reporters, loadErr := svc.loadReporters(ctx, tx, rng, filter.Domain)
		if loadErr != nil {
			return loadErr
		}

		response.Reporters = reporters

		return nil
	})
	if err != nil {
		return ReportersResponse{}, fmt.Errorf("reporters: %w", err)
	}

	return response, nil
}

type reporterHistoryRow struct {
	Org             string        `db:"org"`
	FirstEnd        int64         `db:"first_end"`
	LastEnd         int64         `db:"last_end"`
	LastEndAtCutoff sql.NullInt64 `db:"last_end_at_cutoff"`
}

type reporterRangeRow struct {
	Org      string `db:"org"`
	Reports  int64  `db:"reports"`
	Messages int64  `db:"messages"`
}

func (svc *Service) loadReporters(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string,
) ([]Reporter, error) {
	cutoff := svc.quietCutoff(rng)
	historyQuery := `SELECT org AS org, MIN(end) AS first_end, MAX(end) AS last_end,
		MAX(CASE WHEN end <= ? THEN end END) AS last_end_at_cutoff
		FROM reports
		WHERE duplicate_of IS NULL AND kind = 'dmarc-aggregate'`
	rangeQuery := `SELECT r.org AS org, COUNT(DISTINCT r.id) AS reports,
		COALESCE(SUM(rec.count), 0) AS messages
		FROM reports r
		LEFT JOIN records rec ON rec.report = r.id
		WHERE r.duplicate_of IS NULL AND r.kind = 'dmarc-aggregate'
		AND ((r.begin + r.end) / 2) >= ? AND ((r.begin + r.end) / 2) < ?`
	historyArgs := []any{cutoff}
	rangeArgs := []any{rng.fromUnix, rng.toUnix}

	if domain != "" {
		historyQuery += domainClause

		historyArgs = append(historyArgs, domain)
		rangeQuery += rDomainClause

		rangeArgs = append(rangeArgs, domain)
	}

	historyQuery += ` GROUP BY org`
	rangeQuery += ` GROUP BY r.org`

	var history []reporterHistoryRow

	err := tx.SelectContext(ctx, &history, historyQuery, historyArgs...)
	if err != nil {
		return nil, fmt.Errorf("load reporters: %w", err)
	}

	var inRange []reporterRangeRow

	err = tx.SelectContext(ctx, &inRange, rangeQuery, rangeArgs...)
	if err != nil {
		return nil, fmt.Errorf("load reporter range stats: %w", err)
	}

	statsByOrg := make(map[string]reporterRangeRow, len(inRange))

	for index := range inRange {
		statsByOrg[inRange[index].Org] = inRange[index]
	}

	threshold := cutoff - quietDays*daySeconds
	reporters := make([]Reporter, 0, len(history))

	for index := range history {
		row := &history[index]

		domains, domainsErr := svc.reporterDomains(ctx, tx, row.Org, domain)
		if domainsErr != nil {
			return nil, domainsErr
		}

		stats := statsByOrg[row.Org]
		quiet := row.LastEndAtCutoff.Valid &&
			quietByRule(row.FirstEnd, row.LastEndAtCutoff.Int64, rng.fromUnix, threshold)
		reporters = append(reporters, Reporter{
			Org:           row.Org,
			Domains:       domains,
			Reports:       stats.Reports,
			Messages:      stats.Messages,
			FirstReportAt: row.FirstEnd,
			LastReportAt:  row.LastEnd,
			Quiet:         quiet,
		})
	}

	slices.SortFunc(reporters, func(first, second Reporter) int {
		if first.Quiet != second.Quiet {
			if first.Quiet {
				return -1
			}

			return 1
		}

		if first.LastReportAt != second.LastReportAt {
			if first.LastReportAt > second.LastReportAt {
				return -1
			}

			return 1
		}

		return strings.Compare(first.Org, second.Org)
	})

	return reporters, nil
}
