package imapsrc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/zekihan/rua/internal/ingest"
)

var (
	errUIDMissing  = errors.New("missing from response")
	errBodyMissing = errors.New("missing body")
)

func (fetcher *Fetcher) fetch(ctx context.Context) error {
	err := ctx.Err()
	if err != nil {
		return contextError(err)
	}

	session, err := fetcher.connect(ctx)
	if err != nil {
		if isCancel(err) {
			return err
		}

		return fetcher.failMailboxes(ctx, err)
	}

	defer func() {
		logoutErr := session.Logout(ctx)
		if logoutErr != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "IMAP logout failed", slog.Any("error", logoutErr))
		}
	}()

	var errs []error

	for _, name := range fetcher.cfg.Mailboxes {
		err = ctx.Err()
		if err != nil {
			return contextError(err)
		}

		err = fetcher.fetchMailbox(ctx, session, name)
		if err != nil {
			if isCancel(err) {
				return err
			}

			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (fetcher *Fetcher) failMailboxes(ctx context.Context, cause error) error {
	recorded := cause

	for _, name := range fetcher.cfg.Mailboxes {
		err := fetcher.ensureMailbox(ctx, name)
		if err != nil {
			recorded = errors.Join(recorded, err)

			continue
		}

		err = fetcher.markConnectFailure(ctx, name, cause)
		if err != nil {
			recorded = errors.Join(recorded, err)
		}
	}

	return recorded
}

func (fetcher *Fetcher) fetchMailbox(
	ctx context.Context,
	session mailboxSession,
	name string,
) error {
	err := fetcher.ensureMailbox(ctx, name)
	if err != nil {
		return fmt.Errorf("mailbox %s: %w", name, err)
	}

	err = fetcher.markStart(ctx, name)
	if err != nil {
		return fmt.Errorf("mailbox %s: %w", name, err)
	}

	count, err := fetcher.syncAndFetch(ctx, session, name)
	if err != nil {
		if isCancel(err) {
			return fmt.Errorf("mailbox %s: %w", name, err)
		}

		err = fmt.Errorf("mailbox %s: %w", name, err)
		markErr := fetcher.markFailure(ctx, name, err)

		return errors.Join(err, markErr)
	}

	slog.InfoContext(ctx, "Fetched mailbox",
		slog.String("mailbox", name),
		slog.Int("messages", count),
	)

	err = fetcher.markSuccess(ctx, name)
	if err != nil {
		return fmt.Errorf("mailbox %s: %w", name, err)
	}

	return nil
}

func (fetcher *Fetcher) syncAndFetch(
	ctx context.Context,
	session mailboxSession,
	name string,
) (int, error) {
	row, err := fetcher.loadMailbox(ctx, name)
	if err != nil {
		return 0, err
	}

	info, err := session.Examine(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("examine mailbox: %w", err)
	}

	next, err := fetcher.syncValidity(ctx, name, row, info.UIDValidity)
	if err != nil {
		return 0, err
	}

	uids, err := session.UIDSearch(ctx, next)
	if err != nil {
		return 0, fmt.Errorf("search mailbox: %w", err)
	}

	count := 0

	for start := 0; start < len(uids); start += uidBatch {
		end := min(start+uidBatch, len(uids))

		var advanced int

		next, advanced, err = fetcher.fetchBatch(ctx, session, name, uids[start:end], next)
		count += advanced

		if err != nil {
			return count, err
		}
	}

	return count, nil
}

func (fetcher *Fetcher) fetchBatch(
	ctx context.Context,
	session mailboxSession,
	name string,
	uids []uint32,
	next uint32,
) (uint32, int, error) {
	meta, err := session.UIDFetch(ctx, uids, sectionMeta)
	if err != nil {
		return next, 0, fmt.Errorf("fetch metadata: %w", err)
	}

	byUID := make(map[uint32]messageData, len(meta))
	for _, item := range meta {
		byUID[item.UID] = item
	}

	advanced := 0

	for _, uid := range uids {
		if uid < next {
			continue
		}

		item, ok := byUID[uid]
		if !ok {
			return next, advanced, fmt.Errorf("fetch UID %d: %w", uid, errUIDMissing)
		}

		err = fetcher.handoff(ctx, session, name, item)
		if err != nil {
			return next, advanced, err
		}

		next = advanceUID(uid)

		err = fetcher.saveNextUID(ctx, name, next)
		if err != nil {
			return next, advanced, err
		}

		advanced++
	}

	return next, advanced, nil
}

func (fetcher *Fetcher) handoff(
	ctx context.Context,
	session mailboxSession,
	name string,
	item messageData,
) error {
	section := sectionFull
	if item.Size > fetcher.cfg.MaxMessageBytes {
		section = sectionHeader
	}

	bodies, err := session.UIDFetch(ctx, []uint32{item.UID}, section)
	if err != nil {
		return fmt.Errorf("fetch body: %w", err)
	}

	var body []byte

	for _, fetched := range bodies {
		if fetched.UID == item.UID {
			body = fetched.Body

			break
		}
	}

	if body == nil {
		return fmt.Errorf("fetch UID %d: %w", item.UID, errBodyMissing)
	}

	src := ingest.Source{Mailbox: name, UID: item.UID, InternalDate: item.InternalDate}

	if section == sectionHeader {
		slog.InfoContext(ctx, "Skipped oversized message",
			slog.String("mailbox", name),
			slog.Uint64("uid", uint64(item.UID)),
			slog.Int64("size", item.Size),
		)

		_, err = fetcher.sink.Skip(ctx, body, item.Size, src)
	} else {
		_, err = fetcher.sink.Ingest(ctx, body, src)
	}

	if err != nil {
		return fmt.Errorf("store UID %d: %w", item.UID, err)
	}

	return nil
}

func advanceUID(uid uint32) uint32 {
	if uid == math.MaxUint32 {
		return uid
	}

	return uid + 1
}

func (fetcher *Fetcher) syncValidity(
	ctx context.Context,
	name string,
	row storedMailbox,
	observed uint32,
) (uint32, error) {
	if row.UIDValidity == observed {
		return normalizeNext(row.NextUID), nil
	}

	if row.UIDValidity != 0 {
		err := fetcher.resetValidity(ctx, name, row.UIDValidity, observed)
		if err != nil {
			return 0, err
		}

		slog.WarnContext(ctx, "UIDVALIDITY reset",
			slog.String("mailbox", name),
			slog.Uint64("old", uint64(row.UIDValidity)),
			slog.Uint64("new", uint64(observed)),
		)

		return 1, nil
	}

	err := fetcher.saveUIDValidity(ctx, name, observed)
	if err != nil {
		return 0, err
	}

	return normalizeNext(row.NextUID), nil
}

func normalizeNext(next uint32) uint32 {
	if next == 0 {
		return 1
	}

	return next
}
