package ingest

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
)

func (state *messageWrite) storeAggregate(
	ctx context.Context, tx *sqlx.Tx, data []byte, value *report.Aggregate,
) error {
	row := reportRow{
		kind:         report.KindAggregate,
		blob:         nil,
		firstTLS:     nil,
		digest:       store.Digest(data),
		original:     data,
		parsed:       value,
		org:          value.OrgName,
		email:        value.Email,
		extraContact: value.ExtraContact,
		reportID:     value.ReportID,
		domain:       value.Domain,
		schema:       value.Schema,
		begin:        value.Begin,
		end:          value.End,
	}

	id, original, err := state.insertReport(ctx, tx, &row)
	if err != nil || id == 0 {
		return err
	}

	state.aggregateRows++

	if !original {
		return nil
	}

	policy := value.Policy

	_, err = tx.ExecContext(ctx, `INSERT INTO policies
		(report, p, sp, np, pct, adkim, aspf, fo, testing, discovery)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, policy.P, policy.SP, policy.NP, policy.Pct, policy.ADKIM, policy.ASPF,
		policy.FO, policy.Testing, policy.Discovery)
	if err != nil {
		return fmt.Errorf("insert DMARC policy: %w", err)
	}

	for i := range value.Records {
		err = insertRecord(ctx, tx, id, &value.Records[i])
		if err != nil {
			return err
		}
	}

	state.recordsAdded = state.recordsAdded || len(value.Records) > 0

	return nil
}

func insertRecord(ctx context.Context, tx *sqlx.Tx, reportID int64, row *report.Record) error {
	reasons, err := encodeArray(row.Reasons)
	if err != nil {
		return err
	}

	var recordID int64

	err = tx.GetContext(ctx, &recordID, `INSERT INTO records
		(report, source_ip, count, disposition, dkim, spf, reasons, header_from, envelope_from,
		 envelope_to) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		reportID, row.SourceIP, row.Count, row.Disposition, row.DKIM, row.SPF, reasons,
		row.HeaderFrom, row.EnvelopeFrom, row.EnvelopeTo)
	if err != nil {
		return fmt.Errorf("insert DMARC record: %w", err)
	}

	for _, auth := range row.Auth {
		_, err = tx.ExecContext(ctx, `INSERT INTO auth_results
			(record, method, domain, selector, scope, result, human_result)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			recordID, auth.Method, auth.Domain, auth.Selector, auth.Scope, auth.Result,
			auth.HumanResult)
		if err != nil {
			return fmt.Errorf("insert authentication result: %w", err)
		}
	}

	return nil
}

func groupTLSPolicies(value *report.TLS) (map[string][]report.TLSPolicy, []string) {
	groups := make(map[string][]report.TLSPolicy)

	var domains []string

	for _, policy := range value.Policies {
		if len(groups[policy.Domain]) == 0 {
			domains = append(domains, policy.Domain)
		}

		groups[policy.Domain] = append(groups[policy.Domain], policy)
	}

	return groups, domains
}

func (state *messageWrite) storeTLS(
	ctx context.Context, tx *sqlx.Tx, data []byte, value *report.TLS,
) error {
	groups, domains := groupTLSPolicies(value)

	row := reportRow{
		kind:         report.KindTLS,
		digest:       store.Digest(data),
		firstTLS:     nil,
		blob:         nil,
		parsed:       nil,
		extraContact: "",
		domain:       "",
		original:     data,
		org:          value.OrgName,
		email:        value.ContactInfo,
		reportID:     value.ReportID,
		schema:       "tlsrpt",
		begin:        value.Begin,
		end:          value.End,
	}
	for _, domain := range domains {
		selected := *value
		selected.Policies = groups[domain]
		row.domain = domain
		row.parsed = selected

		id, original, err := state.insertReport(ctx, tx, &row)
		if err != nil {
			return err
		}

		if id == 0 {
			continue
		}

		state.tlsRows++

		if original {
			for i := range selected.Policies {
				err = insertTLSPolicy(ctx, tx, id, &selected.Policies[i])
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func insertTLSPolicy(
	ctx context.Context,
	tx *sqlx.Tx,
	reportID int64,
	policy *report.TLSPolicy,
) error {
	strings, err := encodeArray(policy.Strings)
	if err != nil {
		return err
	}

	hosts, err := encodeArray(policy.MXHosts)
	if err != nil {
		return err
	}

	var policyID int64

	err = tx.GetContext(ctx, &policyID, `INSERT INTO tls_policies
		(report, type, domain, strings, mx_hosts, success, failure)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		reportID, policy.Type, policy.Domain, strings, hosts, policy.Success, policy.Failure)
	if err != nil {
		return fmt.Errorf("insert TLS policy: %w", err)
	}

	for _, failure := range policy.Failures {
		_, err = tx.ExecContext(ctx, `INSERT INTO tls_failures
			(policy, result_type, sending_ip, receiving_ip, receiving_mx, receiving_mx_helo,
			 failed_sessions, reason_code, additional_info)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			policyID, failure.ResultType, failure.SendingMTAIP, failure.ReceivingIP,
			failure.ReceivingMXHostname, failure.ReceivingMXHelo, failure.FailedSessions,
			failure.FailureReasonCode, failure.AdditionalInfoURI)
		if err != nil {
			return fmt.Errorf("insert TLS failure: %w", err)
		}
	}

	return nil
}

func (state *messageWrite) storeFailure(
	ctx context.Context, tx *sqlx.Tx, value *report.Failure,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO failure_reports
		(message, arrival, domain, source_ip, auth_failure, orig_from, orig_to, orig_subject)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		state.result.MessageID, value.Arrival, value.Domain, value.SourceIP, value.AuthFailure,
		value.OriginalFrom, value.OriginalTo, value.Subject)
	if err != nil {
		return fmt.Errorf("insert DMARC failure: %w", err)
	}

	state.failuresWritten++

	return nil
}
