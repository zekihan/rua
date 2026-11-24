package imapsrc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zekihan/rua/internal/store"
)

const stallTimeout = time.Second

type scriptMsg struct {
	header []byte
	body   []byte
	size   int64
	uid    int
}

type scriptSpec struct {
	stall         string
	mode          string
	noText        string
	messages      []scriptMsg
	uidValidity   uint32
	rejectLogin   bool
	rejectExamine bool
}

func TestStalledCommands(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		stall string
		mode  string
		retry bool
	}{
		{name: "accept", stall: "accept", mode: tlsNone, retry: true},
		{name: "starttls", stall: "starttls", mode: tlsStart, retry: true},
		{name: "login", stall: "login", mode: tlsNone, retry: true},
		{name: "examine", stall: "examine", mode: tlsNone, retry: false},
		{name: "search", stall: "search", mode: tlsNone, retry: false},
		{name: "fetch", stall: "fetch", mode: tlsNone, retry: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testStallDeadline(t, tc.stall, tc.mode, tc.retry)
			testStallCancel(t, tc.stall, tc.mode)
		})
	}
}

func testStallDeadline(t *testing.T, stall, mode string, retry bool) {
	t.Helper()

	addr, stalled := listenScript(t, newScript(stall, mode))
	fetcher, database, _ := scriptFetcher(
		t, addr, mode, testUser, testPassword, []string{"Reports"}, 1<<20,
	)
	fetcher.limits = timeLimits{command: stallTimeout, body: stallTimeout}

	var clock *fakeClock
	if retry {
		clock = newFakeClock(time.Unix(1_900_000_000, 0).UTC())
		clock.install(fetcher)
	}

	errCh := make(chan error, 1)
	started := time.Now()

	go func() {
		errCh <- fetcher.FetchOnce(t.Context())
	}()

	waitStalled(t, stalled)

	if retry {
		for _, want := range connectBackoff {
			got := clock.read(t)
			if got != want {
				t.Fatalf("sleep %s, want %s", got, want)
			}

			clock.Advance(got)
		}
	}

	err := waitErrFor(t, errCh, 15*time.Second)
	if time.Since(started) > 15*time.Second || err == nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "timeout during") {
		t.Fatalf("err = %v after %s", err, time.Since(started))
	}

	if fetcher.State().Running {
		t.Fatal("still running")
	}

	row := mailboxOf(t, database, "Reports")
	if row.LastResult != resultError || !strings.Contains(row.LastError, "timeout during") {
		t.Fatalf("mailbox = %+v", row)
	}
}

func testStallCancel(t *testing.T, stall, mode string) {
	t.Helper()

	addr, stalled := listenScript(t, newScript(stall, mode))
	fetcher, database, _ := scriptFetcher(
		t, addr, mode, testUser, testPassword, []string{"Reports"}, 1<<20,
	)
	fetcher.limits = timeLimits{command: time.Hour, body: time.Hour}

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- fetcher.FetchOnce(ctx)
	}()

	waitStalled(t, stalled)
	cancel()

	err := waitErrFor(t, errCh, 3*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	if fetcher.State().Running {
		t.Fatal("still running")
	}

	switch stall {
	case "accept", "starttls", "login":
		if countMailboxes(t, database) != 0 {
			t.Fatal("cancel recorded a mailbox failure")
		}
	default:
		row := mailboxOf(t, database, "Reports")
		if row.LastResult != "" || row.LastError != "" || row.HasSuccess {
			t.Fatalf("mailbox = %+v", row)
		}
	}
}

func TestServerTextDoesNotLeak(t *testing.T) {
	t.Parallel()

	const (
		user   = "leak-user-9f3a"
		phrase = "leak-pass-9f3a"
		mark   = "ECHO-MARKER"
	)

	text := user + " " + phrase + " " + mark

	t.Run("login", func(t *testing.T) {
		t.Parallel()

		spec := new(scriptSpec)
		spec.rejectLogin = true
		spec.noText = text
		assertNoLeak(t, spec, user, phrase, "login rejected", true)
	})

	t.Run("examine", func(t *testing.T) {
		t.Parallel()

		spec := new(scriptSpec)
		spec.rejectExamine = true
		spec.noText = text
		assertNoLeak(t, spec, user, phrase, "mailbox not found: Reports", false)
	})
}

func assertNoLeak(t *testing.T, spec *scriptSpec, user, pass, want string, retry bool) {
	t.Helper()

	addr, _ := listenScript(t, spec)
	fetcher, database, _ := scriptFetcher(t, addr, tlsNone, user, pass, []string{"Reports"}, 1<<20)

	var logs bytes.Buffer

	fetcher.logger = slog.New(slog.NewTextHandler(&logs, nil))

	errCh := make(chan error, 1)

	var clock *fakeClock
	if retry {
		clock = newFakeClock(time.Unix(1_900_000_100, 0).UTC())
		clock.install(fetcher)
	}

	go func() {
		errCh <- fetcher.FetchOnce(t.Context())
	}()

	if retry {
		for _, delay := range connectBackoff {
			got := clock.read(t)
			if got != delay {
				t.Fatalf("sleep %s, want %s", got, delay)
			}

			clock.Advance(got)
		}
	}

	err := waitErrFor(t, errCh, 15*time.Second)

	row := mailboxOf(t, database, "Reports")
	blob := logs.String() + "\n" + row.LastError

	if err != nil {
		blob = err.Error() + "\n" + blob
	}

	for _, secret := range []string{user, pass, "ECHO-MARKER"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("leaked %s in %s", secret, blob)
		}
	}

	if err == nil || !strings.Contains(err.Error(), want) ||
		!strings.Contains(row.LastError, want) {
		t.Fatalf("err = %v mailbox = %+v", err, row)
	}
}

func TestLiteralLimits(t *testing.T) {
	t.Parallel()

	const maxBytes int64 = 1024

	huge := append([]byte("Subject: huge\r\n\r\n"), bytes.Repeat([]byte("x"), 2<<20)...)
	huge = append(huge, []byte("END-MARKER")...)
	lied := append([]byte("Subject: lied\r\n\r\n"), bytes.Repeat([]byte("y"), 2<<20)...)
	lied = append(lied, []byte("END-MARKER")...)

	spec := new(scriptSpec)
	spec.uidValidity = 7
	spec.messages = []scriptMsg{
		{uid: 1, size: 5_000_000, header: huge, body: huge},
		{uid: 2, size: 40, header: lied, body: lied},
	}

	addr, _ := listenScript(t, spec)
	fetcher, database, sink := scriptFetcher(
		t, addr, tlsNone, testUser, testPassword, []string{"Reports"}, maxBytes,
	)

	err := fetcher.FetchOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(sink.ingested) != 0 || len(sink.skipped) != 2 {
		t.Fatalf("ingested %d skipped %d", len(sink.ingested), len(sink.skipped))
	}

	assertTruncatedHeader(t, &sink.skipped[0])
	assertOversizedBody(t, &sink.skipped[1], maxBytes)

	if mailboxOf(t, database, "Reports").NextUID != 3 {
		t.Fatalf("next = %d", mailboxOf(t, database, "Reports").NextUID)
	}
}

func scriptFetcher(
	t *testing.T,
	addr, mode, user, password string,
	mailboxes []string,
	maxBytes int64,
) (*Fetcher, *store.Store, *fakeSink) {
	t.Helper()

	database := openDB(t)
	sink := new(fakeSink)
	fetcher := New(filledConfig(
		addr, mode, "", user, password, mailboxes, time.Hour, maxBytes,
	), database, sink, nil)
	fetcher.jitterN = func() int64 { return 0 }

	return fetcher, database, sink
}

func waitStalled(t *testing.T, stalled <-chan struct{}) {
	t.Helper()

	select {
	case <-stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not reach the stall")
	}
}

func newScript(stall, mode string) *scriptSpec {
	spec := new(scriptSpec)
	spec.stall = stall
	spec.mode = mode

	return spec
}

func assertTruncatedHeader(t *testing.T, call *skipCall) {
	t.Helper()

	if call.src.UID != 1 || call.src.UIDValidity != 7 || call.size != 5_000_000 ||
		len(call.header) != maxHeaderBytes ||
		!bytes.Contains(call.header, []byte("Subject: huge")) ||
		bytes.Contains(call.header, []byte("END-MARKER")) {
		t.Fatalf("header uid %d validity %d size %d len %d",
			call.src.UID, call.src.UIDValidity, call.size, len(call.header))
	}
}

func assertOversizedBody(t *testing.T, call *skipCall, maxBytes int64) {
	t.Helper()

	if call.src.UID != 2 || call.src.UIDValidity != 7 || call.size != maxBytes+1 ||
		!bytes.Contains(call.header, []byte("Subject: lied")) ||
		bytes.Contains(call.header, []byte("END-MARKER")) ||
		len(call.header) >= maxHeaderBytes {
		t.Fatalf("body uid %d size %d len %d", call.src.UID, call.size, len(call.header))
	}
}

func listenScript(t *testing.T, spec *scriptSpec) (string, <-chan struct{}) {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	stalled := make(chan struct{})

	var (
		once  sync.Once
		mu    sync.Mutex
		conns []net.Conn
	)

	signal := func() { once.Do(func() { close(stalled) }) }

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			mu.Lock()

			conns = append(conns, conn)
			mu.Unlock()

			go spec.serve(conn, signal)
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()

		mu.Lock()
		defer mu.Unlock()

		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	return listener.Addr().String(), stalled
}

func (spec *scriptSpec) serve(conn net.Conn, signal func()) {
	defer func() { _ = conn.Close() }()

	if spec.stall == "accept" {
		signalStall(signal)
		hold(conn)

		return
	}

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	greeting := "* OK [CAPABILITY IMAP4rev1] ready"

	if spec.stall == "starttls" {
		greeting = "* OK [CAPABILITY IMAP4rev1 STARTTLS] ready"
	}

	if !writeLine(writer, greeting) {
		return
	}

	if spec.stall == "starttls" {
		spec.stallStartTLS(conn, reader, writer, signal)

		return
	}

	spec.loop(conn, reader, writer, signal)
}

func (spec *scriptSpec) stallStartTLS(
	conn net.Conn,
	reader *bufio.Reader,
	writer *bufio.Writer,
	signal func(),
) {
	for {
		tag, verb, _, err := readCommand(reader)
		if err != nil {
			return
		}

		if verb == "STARTTLS" {
			if !writeLine(writer, tag+" OK Begin TLS negotiation now") {
				return
			}

			signalStall(signal)
			hold(conn)

			return
		}

		if verb == "CAPABILITY" {
			if !writeLine(writer, "* CAPABILITY IMAP4rev1 STARTTLS") ||
				!writeLine(writer, tag+" OK CAPABILITY") {
				return
			}

			continue
		}

		if !writeLine(writer, tag+" BAD") {
			return
		}
	}
}

func (spec *scriptSpec) loop(
	conn net.Conn,
	reader *bufio.Reader,
	writer *bufio.Writer,
	signal func(),
) {
	for {
		tag, verb, args, err := readCommand(reader)
		if err != nil {
			return
		}

		if !spec.command(conn, writer, tag, verb, args, signal) {
			return
		}
	}
}

func (spec *scriptSpec) command(
	conn net.Conn,
	writer *bufio.Writer,
	tag, verb, args string,
	signal func(),
) bool {
	switch verb {
	case "CAPABILITY":
		return writeLine(writer, "* CAPABILITY IMAP4rev1") &&
			writeLine(writer, tag+" OK CAPABILITY")
	case "LOGIN":
		return spec.login(conn, writer, tag, signal)
	case "EXAMINE":
		return spec.examine(conn, writer, tag, signal)
	case "UID":
		return spec.uid(conn, writer, tag, args, signal)
	case "LOGOUT":
		return writeLine(writer, "* BYE") && writeLine(writer, tag+" OK LOGOUT completed")
	default:
		return writeLine(writer, tag+" BAD "+verb)
	}
}

func (spec *scriptSpec) login(conn net.Conn, writer *bufio.Writer, tag string, signal func()) bool {
	if spec.stall == "login" {
		signalStall(signal)
		hold(conn)

		return false
	}

	if spec.rejectLogin {
		return writeLine(writer, tag+" NO "+spec.noText)
	}

	return writeLine(writer, tag+" OK [CAPABILITY IMAP4rev1] logged in")
}

func (spec *scriptSpec) examine(
	conn net.Conn,
	writer *bufio.Writer,
	tag string,
	signal func(),
) bool {
	if spec.stall == "examine" {
		signalStall(signal)
		hold(conn)

		return false
	}

	if spec.rejectExamine {
		return writeLine(writer, tag+" NO [NONEXISTENT] "+spec.noText)
	}

	return spec.writeExamine(writer, tag)
}

func (spec *scriptSpec) uid(
	conn net.Conn,
	writer *bufio.Writer,
	tag, args string,
	signal func(),
) bool {
	switch {
	case strings.HasPrefix(strings.ToUpper(args), "SEARCH"):
		if spec.stall == "search" {
			signalStall(signal)
			hold(conn)

			return false
		}

		return spec.writeSearch(writer, tag)
	case strings.HasPrefix(strings.ToUpper(args), "FETCH"):
		if spec.stall == "fetch" {
			signalStall(signal)
			hold(conn)

			return false
		}

		return spec.writeFetch(writer, tag, args)
	default:
		return writeLine(writer, tag+" BAD UID")
	}
}

func (spec *scriptSpec) writeExamine(writer *bufio.Writer, tag string) bool {
	validity := spec.uidValidity
	if validity == 0 {
		validity = 1
	}

	count := len(spec.messages)
	if count == 0 {
		count = 1
	}

	lines := []string{
		"* " + strconv.Itoa(count) + " EXISTS",
		"* OK [UIDVALIDITY " + strconv.FormatUint(uint64(validity), 10) + "] UIDs valid",
		"* OK [UIDNEXT " + strconv.Itoa(count+1) + "] next",
		tag + " OK [READ-ONLY] EXAMINE completed",
	}

	for _, line := range lines {
		if !writeLine(writer, line) {
			return false
		}
	}

	return true
}

func (spec *scriptSpec) writeSearch(writer *bufio.Writer, tag string) bool {
	var text strings.Builder

	text.WriteString("* SEARCH")

	if len(spec.messages) == 0 {
		text.WriteString(" 1")
	}

	for _, msg := range spec.messages {
		text.WriteByte(' ')
		text.WriteString(strconv.Itoa(msg.uid))
	}

	return writeLine(writer, text.String()) && writeLine(writer, tag+" OK SEARCH completed")
}

func (spec *scriptSpec) writeFetch(writer *bufio.Writer, tag, args string) bool {
	if !strings.Contains(strings.ToUpper(args), "BODY") {
		for _, msg := range spec.messages {
			if !writeLine(writer, metaLine(msg)) {
				return false
			}
		}

		return writeLine(writer, tag+" OK FETCH completed")
	}

	msg, ok := spec.find(fetchUID(args))
	if !ok {
		return writeLine(writer, tag+" OK FETCH completed")
	}

	literal := msg.body
	section := "BODY[]"

	if strings.Contains(strings.ToUpper(args), "HEADER") {
		literal = msg.header
		section = "BODY[HEADER]"
	}

	prefix := "* " + strconv.Itoa(msg.uid) + " FETCH (UID " + strconv.Itoa(msg.uid) + " " +
		section + " {" + strconv.Itoa(len(literal)) + "}"
	if !writeLine(writer, prefix) {
		return false
	}

	_, err := writer.Write(literal)
	if err != nil {
		return false
	}

	return writeLine(writer, ")") && writeLine(writer, tag+" OK FETCH completed")
}

func (spec *scriptSpec) find(uid int) (scriptMsg, bool) {
	for _, msg := range spec.messages {
		if msg.uid == uid {
			return msg, true
		}
	}

	var missing scriptMsg

	return missing, false
}

func metaLine(msg scriptMsg) string {
	return "* " + strconv.Itoa(msg.uid) + " FETCH (UID " + strconv.Itoa(msg.uid) +
		` INTERNALDATE " 2-Jan-2024 00:00:00 +0000" RFC822.SIZE ` +
		strconv.FormatInt(msg.size, 10) + ")"
}

func fetchUID(args string) int {
	fields := strings.Fields(args)
	set := ""

	for index, field := range fields {
		if strings.EqualFold(field, "FETCH") && index+1 < len(fields) {
			set = fields[index+1]

			break
		}
	}

	number := 0

	for _, char := range set {
		if char < '0' || char > '9' {
			break
		}

		number = number*10 + int(char-'0')
	}

	return number
}

func readCommand(reader *bufio.Reader) (string, string, string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", "", "", fmt.Errorf("read command: %w", err)
	}

	line = strings.TrimRight(line, "\r\n")
	tag, rest, _ := strings.Cut(line, " ")
	verb, args, _ := strings.Cut(rest, " ")

	return tag, strings.ToUpper(verb), args, nil
}

func writeLine(writer *bufio.Writer, line string) bool {
	_, err := writer.WriteString(line + "\r\n")
	if err != nil {
		return false
	}

	return writer.Flush() == nil
}

func signalStall(signal func()) {
	if signal != nil {
		signal()
	}
}

func hold(conn net.Conn) {
	_, _ = io.Copy(io.Discard, conn)
}
