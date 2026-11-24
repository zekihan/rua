package imapsrc

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jmoiron/sqlx"
)

const (
	resultOK    = "ok"
	resultError = "error"

	eventUIDValidityReset = "uidvalidity-reset"
)

var errUint32Range = errors.New("value out of range")

type storedMailbox struct {
	UIDValidity uint32
	NextUID     uint32
}

func (fetcher *Fetcher) ensureMailbox(ctx context.Context, name string) error {
	return fetcher.exec(
		ctx,
		`INSERT INTO mailboxes (name) VALUES (?) ON CONFLICT (name) DO NOTHING`,
		name,
	)
}

func (fetcher *Fetcher) loadMailbox(ctx context.Context, name string) (storedMailbox, error) {
	var row struct {
		UIDValidity int64 `db:"uidvalidity"`
		NextUID     int64 `db:"next_uid"`
	}

	err := fetcher.database.Read(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		getErr := tx.GetContext(ctx, &row,
			`SELECT uidvalidity, next_uid FROM mailboxes WHERE name = ?`, name)
		if getErr != nil {
			return fmt.Errorf("read mailbox: %w", getErr)
		}

		return nil
	})
	if err != nil {
		return storedMailbox{UIDValidity: 0, NextUID: 0}, fmt.Errorf("mailbox %s: %w", name, err)
	}

	validity, err := uint32From(row.UIDValidity)
	if err != nil {
		return storedMailbox{
			UIDValidity: 0,
			NextUID:     0,
		}, fmt.Errorf(
			"mailbox %s uidvalidity: %w",
			name,
			err,
		)
	}

	next, err := uint32From(row.NextUID)
	if err != nil {
		return storedMailbox{
			UIDValidity: 0,
			NextUID:     0,
		}, fmt.Errorf(
			"mailbox %s next uid: %w",
			name,
			err,
		)
	}

	return storedMailbox{UIDValidity: validity, NextUID: next}, nil
}

func uint32From(value int64) (uint32, error) {
	if value < 0 || value > int64(^uint32(0)) {
		return 0, fmt.Errorf("%w: %d", errUint32Range, value)
	}

	return uint32(value), nil
}

func (fetcher *Fetcher) markStart(ctx context.Context, name string) error {
	return fetcher.exec(ctx, `UPDATE mailboxes SET last_fetch_start = ? WHERE name = ?`,
		fetcher.now().Unix(), name)
}

func (fetcher *Fetcher) markSuccess(ctx context.Context, name string) error {
	now := fetcher.now().Unix()

	return fetcher.exec(ctx, `
		UPDATE mailboxes
		SET last_fetch_end = ?, last_result = ?, last_error = '', last_success = ?
		WHERE name = ?`, now, resultOK, now, name)
}

func (fetcher *Fetcher) markFailure(ctx context.Context, name string, cause error) error {
	return fetcher.exec(ctx, `
		UPDATE mailboxes
		SET last_fetch_end = ?, last_result = ?, last_error = ?
		WHERE name = ?`,
		fetcher.now().Unix(), resultError, clipError(cause, fetcher.secrets()...), name)
}

func (fetcher *Fetcher) markConnectFailure(ctx context.Context, name string, cause error) error {
	now := fetcher.now().Unix()

	return fetcher.exec(ctx, `
		UPDATE mailboxes
		SET last_fetch_start = ?, last_fetch_end = ?, last_result = ?, last_error = ?
		WHERE name = ?`,
		now, now, resultError, clipError(cause, fetcher.secrets()...), name)
}

// saveNextUID moves the cursor forward only for the UIDVALIDITY this fetch
// observed. A concurrent fetch that has already passed next, or that has
// reset the generation, leaves the stored cursor where it is.
func (fetcher *Fetcher) saveNextUID(ctx context.Context, name string, validity, next uint32) error {
	if fetcher.beforeSave != nil {
		fetcher.beforeSave(name, next)
	}

	_, err := fetcher.execResult(ctx, `
		UPDATE mailboxes
		SET next_uid = ?
		WHERE name = ? AND uidvalidity = ? AND next_uid < ?`,
		int64(next), name, int64(validity), int64(next))

	return err
}

func (fetcher *Fetcher) saveUIDValidity(
	ctx context.Context,
	name string,
	validity uint32,
) (bool, error) {
	rows, err := fetcher.execResult(ctx, `
		UPDATE mailboxes
		SET uidvalidity = ?
		WHERE name = ? AND uidvalidity = 0`, int64(validity), name)
	if err != nil {
		return false, err
	}

	return rows > 0, nil
}

func (fetcher *Fetcher) resetValidity(
	ctx context.Context,
	name string,
	oldValue, observed uint32,
) (bool, error) {
	detail := strconv.FormatUint(
		uint64(oldValue),
		10,
	) + "->" + strconv.FormatUint(
		uint64(observed),
		10,
	)
	now := fetcher.now().Unix()
	applied := false

	err := fetcher.database.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			UPDATE mailboxes
			SET uidvalidity = ?, next_uid = 1
			WHERE name = ? AND uidvalidity = ?`,
			int64(observed), name, int64(oldValue))
		if execErr != nil {
			return fmt.Errorf("reset uidvalidity: %w", execErr)
		}

		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("rows affected: %w", rowsErr)
		}

		if rows == 0 {
			return nil
		}

		_, execErr = tx.ExecContext(ctx, `
			INSERT INTO events (at, kind, mailbox, detail)
			VALUES (?, ?, ?, ?)`, now, eventUIDValidityReset, name, detail)
		if execErr != nil {
			return fmt.Errorf("insert event: %w", execErr)
		}

		applied = true

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("record uidvalidity reset: %w", err)
	}

	return applied, nil
}

func (fetcher *Fetcher) exec(ctx context.Context, query string, args ...any) error {
	_, err := fetcher.execResult(ctx, query, args...)

	return err
}

func (fetcher *Fetcher) execResult(ctx context.Context, query string, args ...any) (int64, error) {
	var rows int64

	err := fetcher.database.Write(ctx, func(ctx context.Context, tx *sqlx.Tx) error {
		result, execErr := tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return fmt.Errorf("exec: %w", execErr)
		}

		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("rows affected: %w", rowsErr)
		}

		rows = affected

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("update mailbox: %w", err)
	}

	return rows, nil
}
