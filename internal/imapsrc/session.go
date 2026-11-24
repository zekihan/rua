package imapsrc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const uidBatch = 50

var errFetchSection = errors.New("unknown fetch section")

// mailboxSession is the only IMAP surface used after login. Dial and login
// happen in dialIMAP; nothing else in this package can change flags, folders
// or messages through this interface.
type mailboxSession interface {
	Examine(ctx context.Context, mailbox string) (examined, error)
	UIDSearch(ctx context.Context, from uint32) ([]uint32, error)
	UIDFetch(ctx context.Context, uids []uint32, section fetchSection) ([]messageData, error)
	Logout(ctx context.Context) error
}

type examined struct {
	UIDValidity uint32
}

type fetchSection int

const (
	sectionMeta fetchSection = iota
	sectionFull
	sectionHeader
)

type messageData struct {
	InternalDate time.Time
	Body         []byte
	Size         int64
	UID          uint32
}

// imapSession wraps *imapclient.Client. Fetch code holds it as a
// mailboxSession, whose methods are the only commands issued here.
type imapSession struct {
	raw *imapclient.Client
}

func (session *imapSession) Examine(ctx context.Context, mailbox string) (examined, error) {
	var data *imap.SelectData

	options := new(imap.SelectOptions)
	options.ReadOnly = true

	err := session.call(ctx, func() error {
		var waitErr error

		data, waitErr = session.raw.Select(mailbox, options).Wait()
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}

		return nil
	})
	if err != nil {
		return examined{UIDValidity: 0}, fmt.Errorf("examine %s: %w", mailbox, err)
	}

	return examined{UIDValidity: data.UIDValidity}, nil
}

func (session *imapSession) UIDSearch(ctx context.Context, from uint32) ([]uint32, error) {
	if from == 0 {
		from = 1
	}

	var set imap.UIDSet
	set.AddRange(imap.UID(from), 0)

	var data *imap.SearchData

	criteria := new(imap.SearchCriteria)
	criteria.UID = []imap.UIDSet{set}

	err := session.call(ctx, func() error {
		var waitErr error

		data, waitErr = session.raw.UIDSearch(criteria, nil).Wait()
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search UIDs: %w", err)
	}

	found := make([]uint32, 0, len(data.AllUIDs()))
	for _, uid := range data.AllUIDs() {
		// UID n:* includes the highest UID even when n is past the end.
		if uid == 0 || uint32(uid) < from {
			continue
		}

		found = append(found, uint32(uid))
	}

	slices.Sort(found)

	return slices.Compact(found), nil
}

func (session *imapSession) UIDFetch(
	ctx context.Context,
	uids []uint32,
	section fetchSection,
) ([]messageData, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	nums := make([]imap.UID, len(uids))
	for index, uid := range uids {
		nums[index] = imap.UID(uid)
	}

	opts := new(imap.FetchOptions)
	opts.UID = true

	var body *imap.FetchItemBodySection

	switch section {
	case sectionMeta:
		opts.InternalDate = true
		opts.RFC822Size = true
	case sectionFull:
		body = new(imap.FetchItemBodySection)
		body.Peek = true
		opts.BodySection = []*imap.FetchItemBodySection{body}
	case sectionHeader:
		body = new(imap.FetchItemBodySection)
		body.Specifier = imap.PartSpecifierHeader
		body.Peek = true
		opts.BodySection = []*imap.FetchItemBodySection{body}
	default:
		return nil, fmt.Errorf("%w: %d", errFetchSection, section)
	}

	var collected []*imapclient.FetchMessageBuffer

	err := session.call(ctx, func() error {
		var fetchErr error

		collected, fetchErr = session.raw.Fetch(imap.UIDSetNum(nums...), opts).Collect()
		if fetchErr != nil {
			return fmt.Errorf("wait: %w", fetchErr)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetch UIDs: %w", err)
	}

	out := make([]messageData, 0, len(collected))
	for _, buf := range collected {
		item := messageData{
			InternalDate: buf.InternalDate,
			Body:         nil,
			Size:         buf.RFC822Size,
			UID:          uint32(buf.UID),
		}
		if body != nil {
			item.Body = buf.FindBodySection(body)
		}

		out = append(out, item)
	}

	return out, nil
}

func (session *imapSession) Logout(ctx context.Context) error {
	err := session.call(ctx, func() error {
		waitErr := session.raw.Logout().Wait()
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}

		return nil
	})

	closeErr := session.raw.Close()
	if err != nil {
		if closeErr != nil && !benignClose(closeErr) {
			err = errors.Join(err, closeErr)
		}

		return fmt.Errorf("logout: %w", err)
	}

	if closeErr != nil && !benignClose(closeErr) {
		return fmt.Errorf("close IMAP connection: %w", closeErr)
	}

	return nil
}

func (session *imapSession) call(ctx context.Context, fn func() error) error {
	err := ctx.Err()
	if err != nil {
		return contextError(err)
	}

	stop := context.AfterFunc(ctx, func() {
		closeErr := session.raw.Close()
		if closeErr != nil && !benignClose(closeErr) {
			slog.DebugContext(ctx, "Close IMAP connection", slog.Any("error", closeErr))
		}
	})

	defer stop()

	err = fn()

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return contextError(ctxErr)
	}

	return err
}

func benignClose(err error) bool {
	return err == nil ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe)
}
