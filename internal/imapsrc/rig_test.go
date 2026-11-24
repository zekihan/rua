package imapsrc

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/store"
)

const (
	testUser     = "rua"
	testPassword = "test-password"
)

var (
	errMoveUnsupported = errors.New("move is not supported")
	errSinkFailed      = errors.New("sink failed")
)

var forbiddenCommands = map[string]struct{}{
	"SELECT":      {},
	"STORE":       {},
	"COPY":        {},
	"MOVE":        {},
	"EXPUNGE":     {},
	"APPEND":      {},
	"CREATE":      {},
	"DELETE":      {},
	"RENAME":      {},
	"SUBSCRIBE":   {},
	"NOT-PEEK":    {},
	"UNSUBSCRIBE": {},
}

type rig struct {
	listener net.Listener
	user     *imapmemserver.User
	database *store.Store
	sink     *fakeSink
	fetcher  *Fetcher
	recorder *recorder
	server   *imapserver.Server
	addr     string
}

type serverSpec struct {
	config *tls.Config
	mode   string
	caFile string
}

func newRig(t *testing.T, mailboxes []string) *rig {
	t.Helper()

	return newServerRig(t, mailboxes, serverSpec{mode: tlsNone, caFile: "", config: nil})
}

func newServerRig(t *testing.T, mailboxes []string, spec serverSpec) *rig {
	t.Helper()

	memory := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPassword)
	memory.AddUser(user)

	recorder := new(recorder)
	logger := new(lineLogger)

	serverTLS := spec.config
	if spec.mode != tlsStart {
		serverTLS = nil
	}

	options := new(imapserver.Options)
	options.NewSession = func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return &recordingSession{Session: memory.NewSession(), recorder: recorder}, nil, nil
	}
	options.InsecureAuth = spec.mode == tlsNone
	options.TLSConfig = serverTLS
	options.Logger = logger

	server := imapserver.New(options)

	var (
		listener net.Listener
		err      error
	)

	if spec.mode == tlsImplicit {
		listener, err = tls.Listen("tcp", "127.0.0.1:0", spec.config)
	} else {
		listener, err = new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	}

	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = server.Serve(listener) }()

	database, err := store.Open(t.Context(), store.Memory)
	if err != nil {
		t.Fatal(err)
	}

	sink := new(fakeSink)
	fetcher := New(Config{
		Addr:            listener.Addr().String(),
		TLSMode:         spec.mode,
		CAFile:          spec.caFile,
		Username:        testUser,
		Password:        testPassword,
		Mailboxes:       mailboxes,
		Interval:        time.Hour,
		MaxMessageBytes: 25 << 20,
	}, database, sink, nil)
	fetcher.jitterN = func() int64 { return 0 }

	built := &rig{
		user:     user,
		database: database,
		sink:     sink,
		fetcher:  fetcher,
		recorder: recorder,
		addr:     listener.Addr().String(),
		listener: listener,
		server:   server,
	}

	t.Cleanup(func() {
		_ = listener.Close()
		_ = server.Close()
		_ = database.Close()

		if t.Failed() {
			for _, line := range logger.lines() {
				t.Log(line)
			}
		}
	})

	return built
}

func (built *rig) create(t *testing.T, name string) {
	t.Helper()

	err := built.user.Create(name, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func messageRaw(subject, body string) []byte {
	return []byte("From: reports@example.com\r\nSubject: " + subject + "\r\n\r\n" + body + "\r\n")
}

func (built *rig) deliver(t *testing.T, mailbox, subject, body string, when time.Time) {
	t.Helper()

	appendOptions := new(imap.AppendOptions)
	appendOptions.Time = when

	_, err := built.user.Append(
		mailbox,
		newLiteral(messageRaw(subject, body)),
		appendOptions,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func (built *rig) fetch(t *testing.T) error {
	t.Helper()

	return built.fetcher.FetchOnce(t.Context())
}

type mailboxRow struct {
	LastError   string
	LastResult  string
	UIDValidity uint32
	NextUID     uint32
	LastStart   int64
	LastEnd     int64
	LastSuccess int64
	HasSuccess  bool
}

func (built *rig) mailbox(t *testing.T, name string) mailboxRow {
	t.Helper()

	var row struct {
		LastError   string        `db:"last_error"`
		LastResult  string        `db:"last_result"`
		UIDValidity int64         `db:"uidvalidity"`
		NextUID     int64         `db:"next_uid"`
		LastStart   sql.NullInt64 `db:"last_fetch_start"`
		LastEnd     sql.NullInt64 `db:"last_fetch_end"`
		LastSuccess sql.NullInt64 `db:"last_success"`
	}

	err := built.database.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &row, `
			SELECT uidvalidity, next_uid, last_result, last_error,
				last_fetch_start, last_fetch_end, last_success
			FROM mailboxes WHERE name = ?`, name)
	})
	if err != nil {
		t.Fatal(err)
	}

	validity, err := uint32From(row.UIDValidity)
	if err != nil {
		t.Fatal(err)
	}

	next, err := uint32From(row.NextUID)
	if err != nil {
		t.Fatal(err)
	}

	return mailboxRow{
		LastError:   row.LastError,
		LastResult:  row.LastResult,
		UIDValidity: validity,
		NextUID:     next,
		LastStart:   row.LastStart.Int64,
		LastEnd:     row.LastEnd.Int64,
		LastSuccess: row.LastSuccess.Int64,
		HasSuccess:  row.LastSuccess.Valid,
	}
}

func (built *rig) events(t *testing.T) []eventRow {
	t.Helper()

	var rows []eventRow

	err := built.database.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT kind, mailbox, detail FROM events ORDER BY id`)
	})
	if err != nil {
		t.Fatal(err)
	}

	return rows
}

type eventRow struct {
	Kind    string `db:"kind"`
	Mailbox string `db:"mailbox"`
	Detail  string `db:"detail"`
}

func (built *rig) assertReadOnly(t *testing.T, names ...string) {
	t.Helper()

	commands := built.recorder.snapshot()
	for _, command := range commands {
		if _, forbidden := forbiddenCommands[command]; forbidden {
			t.Errorf("server received %s in %v", command, commands)
		}
	}

	if !slices.Contains(commands, "EXAMINE") {
		t.Errorf("commands = %v, want EXAMINE", commands)
	}

	secret := built.fetcher.cfg.Password
	if secret != "" {
		for _, command := range commands {
			if strings.Contains(command, secret) {
				t.Errorf("command recorded the password: %s", command)
			}
		}
	}

	for _, name := range names {
		statusOptions := new(imap.StatusOptions)
		statusOptions.NumMessages = true
		statusOptions.NumUnseen = true

		data, err := built.user.Status(name, statusOptions)
		if err != nil {
			t.Fatal(err)
		}

		if data.NumMessages == nil || data.NumUnseen == nil ||
			*data.NumMessages != *data.NumUnseen {
			t.Errorf("mailbox %s set \\Seen: %+v", name, data)
		}
	}
}

type recorder struct {
	commands []string
	mu       sync.Mutex
}

func (recorder *recorder) add(command string) {
	recorder.mu.Lock()
	recorder.commands = append(recorder.commands, command)
	recorder.mu.Unlock()
}

func (recorder *recorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	out := make([]string, len(recorder.commands))
	copy(out, recorder.commands)

	return out
}

func (recorder *recorder) count(command string) int {
	total := 0

	for _, item := range recorder.snapshot() {
		if item == command {
			total++
		}
	}

	return total
}

type recordingSession struct {
	imapserver.Session

	recorder *recorder
}

func forward(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("imap session: %w", err)
}

func (session *recordingSession) Login(username, password string) error {
	session.recorder.add("LOGIN")

	return forward(session.Session.Login(username, password))
}

func (session *recordingSession) Select(
	mailbox string,
	opts *imap.SelectOptions,
) (*imap.SelectData, error) {
	if opts != nil && opts.ReadOnly {
		session.recorder.add("EXAMINE")
	} else {
		session.recorder.add("SELECT")
	}

	data, err := session.Session.Select(mailbox, opts)

	return data, forward(err)
}

func (session *recordingSession) Create(mailbox string, opts *imap.CreateOptions) error {
	session.recorder.add("CREATE")

	return forward(session.Session.Create(mailbox, opts))
}

func (session *recordingSession) Delete(mailbox string) error {
	session.recorder.add("DELETE")

	return forward(session.Session.Delete(mailbox))
}

func (session *recordingSession) Rename(mailbox, newName string, opts *imap.RenameOptions) error {
	session.recorder.add("RENAME")

	return forward(session.Session.Rename(mailbox, newName, opts))
}

func (session *recordingSession) Subscribe(mailbox string) error {
	session.recorder.add("SUBSCRIBE")

	return forward(session.Session.Subscribe(mailbox))
}

func (session *recordingSession) Unsubscribe(mailbox string) error {
	session.recorder.add("UNSUBSCRIBE")

	return forward(session.Session.Unsubscribe(mailbox))
}

func (session *recordingSession) Append(
	mailbox string,
	reader imap.LiteralReader,
	opts *imap.AppendOptions,
) (*imap.AppendData, error) {
	session.recorder.add("APPEND")

	data, err := session.Session.Append(mailbox, reader, opts)

	return data, forward(err)
}

func (session *recordingSession) Search(
	kind imapserver.NumKind,
	criteria *imap.SearchCriteria,
	opts *imap.SearchOptions,
) (*imap.SearchData, error) {
	if kind == imapserver.NumKindUID {
		session.recorder.add("UID SEARCH")
	} else {
		session.recorder.add("SEARCH")
	}

	data, err := session.Session.Search(kind, criteria, opts)

	return data, forward(err)
}

func (session *recordingSession) Fetch(
	writer *imapserver.FetchWriter,
	nums imap.NumSet,
	opts *imap.FetchOptions,
) error {
	session.recorder.add("FETCH")

	if numCount(nums) == uidBatch {
		session.recorder.add("BATCH")
	}

	if opts != nil {
		for _, section := range opts.BodySection {
			if section != nil && !section.Peek {
				session.recorder.add("NOT-PEEK")
			}
		}
	}

	return forward(session.Session.Fetch(writer, nums, opts))
}

func numCount(nums imap.NumSet) int {
	set, ok := nums.(imap.UIDSet)
	if !ok {
		return 0
	}

	ids, ok := set.Nums()
	if !ok {
		return 0
	}

	return len(ids)
}

func (session *recordingSession) Store(
	writer *imapserver.FetchWriter,
	nums imap.NumSet,
	flags *imap.StoreFlags,
	opts *imap.StoreOptions,
) error {
	session.recorder.add("STORE")

	return forward(session.Session.Store(writer, nums, flags, opts))
}

func (session *recordingSession) Copy(nums imap.NumSet, dest string) (*imap.CopyData, error) {
	session.recorder.add("COPY")

	data, err := session.Session.Copy(nums, dest)

	return data, forward(err)
}

func (session *recordingSession) Expunge(
	writer *imapserver.ExpungeWriter,
	uids *imap.UIDSet,
) error {
	session.recorder.add("EXPUNGE")

	return forward(session.Session.Expunge(writer, uids))
}

func (session *recordingSession) Move(
	writer *imapserver.MoveWriter,
	nums imap.NumSet,
	dest string,
) error {
	session.recorder.add("MOVE")

	mover, ok := session.Session.(imapserver.SessionMove)
	if !ok {
		return errMoveUnsupported
	}

	return forward(mover.Move(writer, nums, dest))
}

type lineLogger struct {
	saved []string
	mu    sync.Mutex
}

func (logger *lineLogger) Printf(format string, args ...any) {
	logger.mu.Lock()
	logger.saved = append(logger.saved, fmt.Sprintf(format, args...))
	logger.mu.Unlock()
}

func (logger *lineLogger) lines() []string {
	logger.mu.Lock()
	defer logger.mu.Unlock()

	out := make([]string, len(logger.saved))
	copy(out, logger.saved)

	return out
}

type ingestCall struct {
	src ingest.Source
	raw []byte
}

type skipCall struct {
	src    ingest.Source
	header []byte
	size   int64
}

type fakeSink struct {
	failErr    error
	block      <-chan struct{}
	started    chan struct{}
	ingested   []ingestCall
	skipped    []skipCall
	failOn     int
	calls      int
	current    int
	maxCurrent int
	startOnce  sync.Once
	mu         sync.Mutex
	failAll    bool
}

func (sink *fakeSink) Ingest(
	ctx context.Context,
	raw []byte,
	src ingest.Source,
) (ingest.Result, error) {
	err := sink.enter(ctx)
	if err != nil {
		return noneResult(), err
	}

	sink.mu.Lock()
	sink.ingested = append(sink.ingested, ingestCall{raw: append([]byte(nil), raw...), src: src})
	sink.current--
	sink.mu.Unlock()

	return noneResult(), nil
}

func (sink *fakeSink) Skip(
	ctx context.Context,
	header []byte,
	size int64,
	src ingest.Source,
) (ingest.Result, error) {
	err := sink.enter(ctx)
	if err != nil {
		return noneResult(), err
	}

	sink.mu.Lock()
	sink.skipped = append(sink.skipped, skipCall{
		header: append([]byte(nil), header...),
		size:   size,
		src:    src,
	})
	sink.current--
	sink.mu.Unlock()

	return noneResult(), nil
}

func (sink *fakeSink) setFailAll(fail bool) {
	sink.mu.Lock()
	sink.failAll = fail
	sink.mu.Unlock()
}

func noneResult() ingest.Result {
	var result ingest.Result

	return result
}

func (sink *fakeSink) enter(ctx context.Context) error {
	sink.mu.Lock()
	sink.current++

	if sink.current > sink.maxCurrent {
		sink.maxCurrent = sink.current
	}

	sink.calls++
	call := sink.calls
	fail := sink.failAll || (sink.failOn > 0 && call == sink.failOn)
	failErr := sink.failErr
	block := sink.block
	started := sink.started
	sink.mu.Unlock()

	if started != nil {
		sink.startOnce.Do(func() { close(started) })
	}

	if fail {
		sink.mu.Lock()
		sink.current--
		sink.mu.Unlock()

		if failErr == nil {
			failErr = errSinkFailed
		}

		return failErr
	}

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			sink.mu.Lock()
			sink.current--
			sink.mu.Unlock()

			return contextError(ctx.Err())
		}
	}

	return nil
}

func (sink *fakeSink) peak() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()

	return sink.maxCurrent
}

func (sink *fakeSink) uids() []uint32 {
	sink.mu.Lock()
	defer sink.mu.Unlock()

	uids := make([]uint32, len(sink.ingested))
	for index, call := range sink.ingested {
		uids[index] = call.src.UID
	}

	return uids
}

type bytesLiteral struct {
	reader *bytes.Reader
	size   int64
}

func newLiteral(raw []byte) bytesLiteral {
	return bytesLiteral{reader: bytes.NewReader(raw), size: int64(len(raw))}
}

func (literal bytesLiteral) Read(buf []byte) (int, error) {
	count, err := literal.reader.Read(buf)
	if err != nil {
		//nolint:wrapcheck // io.Reader must return io.EOF unchanged.
		return count, err
	}

	return count, nil
}

func (literal bytesLiteral) Size() int64 { return literal.size }
