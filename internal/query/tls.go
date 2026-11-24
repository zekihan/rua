package query

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
)

type tlsReportRow struct {
	Org    string `db:"org"`
	Domain string `db:"domain"`
	ID     int64  `db:"id"`
	Begin  int64  `db:"begin"`
	End    int64  `db:"end"`
}

func (svc *Service) loadTLSReports(
	ctx context.Context, tx *sqlx.Tx, rng *resolvedRange, domain string,
) ([]tlsReportRow, error) {
	query := `SELECT id, org, domain, begin, end FROM reports
		WHERE duplicate_of IS NULL AND kind = 'tls-report'
		AND ((begin + end) / 2) >= ? AND ((begin + end) / 2) < ?`
	args := []any{rng.fromUnix, rng.toUnix}

	if domain != "" {
		query += domainClause

		args = append(args, domain)
	}

	query += ` ORDER BY begin, id`

	var rows []tlsReportRow

	err := tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load TLS reports: %w", err)
	}

	return rows, nil
}

type tlsPolicyRow struct {
	Strings  string `db:"strings"`
	MXHosts  string `db:"mx_hosts"`
	Type     string `db:"type"`
	Domain   string `db:"domain"`
	ReportID int64  `db:"report"`
	PolicyID int64  `db:"id"`
	Success  int64  `db:"success"`
	Failure  int64  `db:"failure"`
}

func (svc *Service) loadTLSPolicies(
	ctx context.Context, tx *sqlx.Tx, reportIDs []int64,
) ([]tlsPolicyRow, error) {
	if len(reportIDs) == 0 {
		return []tlsPolicyRow{}, nil
	}

	query, args, err := sqlx.In(
		`SELECT id, report, type, domain, strings, mx_hosts, success, failure
		FROM tls_policies WHERE report IN (?) ORDER BY report, id`,
		reportIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("build TLS policy query: %w", err)
	}

	query = tx.Rebind(query)

	var rows []tlsPolicyRow

	err = tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load TLS policies: %w", err)
	}

	return rows, nil
}

// TLS returns per-domain TLS totals in range.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) TLS(ctx context.Context, filter Filter) (TLSResponse, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return TLSResponse{}, fmt.Errorf("tls: %w", err)
	}

	response := TLSResponse{Range: rng.rangeValue(), Domains: []TLSDomain{}}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		reports, loadErr := svc.loadTLSReports(ctx, tx, rng, filter.Domain)
		if loadErr != nil {
			return loadErr
		}

		reportIDs := make([]int64, 0, len(reports))
		reportByID := make(map[int64]tlsReportRow, len(reports))

		for index := range reports {
			reportIDs = append(reportIDs, reports[index].ID)
			reportByID[reports[index].ID] = reports[index]
		}

		policies, polErr := svc.loadTLSPolicies(ctx, tx, reportIDs)
		if polErr != nil {
			return polErr
		}

		type domainAgg struct {
			reports map[int64]struct{}
			success int64
			failure int64
			last    int64
		}

		byDomain := make(map[string]*domainAgg)

		for index := range reports {
			report := &reports[index]

			agg, ok := byDomain[report.Domain]
			if !ok {
				agg = &domainAgg{success: 0, failure: 0, reports: make(map[int64]struct{}), last: 0}
				byDomain[report.Domain] = agg
			}

			agg.reports[report.ID] = struct{}{}

			if report.End > agg.last {
				agg.last = report.End
			}
		}

		for index := range policies {
			policy := &policies[index]

			report, ok := reportByID[policy.ReportID]
			if !ok {
				continue
			}

			agg, ok := byDomain[report.Domain]
			if !ok {
				agg = &domainAgg{success: 0, failure: 0, reports: make(map[int64]struct{}), last: 0}
				byDomain[report.Domain] = agg
			}

			agg.success += policy.Success
			agg.failure += policy.Failure
		}

		domains := make([]TLSDomain, 0, len(byDomain))

		for domain, agg := range byDomain {
			domains = append(domains, TLSDomain{
				Domain:       domain,
				Success:      agg.success,
				Failure:      agg.failure,
				Reports:      int64(len(agg.reports)),
				LastReportAt: agg.last,
			})
		}

		slices.SortFunc(domains, func(first, second TLSDomain) int {
			return strings.Compare(first.Domain, second.Domain)
		})
		response.Domains = domains

		return nil
	})
	if err != nil {
		return TLSResponse{}, fmt.Errorf("tls: %w", err)
	}

	return response, nil
}

// TLSDomain returns detail for one TLS policy domain.
//
//nolint:gocritic // hugeParam: the exported signature takes Filter by value.
func (svc *Service) TLSDomain(
	ctx context.Context, domain string, filter Filter,
) (TLSDomainDetail, error) {
	rng, err := svc.resolveRange(&filter)
	if err != nil {
		return TLSDomainDetail{}, fmt.Errorf("tls domain: %w", err)
	}

	detail := TLSDomainDetail{
		Range: rng.rangeValue(), Domain: domain, Days: []TLSDay{}, Failures: []TLSFailure{},
		Policies: []TLSPolicy{}, Reporters: []Count{}, Success: 0, Failure: 0,
	}

	err = svc.deps.Store.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		reports, loadErr := svc.loadTLSReports(ctx, tx, rng, domain)
		if loadErr != nil {
			return loadErr
		}

		reportIDs := make([]int64, 0, len(reports))
		reportByID := make(map[int64]tlsReportRow, len(reports))

		for index := range reports {
			reportIDs = append(reportIDs, reports[index].ID)
			reportByID[reports[index].ID] = reports[index]
		}

		policies, polErr := svc.loadTLSPolicies(ctx, tx, reportIDs)
		if polErr != nil {
			return polErr
		}

		// Days.
		dayMap := make(map[string]*TLSDay)
		days := make([]TLSDay, 0, len(rng.days))

		for _, date := range rng.days {
			day := TLSDay{Date: date, Success: 0, Failure: 0}
			days = append(days, day)
			dayMap[date] = &days[len(days)-1]
		}

		var success, failure int64

		for index := range policies {
			policy := &policies[index]
			report := reportByID[policy.ReportID]
			date := midpointDate(report.Begin, report.End, rng.loc)

			if day, ok := dayMap[date]; ok {
				day.Success += policy.Success
				day.Failure += policy.Failure
			}

			success += policy.Success
			failure += policy.Failure
		}

		detail.Days = days
		detail.Success = success
		detail.Failure = failure

		failures, failErr := svc.tlsFailureGroups(ctx, tx, policies)
		if failErr != nil {
			return failErr
		}

		detail.Failures = failures
		detail.Policies = buildTLSPolicyGroups(policies, reportByID)
		detail.Reporters = buildTLSReporters(reports, policies, reportByID)

		return nil
	})
	if err != nil {
		return TLSDomainDetail{}, fmt.Errorf("tls domain: %w", err)
	}

	return detail, nil
}

func (svc *Service) tlsFailureGroups(
	ctx context.Context, tx *sqlx.Tx, policies []tlsPolicyRow,
) ([]TLSFailure, error) {
	groups := []TLSFailure{}

	if len(policies) == 0 {
		return groups, nil
	}

	policyIDs := make([]int64, 0, len(policies))
	for index := range policies {
		policyIDs = append(policyIDs, policies[index].PolicyID)
	}

	query, args, err := sqlx.In(`SELECT policy, result_type, receiving_ip, receiving_mx,
		reason_code, failed_sessions FROM tls_failures WHERE policy IN (?)`, policyIDs)
	if err != nil {
		return nil, fmt.Errorf("build TLS failures query: %w", err)
	}

	query = tx.Rebind(query)

	var rows []struct {
		ResultType string `db:"result_type"`
		Receiving  string `db:"receiving_ip"`
		MX         string `db:"receiving_mx"`
		Reason     string `db:"reason_code"`
		Policy     int64  `db:"policy"`
		Sessions   int64  `db:"failed_sessions"`
	}

	err = tx.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load TLS failure groups: %w", err)
	}

	type failureAgg struct {
		mxHosts  map[string]struct{}
		ips      map[string]struct{}
		reasons  map[string]struct{}
		sessions int64
	}

	byType := make(map[string]*failureAgg)

	for index := range rows {
		row := &rows[index]

		agg, ok := byType[row.ResultType]
		if !ok {
			agg = &failureAgg{
				mxHosts:  make(map[string]struct{}),
				ips:      make(map[string]struct{}),
				reasons:  make(map[string]struct{}),
				sessions: 0,
			}
			byType[row.ResultType] = agg
		}

		agg.sessions += row.Sessions

		if row.MX != "" {
			agg.mxHosts[row.MX] = struct{}{}
		}

		if row.Receiving != "" {
			agg.ips[row.Receiving] = struct{}{}
		}

		if row.Reason != "" {
			agg.reasons[row.Reason] = struct{}{}
		}
	}

	for resultType, agg := range byType {
		groups = append(groups, TLSFailure{
			ResultType:   resultType,
			MXHosts:      sortedKeys(agg.mxHosts),
			ReceivingIPs: sortedKeys(agg.ips),
			ReasonCodes:  sortedKeys(agg.reasons),
			Sessions:     agg.sessions,
		})
	}

	slices.SortFunc(groups, func(first, second TLSFailure) int {
		if first.Sessions != second.Sessions {
			if first.Sessions > second.Sessions {
				return -1
			}

			return 1
		}

		return strings.Compare(first.ResultType, second.ResultType)
	})

	return groups, nil
}

func buildTLSPolicyGroups(
	policies []tlsPolicyRow, reportByID map[int64]tlsReportRow,
) []TLSPolicy {
	type policyKey struct {
		policyType string
		domain     string
		stringsRaw string
	}

	type policyAgg struct {
		mxHosts  map[string]struct{}
		reports  map[int64]struct{}
		key      policyKey
		strings  []string
		success  int64
		failure  int64
		lastSeen int64
	}

	byKey := make(map[policyKey]*policyAgg)
	order := []policyKey{}

	for index := range policies {
		policy := &policies[index]
		key := policyKey{policyType: policy.Type, domain: policy.Domain, stringsRaw: policy.Strings}

		agg, ok := byKey[key]
		if !ok {
			agg = &policyAgg{
				strings:  parseJSONStringArray(policy.Strings),
				mxHosts:  make(map[string]struct{}),
				reports:  make(map[int64]struct{}),
				key:      key,
				success:  0,
				failure:  0,
				lastSeen: 0,
			}
			byKey[key] = agg
			order = append(order, key)
		}

		for _, host := range parseJSONStringArray(policy.MXHosts) {
			if host != "" {
				agg.mxHosts[host] = struct{}{}
			}
		}

		agg.success += policy.Success
		agg.failure += policy.Failure
		agg.reports[policy.ReportID] = struct{}{}

		if report, ok := reportByID[policy.ReportID]; ok && report.End > agg.lastSeen {
			agg.lastSeen = report.End
		}
	}

	groups := make([]TLSPolicy, 0, len(order))

	for _, key := range order {
		agg := byKey[key]
		groups = append(groups, TLSPolicy{
			Type:       agg.key.policyType,
			Domain:     agg.key.domain,
			Strings:    agg.strings,
			MXHosts:    sortedKeys(agg.mxHosts),
			Failures:   []TLSFailureDetail{},
			Success:    agg.success,
			Failure:    agg.failure,
			Reports:    int64(len(agg.reports)),
			LastSeenAt: agg.lastSeen,
		})
	}

	slices.SortFunc(groups, func(first, second TLSPolicy) int {
		if first.Type != second.Type {
			return strings.Compare(first.Type, second.Type)
		}

		return strings.Compare(
			strings.Join(first.Strings, "\n"),
			strings.Join(second.Strings, "\n"),
		)
	})

	return groups
}

func buildTLSReporters(
	reports []tlsReportRow, policies []tlsPolicyRow, reportByID map[int64]tlsReportRow,
) []Count {
	_ = reports
	sessionsByOrg := make(map[string]int64)

	for index := range policies {
		policy := &policies[index]

		report, ok := reportByID[policy.ReportID]
		if !ok {
			continue
		}

		sessionsByOrg[report.Org] += policy.Success + policy.Failure
	}

	counts := make([]Count, 0, len(sessionsByOrg))

	for org, sessions := range sessionsByOrg {
		counts = append(counts, Count{Value: org, Messages: sessions})
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
