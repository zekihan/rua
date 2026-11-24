package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/emersion/go-message"
	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/parse/dmarc"
	"github.com/zekihan/rua/internal/parse/tlsrpt"
	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
)

var (
	parseAggregate    = dmarc.ParseAggregate
	parseFailure      = dmarc.ParseFailure
	parseTLS          = tlsrpt.Parse
	errNoTLSPolicies  = errors.New("TLS report has no policies")
	errUnexpectedKind = errors.New("unexpected report kind")
)

const (
	headerFrom    = "From"
	headerSubject = "Subject"
)

type messageWrite struct {
	messageError    string
	result          Result
	aggregateRows   int
	tlsRows         int
	failuresWritten int
	recordsAdded    bool
}

// Ingest stores an RFC 5322 message or a bare document. Parse failures are
// committed with their message; errors returned here mean the write failed.
func (pipe *Pipeline) Ingest(ctx context.Context, raw []byte, src Source) (Result, error) {
	digest := store.Digest(raw)

	return pipe.writeMessage(ctx, digest, src, int64(len(raw)), func(
		ctx context.Context, tx *sqlx.Tx, state *messageWrite,
	) ([]byte, *message.Entity, error) {
		var (
			blob      []byte
			entity    *message.Entity
			headerErr error
		)
		if !looksBare(raw) {
			entity, headerErr = message.Read(bytes.NewReader(raw))
		}

		if len(raw) <= MaxMessageBytes {
			var err error

			blob, err = store.PutBlob(ctx, tx, raw)
			if err != nil {
				return nil, nil, fmt.Errorf("save raw message: %w", err)
			}
		}

		return blob, entity, parseMessage(ctx, tx, state, raw, entity, headerErr)
	})
}

// Skip records the header and full size of a message that exceeded the
// mailbox fetch limit. Its raw message blob is unavailable.
func (pipe *Pipeline) Skip(
	ctx context.Context, header []byte, size int64, src Source,
) (Result, error) {
	return pipe.writeMessage(ctx, store.Digest(header), src, size, func(
		_ context.Context, _ *sqlx.Tx, state *messageWrite,
	) ([]byte, *message.Entity, error) {
		state.result.Class = report.ClassOther
		state.result.Status = StatusSkipped
		state.messageError = "message of " + strconv.FormatInt(size, 10) +
			" bytes is larger than the fetch limit"

		entity, err := readHeader(header)
		if err != nil {
			entity = nil // Skipped messages retain their status without usable header fields.
		}

		return nil, entity, nil
	})
}

func (pipe *Pipeline) writeMessage(
	ctx context.Context, digest []byte, src Source, size int64,
	process func(context.Context, *sqlx.Tx, *messageWrite) ([]byte, *message.Entity, error),
) (Result, error) {
	var state messageWrite

	err := pipe.store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		now := pipe.opts.Now()

		date := src.InternalDate
		if date.IsZero() {
			date = now
		}

		err := tx.GetContext(ctx, &state.result.MessageID, `INSERT INTO messages
			(sha256, mailbox, uid, internaldate, size, class, status, parser_version, ingested_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (sha256) DO NOTHING RETURNING id`,
			digest, src.Mailbox, src.UID, date.Unix(), size, report.ClassOther, StatusOK,
			report.ParserVersion, now.Unix())
		if errors.Is(err, sql.ErrNoRows) {
			state.result.Known = true

			return nil
		}

		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}

		blob, entity, err := process(ctx, tx, &state)
		if err != nil {
			return err
		}

		var from, subject string
		if entity != nil {
			from = headerText(&entity.Header, headerFrom)
			subject = headerText(&entity.Header, headerSubject)
		}

		_, err = tx.ExecContext(
			ctx,
			`UPDATE messages
			SET blob = ?, from_addr = ?, subject = ?, class = ?, status = ?, error = ? WHERE id = ?`,
			blob,
			from,
			subject,
			state.result.Class,
			state.result.Status,
			state.messageError,
			state.result.MessageID,
		)
		if err != nil {
			return fmt.Errorf("update message: %w", err)
		}

		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("commit message: %w", err)
	}

	if !state.result.Known {
		pipe.opts.Metrics.MessageIngested(string(state.result.Class), state.result.Status)
		pipe.opts.Metrics.ReportsIngested(string(report.KindAggregate), state.aggregateRows)
		pipe.opts.Metrics.ReportsIngested(string(report.KindTLS), state.tlsRows)
		pipe.notify(state.recordsAdded)
	}

	return state.result, nil
}

func (pipe *Pipeline) notify(added bool) {
	if added && pipe.opts.NewIPs != nil {
		go pipe.opts.NewIPs()
	}
}

func (state *messageWrite) parse(ctx context.Context, tx *sqlx.Tx, raw []byte) error {
	var (
		entity    *message.Entity
		headerErr error
	)
	if !looksBare(raw) {
		entity, headerErr = message.Read(bytes.NewReader(raw))
	}

	return parseMessage(ctx, tx, state, raw, entity, headerErr)
}

func parseMessage(
	ctx context.Context, tx *sqlx.Tx, state *messageWrite, raw []byte,
	entity *message.Entity, headerErr error,
) error {
	ext := newExtraction(nil)
	ext.accept = func(doc document) error {
		return state.storeDocument(ctx, tx, ext, doc)
	}

	err := ext.extract(raw, entity, headerErr)
	if err != nil {
		return err
	}

	state.result.Class = ext.class
	state.result.Reports = state.aggregateRows + state.tlsRows

	state.result.Status = StatusOK
	if ext.firstError != nil {
		state.result.Status = StatusError
		if state.result.Reports+state.failuresWritten > 0 {
			state.result.Status = StatusPartial
		}

		state.messageError = boundedText(ext.firstError.Error(), report.MaxLong)
	}

	return nil
}

func (state *messageWrite) storeDocument(
	ctx context.Context, tx *sqlx.Tx, ext *extraction, doc document,
) error {
	switch doc.class {
	case report.ClassAggregate:
		value, err := parseAggregate(doc.data)
		if err != nil {
			ext.failed(fmt.Errorf("parse DMARC aggregate: %w", err))

			return nil
		}

		return state.storeAggregate(ctx, tx, doc.data, value)
	case report.ClassTLS:
		value, err := parseTLS(doc.data)
		if err != nil {
			ext.failed(fmt.Errorf("parse TLS report: %w", err))

			return nil
		}

		if len(value.Policies) == 0 {
			ext.failed(errNoTLSPolicies)

			return nil
		}

		return state.storeTLS(ctx, tx, doc.data, value)
	case report.ClassFailure:
		value, err := parseFailure(doc.data, doc.originalHeaders)
		if err != nil {
			ext.failed(fmt.Errorf("parse DMARC failure: %w", err))

			return nil
		}

		return state.storeFailure(ctx, tx, value)
	case report.ClassOther:
		return nil
	default:
		return fmt.Errorf("%w: %s", errUnexpectedKind, doc.class)
	}
}

type reportRow struct {
	parsed       any
	firstTLS     *tlsComparison
	kind         report.Kind
	org          string
	email        string
	extraContact string
	reportID     string
	domain       string
	schema       string
	blob         []byte
	digest       []byte
	original     []byte
	begin        int64
	end          int64
}

func (state *messageWrite) insertReport(
	ctx context.Context, tx *sqlx.Tx, row *reportRow,
) (int64, bool, error) {
	var first struct {
		Blob []byte `db:"blob"`
		ID   int64  `db:"id"`
	}

	err := tx.GetContext(ctx, &first, `SELECT id, blob FROM reports
		WHERE kind = ? AND org = ? AND report_id = ? AND domain = ? AND duplicate_of IS NULL`,
		row.kind, row.org, row.reportID, row.domain)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("find original report: %w", err)
	}

	var (
		duplicateOf sql.NullInt64
		conflict    bool
	)

	original := first.ID == 0
	if !original {
		duplicateOf = sql.NullInt64{Int64: first.ID, Valid: true}

		conflict, err = reportConflict(ctx, tx, row, first.Blob)
		if err != nil {
			return 0, false, err
		}
	}

	if row.blob == nil {
		row.blob, err = store.PutBlob(ctx, tx, row.original)
		if err != nil {
			return 0, false, fmt.Errorf("save extracted report: %w", err)
		}
	}

	var id int64

	err = tx.GetContext(ctx, &id, `INSERT INTO reports
		(kind, message, blob, org, email, extra_contact, report_id, begin, end, domain, schema,
		 duplicate_of, conflict)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		row.kind, state.result.MessageID, row.blob, row.org, row.email, row.extraContact,
		row.reportID, row.begin, row.end, row.domain, row.schema, duplicateOf, conflict)
	if err != nil {
		return 0, false, fmt.Errorf("insert report: %w", err)
	}

	return id, original, nil
}

func reportConflict(
	ctx context.Context, tx *sqlx.Tx, row *reportRow, firstDigest []byte,
) (bool, error) {
	if bytes.Equal(row.digest, firstDigest) {
		return false, nil
	}

	parsed, err := row.parseOriginal(ctx, tx, firstDigest)
	if err != nil {
		return false, err
	}

	oldDigest, err := canonicalDigest(parsed)
	if err != nil {
		return false, err
	}

	newDigest, err := canonicalDigest(row.parsed)
	if err != nil {
		return false, err
	}

	return !bytes.Equal(oldDigest, newDigest), nil
}

// A TLS document often supplies the first copy for many domains. Reuse
// the most recently parsed original without growing a cross-message cache.
type tlsComparison struct {
	policies map[string][]report.TLSPolicy
	value    *report.TLS
	blob     []byte
}

func (row *reportRow) parseOriginal(ctx context.Context, tx *sqlx.Tx, digest []byte) (any, error) {
	if row.firstTLS != nil && bytes.Equal(digest, row.firstTLS.blob) {
		return row.firstTLS.forDomain(row.domain), nil
	}

	data, err := store.GetBlob(ctx, tx, digest)
	if err != nil {
		return nil, fmt.Errorf("read original for conflict comparison: %w", err)
	}

	switch row.kind {
	case report.KindAggregate:
		value, err := parseAggregate(data)
		if err != nil {
			return nil, fmt.Errorf("parse original DMARC report for conflict comparison: %w", err)
		}

		return value, nil
	case report.KindTLS:
		value, err := parseTLS(data)
		if err != nil {
			return nil, fmt.Errorf("parse original TLS report for conflict comparison: %w", err)
		}

		policies, _ := groupTLSPolicies(value)
		row.firstTLS = &tlsComparison{value: value, blob: digest, policies: policies}

		return row.firstTLS.forDomain(row.domain), nil
	default:
		return nil, fmt.Errorf("%w: %s", errUnexpectedKind, row.kind)
	}
}

func (original *tlsComparison) forDomain(domain string) report.TLS {
	value := *original.value
	value.Policies = original.policies[domain]

	return value
}

func canonicalDigest(parsed any) ([]byte, error) {
	data, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("encode parsed report for conflict comparison: %w", err)
	}

	return store.Digest(data), nil
}

func encodeArray[Item any](items []Item) (string, error) {
	if len(items) == 0 {
		return "[]", nil
	}

	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode report array: %w", err)
	}

	return string(data), nil
}
