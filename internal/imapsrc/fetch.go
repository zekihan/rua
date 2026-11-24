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
	errUIDMissing         = errors.New("missing from response")
	errBodyMissing        = errors.New("missing body")
	errUIDValidityChanged = errors.New("uidvalidity changed")
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
			fetcher.log().WarnContext(ctx, "IMAP logout failed",
				slog.String("error", clipError(logoutErr, fetcher.secrets()...)),
			)
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

	return fetcher.safe(errors.Join(errs...))
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

	if fetcher.afterLoad != nil {
		fetcher.afterLoad(name)
	}

	info, err := session.Examine(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("examine: %w", err)
	}

	next, err := fetcher.syncValidity(ctx, name, row, info.UIDValidity)
	if err != nil {
		return 0, err
	}

	uids, err := session.UIDSearch(ctx, next)
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}

	err = fetcher.setPending(ctx, name, int64(len(uids)))
	if err != nil {
		return 0, err
	}

	count := 0

	for start := 0; start < len(uids); start += uidBatch {
		end := min(start+uidBatch, len(uids))

		var advanced int

		next, advanced, err = fetcher.fetchBatch(
			ctx, session, name, uids[start:end], next, info.UIDValidity, len(uids)-count,
		)
		count += advanced

		if err != nil {
			pendingErr := fetcher.setPending(ctx, name, int64(len(uids)-count))

			return count, errors.Join(err, pendingErr)
		}
	}

	err = fetcher.setPending(ctx, name, 0)
	if err != nil {
		return count, err
	}

	return count, nil
}

func (fetcher *Fetcher) fetchBatch(
	ctx context.Context,
	session mailboxSession,
	name string,
	uids []uint32,
	next, validity uint32,
	remaining int,
) (uint32, int, error) {
	meta, err := session.UIDFetch(ctx, uids, sectionMeta, 0)
	if err != nil {
		return next, 0, fmt.Errorf("fetch: %w", err)
	}

	byUID := make(map[uint32]messageData, len(meta))
	for _, item := range meta {
		byUID[item.UID] = item
	}

	advanced := 0

	// Metadata is read uidBatch UIDs at a time. Each body is fetched on its
	// own so memory stays bounded by one message, or by a truncated header
	// when the message is oversized, instead of the whole batch.
	for _, uid := range uids {
		if uid < next {
			remaining--

			err = fetcher.setPending(ctx, name, int64(remaining))
			if err != nil {
				return next, advanced, err
			}

			continue
		}

		item, ok := byUID[uid]
		if !ok {
			return next, advanced, fmt.Errorf("fetch UID %d: %w", uid, errUIDMissing)
		}

		err = fetcher.handoff(ctx, session, name, validity, item)
		if err != nil {
			return next, advanced, err
		}

		next = advanceUID(uid)

		err = fetcher.saveNextUID(ctx, name, validity, next)
		if err != nil {
			return next, advanced, err
		}

		advanced++
		remaining--

		err = fetcher.setPending(ctx, name, int64(remaining))
		if err != nil {
			return next, advanced, err
		}
	}

	return next, advanced, nil
}

func (fetcher *Fetcher) handoff(
	ctx context.Context,
	session mailboxSession,
	name string,
	validity uint32,
	item messageData,
) error {
	section := sectionFull
	if item.Size > fetcher.cfg.MaxMessageBytes {
		section = sectionHeader
	}

	bodies, err := session.UIDFetch(
		ctx,
		[]uint32{item.UID},
		section,
		literalKeep(section, item.Size, fetcher.cfg.MaxMessageBytes),
	)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	var fetched messageData

	found := false

	for _, candidate := range bodies {
		if candidate.UID == item.UID {
			fetched = candidate
			found = true

			break
		}
	}

	if !found || !fetched.HaveBody {
		return fmt.Errorf("fetch UID %d: %w", item.UID, errBodyMissing)
	}

	src := ingest.Source{
		Mailbox:      name,
		UID:          item.UID,
		UIDValidity:  validity,
		InternalDate: item.InternalDate,
	}

	if section == sectionHeader || fetched.Oversized {
		size := item.Size
		if fetched.Oversized {
			size = oversizedSize(item.Size, fetcher.cfg.MaxMessageBytes)
		}

		fetcher.log().InfoContext(ctx, "Skipped oversized message",
			slog.String("mailbox", name),
			slog.Uint64("uid", uint64(item.UID)),
			slog.Int64("size", size),
		)

		_, err = fetcher.sink.Skip(ctx, fetched.Body, size, src)
	} else {
		_, err = fetcher.sink.Ingest(ctx, fetched.Body, src)
	}

	if err != nil {
		return fmt.Errorf("store UID %d: %w", item.UID, err)
	}

	return nil
}

func oversizedSize(claimed, maxBytes int64) int64 {
	floor := maxBytes + 1
	if claimed > floor {
		return claimed
	}

	return floor
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
		applied, resetErr := fetcher.resetValidity(ctx, name, row.UIDValidity, observed)
		if resetErr != nil {
			return 0, resetErr
		}

		if !applied {
			return fetcher.reloadNext(ctx, name, observed)
		}

		fetcher.log().WarnContext(ctx, "UIDVALIDITY reset",
			slog.String("mailbox", name),
			slog.Uint64("old", uint64(row.UIDValidity)),
			slog.Uint64("new", uint64(observed)),
		)

		return 1, nil
	}

	applied, err := fetcher.saveUIDValidity(ctx, name, observed)
	if err != nil {
		return 0, err
	}

	if !applied {
		return fetcher.reloadNext(ctx, name, observed)
	}

	return normalizeNext(row.NextUID), nil
}

func (fetcher *Fetcher) reloadNext(
	ctx context.Context,
	name string,
	observed uint32,
) (uint32, error) {
	fresh, err := fetcher.loadMailbox(ctx, name)
	if err != nil {
		return 0, err
	}

	if fresh.UIDValidity != observed {
		return 0, fmt.Errorf("mailbox %s: %w", name, errUIDValidityChanged)
	}

	return normalizeNext(fresh.NextUID), nil
}

func normalizeNext(next uint32) uint32 {
	if next == 0 {
		return 1
	}

	return next
}
