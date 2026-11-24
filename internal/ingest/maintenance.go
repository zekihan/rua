package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
)

var errRetentionDays = errors.New("retention days must be nonnegative")

// ReparseIfNeeded rebuilds derived rows if the parser version has changed.
// A new database only needs the current version recorded.
func (pipe *Pipeline) ReparseIfNeeded(ctx context.Context) (bool, error) {
	var ran, recordsAdded bool

	err := pipe.store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		version, _, err := store.Meta(ctx, tx, store.MetaParserVersion)
		if err != nil {
			return fmt.Errorf("check parser version: %w", err)
		}

		if version == strconv.Itoa(report.ParserVersion) {
			return nil
		}

		var haveMessages bool

		err = tx.GetContext(ctx, &haveMessages, `SELECT EXISTS (SELECT 1 FROM messages)`)
		if err != nil {
			return fmt.Errorf("check for stored messages: %w", err)
		}

		if !haveMessages {
			return setParserVersion(ctx, tx)
		}

		pipe.reparsing.Add(1)

		ran = true
		_, recordsAdded, err = pipe.rebuild(ctx, tx)

		return err
	})
	if ran {
		pipe.reparsing.Add(-1)
	}

	if err != nil {
		return false, fmt.Errorf("check or rebuild reports: %w", err)
	}

	pipe.notify(recordsAdded)

	return ran, nil
}

// Reparse rebuilds derived rows from stored messages in one transaction.
// Skipped and fully pruned messages do not have raw blobs to replay.
func (pipe *Pipeline) Reparse(ctx context.Context) (int, error) {
	pipe.reparsing.Add(1)
	defer pipe.reparsing.Add(-1)

	var (
		count        int
		recordsAdded bool
	)

	err := pipe.store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		var err error

		count, recordsAdded, err = pipe.rebuild(ctx, tx)

		return err
	})
	if err != nil {
		return 0, fmt.Errorf("commit reparsed reports: %w", err)
	}

	pipe.notify(recordsAdded)

	return count, nil
}

func (pipe *Pipeline) rebuild(ctx context.Context, tx *sqlx.Tx) (int, bool, error) {
	_, err := tx.ExecContext(ctx, `DELETE FROM reports; DELETE FROM failure_reports;`)
	if err != nil {
		return 0, false, fmt.Errorf("clear parsed reports: %w", err)
	}

	var (
		lastID       int64
		count        int
		recordsAdded bool
	)

	for {
		var row struct {
			Blob []byte `db:"blob"`
			ID   int64  `db:"id"`
		}

		err = tx.GetContext(ctx, &row, `SELECT id, blob FROM messages
			WHERE id > ? AND blob IS NOT NULL ORDER BY id LIMIT 1`, lastID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}

		if err != nil {
			return 0, false, fmt.Errorf("find reparsable message: %w", err)
		}

		lastID = row.ID

		added, parseErr := rebuildMessage(ctx, tx, row.ID, row.Blob)
		if parseErr != nil {
			return 0, false, parseErr
		}

		count++
		recordsAdded = recordsAdded || added
	}

	if pipe.opts.RetentionDays > 0 {
		_, err = applyRetention(ctx, tx, pipe.cutoff(pipe.opts.RetentionDays))
		if err != nil {
			return 0, false, err
		}
	}

	err = deleteUnusedBlobs(ctx, tx)
	if err != nil {
		return 0, false, err
	}

	return count, recordsAdded, setParserVersion(ctx, tx)
}

func rebuildMessage(
	ctx context.Context, tx *sqlx.Tx, id int64, digest []byte,
) (bool, error) {
	raw, err := store.GetBlob(ctx, tx, digest)
	if err != nil {
		return false, fmt.Errorf("read raw message for reparse: %w", err)
	}

	var state messageWrite

	state.result.MessageID = id

	err = state.parse(ctx, tx, raw)
	if err != nil {
		return false, err
	}

	blob := digest

	if state.result.Class == report.ClassFailure {
		sanitized := sanitizeFailureMessage(raw)
		if !bytes.Equal(sanitized, raw) {
			blob, err = store.PutBlob(ctx, tx, sanitized)
			if err != nil {
				return false, fmt.Errorf("save sanitized failure message: %w", err)
			}
		}
	}

	_, err = tx.ExecContext(ctx, `UPDATE messages
		SET blob = ?, class = ?, status = ?, error = ?, parser_version = ? WHERE id = ?`,
		blob, state.result.Class, state.result.Status, state.messageError, report.ParserVersion, id)
	if err != nil {
		return false, fmt.Errorf("update reparsed message: %w", err)
	}

	return state.recordsAdded, nil
}

func (pipe *Pipeline) cutoff(days int) int64 {
	return pipe.opts.Now().UTC().AddDate(0, 0, -days).Unix()
}

func setParserVersion(ctx context.Context, tx *sqlx.Tx) error {
	err := store.SetMeta(ctx, tx, store.MetaParserVersion, strconv.Itoa(report.ParserVersion))
	if err != nil {
		return fmt.Errorf("record parser version: %w", err)
	}

	return nil
}

func deleteUnusedBlobs(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM blobs
		WHERE sha256 NOT IN (SELECT blob FROM messages WHERE blob IS NOT NULL)
		  AND sha256 NOT IN (SELECT blob FROM reports)`)
	if err != nil {
		return fmt.Errorf("delete unreferenced blobs: %w", err)
	}

	return nil
}

// Prune removes expired reports and unreferenced raw data. The returned
// count includes duplicate and failure rows removed by retention.
func (pipe *Pipeline) Prune(ctx context.Context, days int) (int, error) {
	if days < 0 {
		return 0, errRetentionDays
	}

	if days == 0 {
		return 0, nil
	}

	cutoff := pipe.cutoff(days)

	var count int64

	err := pipe.store.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		var err error

		count, err = applyRetention(ctx, tx, cutoff)

		return err
	})
	if err != nil {
		return 0, fmt.Errorf("commit retention: %w", err)
	}

	return int(count), nil
}

// applyRetention deletes reports whose window ended before cutoff (with
// their duplicates) and failure reports that arrived before it. A message
// left without any report loses its raw blob and becomes pruned, so a
// reparse cannot bring its reports back; a message with surviving reports
// keeps its blob, and its expired reports are dropped again by the
// retention pass that follows every reparse.
// The expiredReport condition selects reports whose own window ended before the cutoff
// (bound twice). An original is kept while any of its copies is still
// within retention, since deleting it would cascade to that copy.
const expiredReport = `end < ? AND NOT EXISTS (
	SELECT 1 FROM reports AS copy WHERE copy.duplicate_of = reports.id AND copy.end >= ?)`

func applyRetention(ctx context.Context, tx *sqlx.Tx, cutoff int64) (int64, error) {
	var messages []int64

	err := tx.SelectContext(ctx, &messages, `
		SELECT message FROM reports WHERE `+expiredReport+`
		UNION SELECT message FROM failure_reports WHERE arrival < ?`, cutoff, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("find expired reports: %w", err)
	}

	if len(messages) == 0 {
		return 0, nil
	}

	var count int64

	err = tx.GetContext(ctx, &count, `SELECT
		(SELECT count(*) FROM reports WHERE `+expiredReport+`)
		+ (SELECT count(*) FROM failure_reports WHERE arrival < ?)`, cutoff, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("count expired reports: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM reports WHERE `+expiredReport, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired reports: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM failure_reports WHERE arrival < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired failure reports: %w", err)
	}

	for _, id := range messages {
		_, err = tx.ExecContext(ctx, `UPDATE messages SET blob = NULL, status = ?
			WHERE id = ?
			  AND NOT EXISTS (SELECT 1 FROM reports WHERE message = ?)
			  AND NOT EXISTS (SELECT 1 FROM failure_reports WHERE message = ?)`,
			StatusPruned, id, id, id)
		if err != nil {
			return 0, fmt.Errorf("unlink pruned message: %w", err)
		}
	}

	return count, deleteUnusedBlobs(ctx, tx)
}
