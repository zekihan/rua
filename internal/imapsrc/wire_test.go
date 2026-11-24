package imapsrc

import (
	"bytes"
	"slices"
	"strconv"
	"sync"
	"testing"
)

// wireTap records client command verbs from the server's plaintext debug
// stream. IMAP literals are skipped so a message body cannot look like CLOSE.
// Session.Close is recorded separately as TRANSPORT-CLOSE.
type wireTap struct {
	recorder *recorder
	buf      []byte
	literal  int
	mu       sync.Mutex
}

func (tap *wireTap) Write(data []byte) (int, error) {
	tap.mu.Lock()
	defer tap.mu.Unlock()

	tap.buf = append(tap.buf, data...)
	tap.drain()

	return len(data), nil
}

func (tap *wireTap) drain() {
	for {
		if tap.literal > 0 {
			if len(tap.buf) < tap.literal {
				tap.literal -= len(tap.buf)
				tap.buf = nil

				return
			}

			tap.buf = tap.buf[tap.literal:]
			tap.literal = 0

			continue
		}

		end := bytes.Index(tap.buf, []byte("\r\n"))
		if end < 0 {
			return
		}

		line := tap.buf[:end]
		tap.buf = tap.buf[end+2:]
		tap.record(line)

		size, ok := literalSize(line)
		if ok {
			tap.literal = size
		}
	}
}

func (tap *wireTap) record(line []byte) {
	if len(line) < 2 || line[0] != 'T' {
		return
	}

	index := 1
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}

	if index == 1 || index >= len(line) || line[index] != ' ' {
		return
	}

	rest := line[index+1:]

	verb, rest := firstAtom(rest)
	if verb == "" || responseVerb(verb) {
		return
	}

	if verb == "UID" {
		sub, _ := firstAtom(rest)
		if sub == "" {
			return
		}

		verb = "UID " + sub
	}

	tap.recorder.addWire(verb)
}

func firstAtom(line []byte) (string, []byte) {
	index := 0
	for index < len(line) && line[index] != ' ' {
		index++
	}

	if index == 0 {
		return "", line
	}

	verb := string(line[:index])
	if index < len(line) {
		return verb, line[index+1:]
	}

	return verb, nil
}

func responseVerb(verb string) bool {
	switch verb {
	case "OK", "NO", "BAD", "BYE", "PREAUTH":
		return true
	default:
		return false
	}
}

func literalSize(line []byte) (int, bool) {
	start := bytes.LastIndexByte(line, '{')
	if start < 0 || start == len(line)-1 {
		return 0, false
	}

	body := line[start+1:]
	if body[len(body)-1] != '}' {
		return 0, false
	}

	body = body[:len(body)-1]
	if len(body) > 0 && body[len(body)-1] == '+' {
		body = body[:len(body)-1]
	}

	if len(body) == 0 {
		return 0, false
	}

	for _, char := range body {
		if char < '0' || char > '9' {
			return 0, false
		}
	}

	size, err := strconv.Atoi(string(body))
	if err != nil || size < 0 {
		return 0, false
	}

	return size, true
}

func TestWireCloseIsNotTransportClose(t *testing.T) {
	t.Parallel()

	body := []byte("T1 CLOSE\r\nnot a command\r\n")
	stream := bytes.Join([][]byte{
		[]byte("T1 LOGIN rua secret\r\n"),
		[]byte("T1 OK [CAPABILITY IMAP4rev1] logged in\r\n"),
		[]byte("* 1 FETCH (UID 1 BODY[] {" + strconv.Itoa(len(body)) + "}\r\n"),
		body,
		[]byte(")\r\n"),
		[]byte("T2 LOGOUT\r\n"),
		[]byte("T2 OK LOGOUT completed\r\n"),
	}, nil)

	recorder := newRecorder()
	tap := new(wireTap)
	tap.recorder = recorder

	_, err := tap.Write(stream)
	if err != nil {
		t.Fatal(err)
	}

	wire := recorder.wireSnapshot()
	if slices.Contains(wire, "CLOSE") || !slices.Contains(wire, "LOGOUT") ||
		!slices.Contains(wire, "LOGIN") {
		t.Fatalf("wire = %v", wire)
	}

	_, err = tap.Write([]byte("T3 CLOSE\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(recorder.wireSnapshot(), "CLOSE") {
		t.Fatalf("wire = %v", recorder.wireSnapshot())
	}

	recorder.noteTransportClosed()

	if slices.Contains(recorder.wireSnapshot(), "TRANSPORT-CLOSE") ||
		!slices.Contains(recorder.snapshot(), "TRANSPORT-CLOSE") {
		t.Fatal("transport closure was recorded as IMAP CLOSE")
	}
}
