package ingest

import (
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
		_, recordsAdded, err = rebuild(ctx, tx)

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

		count, recordsAdded, err = rebuild(ctx, tx)

		return err
	})
	if err != nil {
		return 0, fmt.Errorf("commit reparsed reports: %w", err)
	}

	pipe.notify(recordsAdded)

	return count, nil
}

func rebuild(ctx context.Context, tx *sqlx.Tx) (int, bool, error) {
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
			Status string `db:"status"`
			Blob   []byte `db:"blob"`
			ID     int64  `db:"id"`
		}

		err = tx.GetContext(ctx, &row, `SELECT id, blob, status FROM messages
			WHERE id > ? AND blob IS NOT NULL ORDER BY id LIMIT 1`, lastID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}

		if err != nil {
			return 0, false, fmt.Errorf("find reparsable message: %w", err)
		}

		lastID = row.ID

		added, parseErr := rebuildMessage(ctx, tx, row.ID, row.Blob, row.Status == StatusPruned)
		if parseErr != nil {
			return 0, false, parseErr
		}

		count++
		recordsAdded = recordsAdded || added
	}

	err = deleteUnusedBlobs(ctx, tx)
	if err != nil {
		return 0, false, err
	}

	return count, recordsAdded, setParserVersion(ctx, tx)
}

func rebuildMessage(
	ctx context.Context, tx *sqlx.Tx, id int64, digest []byte, pruned bool,
) (bool, error) {
	raw, err := store.GetBlob(ctx, tx, digest)
	if err != nil {
		return false, fmt.Errorf("read raw message for reparse: %w", err)
	}

	var state messageWrite

	state.result.MessageID = id
	state.pruned = pruned

	err = state.parse(ctx, tx, raw)
	if err != nil {
		return false, err
	}

	status := state.result.Status
	if pruned {
		status = StatusPruned
	}

	_, err = tx.ExecContext(ctx, `UPDATE messages
		SET class = ?, status = ?, error = ?, parser_version = ? WHERE id = ?`,
		state.result.Class, status, state.messageError, report.ParserVersion, id)
	if err != nil {
		return false, fmt.Errorf("update reparsed message: %w", err)
	}

	return state.recordsAdded, nil
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

	cutoff := pipe.opts.Now().UTC().AddDate(0, 0, -days).Unix()

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

const (
	prunePrefix    = "ingest.prune/"
	expiredReports = `SELECT message, blob, domain FROM reports WHERE end < ?
		OR duplicate_of IN (SELECT id FROM reports WHERE duplicate_of IS NULL AND end < ?)`
)

func applyRetention(ctx context.Context, tx *sqlx.Tx, cutoff int64) (int64, error) {
	// Mixed messages keep their raw blob so surviving reports can be reparsed.
	// Persist which document/domain pairs were deleted, including cascading
	// duplicates whose own reported window may still be within retention.
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta (key, value)
		SELECT '`+prunePrefix+`' || message || '/r/' || hex(blob) || '/' || domain, '1'
		FROM (`+expiredReports+`)
		UNION ALL
		SELECT '`+prunePrefix+`' || message || '/f/' || arrival, '1'
		FROM failure_reports WHERE arrival < ?`, cutoff, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark expired reports: %w", err)
	}

	var count int64

	err = tx.GetContext(ctx, &count, `SELECT (SELECT COUNT(*) FROM (`+expiredReports+`))
		+ (SELECT COUNT(*) FROM failure_reports WHERE arrival < ?)`, cutoff, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("count expired reports: %w", err)
	}

	if count == 0 {
		return 0, nil
	}

	_, err = tx.ExecContext(ctx, `UPDATE messages SET status = ?
		WHERE id IN (SELECT message FROM (`+expiredReports+`)
		             UNION SELECT message FROM failure_reports WHERE arrival < ?)`,
		StatusPruned, cutoff, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark pruned messages: %w", err)
	}

	err = deleteExpiredRows(ctx, tx, cutoff)
	if err != nil {
		return 0, err
	}

	return count, deleteUnusedBlobs(ctx, tx)
}

func deleteExpiredRows(ctx context.Context, tx *sqlx.Tx, cutoff int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM reports WHERE end < ?`, cutoff)
	if err != nil {
		return fmt.Errorf("delete expired reports: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM failure_reports WHERE arrival < ?`, cutoff)
	if err != nil {
		return fmt.Errorf("delete expired failure reports: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE messages SET blob = NULL
		WHERE status = ? AND blob IS NOT NULL
		  AND id NOT IN (SELECT message FROM reports)
		  AND id NOT IN (SELECT message FROM failure_reports)`, StatusPruned)
	if err != nil {
		return fmt.Errorf("unlink fully pruned messages: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM meta
		WHERE key GLOB '`+prunePrefix+`*' AND NOT EXISTS (
			SELECT 1 FROM messages WHERE blob IS NOT NULL
			  AND id = CAST(substr(meta.key, length('`+prunePrefix+`') + 1) AS INTEGER)
		)`)
	if err != nil {
		return fmt.Errorf("delete markers for fully pruned messages: %w", err)
	}

	return nil
}

func prunedReport(ctx context.Context, tx *sqlx.Tx, id int64, row *reportRow) (bool, error) {
	key := fmt.Sprintf("%s%d/r/%X/%s", prunePrefix, id, row.digest, row.domain)

	_, exists, err := store.Meta(ctx, tx, key)
	if err != nil {
		return false, fmt.Errorf("read retention marker: %w", err)
	}

	return exists, nil
}

func prunedFailure(ctx context.Context, tx *sqlx.Tx, id, arrival int64) (bool, error) {
	key := fmt.Sprintf("%s%d/f/%d", prunePrefix, id, arrival)

	_, exists, err := store.Meta(ctx, tx, key)
	if err != nil {
		return false, fmt.Errorf("read retention marker: %w", err)
	}

	return exists, nil
}
