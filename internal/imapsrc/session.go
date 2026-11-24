package imapsrc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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
	UIDFetch(
		ctx context.Context,
		uids []uint32,
		section fetchSection,
		keep int64,
	) ([]messageData, error)
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
	HaveBody     bool
	// Oversized is set when a full-body literal exceeded the claimed size or
	// MaxMessageBytes. Body then holds only the header prefix.
	Oversized bool
}

// imapSession wraps *imapclient.Client. Fetch code holds it as a
// mailboxSession, whose methods are the only commands issued here.
type imapSession struct {
	raw      *imapclient.Client
	conn     net.Conn
	stop     func() bool
	limits   timeLimits
	maxBytes int64
}

func (session *imapSession) Examine(ctx context.Context, mailbox string) (examined, error) {
	var data *imap.SelectData

	err := session.run(ctx, stageExamine, mailbox, session.limits.commandFor(), func() error {
		options := new(imap.SelectOptions)
		options.ReadOnly = true

		var waitErr error

		data, waitErr = session.raw.Select(mailbox, options).Wait()
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}

		return nil
	})
	if err != nil {
		return examined{UIDValidity: 0}, err
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

	err := session.run(ctx, stageSearch, "", session.limits.commandFor(), func() error {
		var waitErr error

		data, waitErr = session.raw.UIDSearch(criteria, nil).Wait()
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}

		return nil
	})
	if err != nil {
		return nil, err
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
	keep int64,
) ([]messageData, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	nums := make([]imap.UID, len(uids))
	for index, uid := range uids {
		nums[index] = imap.UID(uid)
	}

	opts, body, err := fetchOptions(section)
	if err != nil {
		return nil, err
	}

	var out []messageData

	err = session.run(ctx, stageFetch, "", session.limitFor(section), func() error {
		return collectFetch(session.raw.Fetch(imap.UIDSetNum(nums...), opts), body, keep, &out)
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

func fetchOptions(section fetchSection) (*imap.FetchOptions, *imap.FetchItemBodySection, error) {
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
		return nil, nil, fmt.Errorf("%w: %d", errFetchSection, section)
	}

	return opts, body, nil
}

func collectFetch(
	cmd *imapclient.FetchCommand,
	body *imap.FetchItemBodySection,
	keep int64,
	out *[]messageData,
) (err error) {
	defer func() {
		closeErr := cmd.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("wait: %w", closeErr)
		}
	}()

	for {
		msg := cmd.Next()
		if msg == nil {
			return nil
		}

		item, readErr := readMessage(msg, body, keep)
		if readErr != nil {
			return readErr
		}

		*out = append(*out, item)
	}
}

func readMessage(
	msg *imapclient.FetchMessageData,
	body *imap.FetchItemBodySection,
	keep int64,
) (messageData, error) {
	var item messageData

	for {
		field := msg.Next()
		if field == nil {
			return item, nil
		}

		err := applyFetchItem(&item, field, body, keep)
		if err != nil {
			return messageData{}, err
		}
	}
}

func applyFetchItem(
	item *messageData,
	field imapclient.FetchItemData,
	body *imap.FetchItemBodySection,
	keep int64,
) error {
	switch data := field.(type) {
	case imapclient.FetchItemDataUID:
		item.UID = uint32(data.UID)
	case imapclient.FetchItemDataInternalDate:
		item.InternalDate = data.Time
	case imapclient.FetchItemDataRFC822Size:
		item.Size = data.Size
	case imapclient.FetchItemDataBodySection:
		return readBodySection(item, data, body, keep)
	case imapclient.FetchItemDataBinarySection:
		return discardLiteral(data.Literal)
	default:
		return nil
	}

	return nil
}

func readBodySection(
	item *messageData,
	data imapclient.FetchItemDataBodySection,
	body *imap.FetchItemBodySection,
	keep int64,
) error {
	if body != nil && !data.MatchCommand(body) {
		return discardLiteral(data.Literal)
	}

	payload, overflow, err := readLiteral(data.Literal, keep)
	if err != nil {
		return err
	}

	item.HaveBody = true

	item.Oversized = overflow && (body == nil || body.Specifier != imap.PartSpecifierHeader)
	if item.Oversized {
		item.Body = headerPrefix(payload)

		return nil
	}

	item.Body = payload

	return nil
}

func discardLiteral(reader imap.LiteralReader) error {
	if reader == nil {
		return nil
	}

	_, err := io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("discard FETCH literal: %w", err)
	}

	return nil
}

// readLiteral retains at most keep bytes and drains the rest. One extra byte
// is read so a literal longer than keep is reported as overflow without
// holding it.
func readLiteral(reader imap.LiteralReader, keep int64) ([]byte, bool, error) {
	if reader == nil {
		return []byte{}, false, nil
	}

	if keep < 0 {
		keep = 0
	}

	limit := keep
	if keep < math.MaxInt64 {
		limit = keep + 1
	}

	payload, err := io.ReadAll(io.LimitReader(reader, limit))
	if err != nil {
		return nil, false, fmt.Errorf("read FETCH literal: %w", err)
	}

	if int64(len(payload)) <= keep {
		return payload, false, nil
	}

	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return nil, true, fmt.Errorf("discard FETCH literal: %w", err)
	}

	return payload[:keep], true, nil
}

func headerPrefix(raw []byte) []byte {
	if len(raw) > maxHeaderBytes {
		raw = raw[:maxHeaderBytes]
	}

	separator := []byte("\r\n\r\n")
	end := len(raw)

	index := bytes.Index(raw, separator)
	if index >= 0 {
		end = index + len(separator)
	}

	// Copy so the retained header does not pin a large literal buffer.
	out := make([]byte, end)
	copy(out, raw[:end])

	return out
}

func (session *imapSession) Logout(ctx context.Context) error {
	defer session.closeWatch()

	err := session.run(ctx, stageLogout, "", session.limits.commandFor(), func() error {
		waitErr := session.raw.Logout().Wait()
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}

		return nil
	})

	closeErr := session.raw.Close()

	if err != nil {
		return err
	}

	if closeErr != nil && !benignClose(closeErr) {
		return redactedError(textConnectionClosed)
	}

	return nil
}

func (session *imapSession) limitFor(section fetchSection) time.Duration {
	if section == sectionMeta {
		return session.limits.commandFor()
	}

	return session.limits.bodyFor(session.maxBytes)
}

func literalKeep(section fetchSection, claimed, maxBytes int64) int64 {
	switch section {
	case sectionHeader:
		return maxHeaderBytes
	case sectionFull:
		if claimed < 0 {
			claimed = 0
		}

		if maxBytes < 0 {
			maxBytes = 0
		}

		if claimed > maxBytes {
			return maxBytes
		}

		return claimed
	case sectionMeta:
		return 0
	default:
		return 0
	}
}

func (session *imapSession) closeWatch() {
	if session.stop != nil {
		session.stop()
		session.stop = nil
	}
}

func (session *imapSession) run(
	ctx context.Context,
	stage, mailbox string,
	limit time.Duration,
	fn func() error,
) error {
	return runStage(ctx, session.conn, limit, stage, mailbox, fn)
}
