package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
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
	uint32Bytes   = 4
)

type messageWrite struct {
	preserved         map[reportIdentity][]int64
	messageError      string
	result            Result
	aggregateRows     int
	tlsRows           int
	failuresWritten   int
	recordsAdded      bool
	failureContaining bool
}

// reportIdentity identifies a report row across a reparse (DESIGN.md
// section 5 report identity, scoped to its message).
type reportIdentity struct {
	kind     string
	org      string
	reportID string
	domain   string
	message  int64
}

const reportIDSeqKey = "report_id_seq"

// ensureReportIDSeq initializes the monotonic report id sequence from the
// largest id in use, so ids of deleted reports are never reused.
func ensureReportIDSeq(ctx context.Context, tx *sqlx.Tx) (int64, error) {
	var maxID sql.NullInt64

	err := tx.GetContext(ctx, &maxID, `SELECT max(id) FROM reports`)
	if err != nil {
		return 0, fmt.Errorf("read largest report id: %w", err)
	}

	stored, ok, err := store.Meta(ctx, tx, reportIDSeqKey)
	if err != nil {
		return 0, fmt.Errorf("read report id sequence: %w", err)
	}

	var seq int64

	if ok {
		seq, err = strconv.ParseInt(stored, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", reportIDSeqKey, err)
		}
	}

	changed := !ok

	if maxID.Int64 > seq {
		seq = maxID.Int64
		changed = true
	}

	if changed {
		err = store.SetMeta(ctx, tx, reportIDSeqKey, strconv.FormatInt(seq, 10))
		if err != nil {
			return 0, fmt.Errorf("write report id sequence: %w", err)
		}
	}

	return seq, nil
}

func allocateReportID(ctx context.Context, tx *sqlx.Tx) (int64, error) {
	seq, err := ensureReportIDSeq(ctx, tx)
	if err != nil {
		return 0, err
	}

	seq++

	err = store.SetMeta(ctx, tx, reportIDSeqKey, strconv.FormatInt(seq, 10))
	if err != nil {
		return 0, fmt.Errorf("write report id sequence: %w", err)
	}

	return seq, nil
}

func (state *messageWrite) preservedID(row *reportRow) int64 {
	key := reportIdentity{
		message:  state.result.MessageID,
		kind:     string(row.kind),
		org:      row.org,
		reportID: row.reportID,
		domain:   row.domain,
	}

	queue := state.preserved[key]
	if len(queue) == 0 {
		return 0
	}

	state.preserved[key] = queue[1:]

	return queue[0]
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

		collection, err := extractMessage(raw, entity, headerErr)
		if err != nil {
			return nil, nil, err
		}

		state.failureContaining = collection.failureContaining

		err = state.applyDocuments(ctx, tx, collection.docs)
		if err != nil {
			return nil, nil, err
		}

		switch {
		case collection.failureContaining:
			err = writeDocuments(ctx, tx, state.result.MessageID, collection.docs)
			if err != nil {
				return nil, nil, err
			}
		case len(raw) <= MaxMessageBytes:
			blob, err = store.PutBlob(ctx, tx, raw)
			if err != nil {
				return nil, nil, fmt.Errorf("save raw message: %w", err)
			}
		default:
			// Oversized messages keep no stored form.
		}

		return blob, entity, nil
	})
}

// Skip records the header and full size of a message that exceeded the
// mailbox fetch limit. Its raw message blob is unavailable. The digest is
// the header bytes plus the mailbox, UIDVALIDITY and UID, so two oversized
// messages that share a header do not collapse into one row.
func (pipe *Pipeline) Skip(
	ctx context.Context, header []byte, size int64, src Source,
) (Result, error) {
	return pipe.writeMessage(ctx, skipDigest(header, src), src, size, func(
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

// skipDigest is sha256(header || 0x00 || mailbox || 0x00 || uidvalidity || 0x00 || uid).
func skipDigest(header []byte, src Source) []byte {
	var num [uint32Bytes]byte

	encoded := make(
		[]byte, 0, len(header)+1+len(src.Mailbox)+1+uint32Bytes+1+uint32Bytes,
	)
	encoded = append(encoded, header...)
	encoded = append(encoded, 0)
	encoded = append(encoded, src.Mailbox...)
	encoded = append(encoded, 0)

	binary.BigEndian.PutUint32(num[:], src.UIDValidity)
	encoded = append(encoded, num[:]...)
	encoded = append(encoded, 0)

	binary.BigEndian.PutUint32(num[:], src.UID)
	encoded = append(encoded, num[:]...)

	return store.Digest(encoded)
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

// applyDocuments evaluates extraction output and writes the derived
// rows. It is the one path fresh ingest and replay share.
func (state *messageWrite) applyDocuments(
	ctx context.Context, tx *sqlx.Tx, docs []storedDoc,
) error {
	outcome := evaluateDocuments(docs)

	state.result.Class = outcome.class
	state.result.Status = outcome.status
	state.messageError = outcome.errText

	for index := range outcome.items {
		err := state.storeItem(ctx, tx, &outcome.items[index])
		if err != nil {
			return err
		}
	}

	state.result.Reports = state.aggregateRows + state.tlsRows

	return nil
}

func (state *messageWrite) storeItem(
	ctx context.Context, tx *sqlx.Tx, item *evalItem,
) error {
	switch item.class {
	case report.ClassAggregate:
		return state.storeAggregate(ctx, tx, item.data, item.aggregate)
	case report.ClassTLS:
		return state.storeTLS(ctx, tx, item.data, item.tlsReport)
	case report.ClassFailure:
		return state.storeFailure(ctx, tx, item.failure)
	case report.ClassOther:
		return nil
	default:
		return fmt.Errorf("%w: %s", errUnexpectedKind, item.class)
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

	id := state.preservedID(row)
	if id == 0 {
		id, err = allocateReportID(ctx, tx)
		if err != nil {
			return 0, false, err
		}
	}

	err = tx.GetContext(ctx, &id, `INSERT INTO reports
		(id, kind, message, blob, org, email, extra_contact, report_id, begin, end, domain, schema,
		 duplicate_of, conflict)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		id, row.kind, state.result.MessageID, row.blob, row.org, row.email, row.extraContact,
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
