package imapsrc

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zekihan/go-commons/metrics"

	"github.com/zekihan/rua/internal/telemetry"
)

//nolint:gocyclo // One fetch scenario checked end to end.
func TestBackfill(t *testing.T) {
	t.Parallel()

	const messages = 51

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	when := time.Unix(1_700_000_000, 0).UTC()
	for index := range messages {
		built.deliver(t, "Reports", "report", "body", when.Add(time.Duration(index)*time.Second))
	}

	registry := metrics.New()
	built.fetcher.metrics = telemetry.New(registry)

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "Reports")

	if built.recorder.count("LOGIN") != 1 || built.recorder.count("EXAMINE") != 1 ||
		built.recorder.count("UID SEARCH") != 1 || built.recorder.count("BATCH") != 1 {
		t.Fatalf("commands = %v", built.recorder.snapshot())
	}

	const bodyFetches = messages

	const metaFetches = 2

	if built.recorder.count("FETCH") != bodyFetches+metaFetches {
		t.Fatalf(
			"FETCH count = %d, commands = %v",
			built.recorder.count("FETCH"),
			built.recorder.snapshot(),
		)
	}

	uids := built.sink.uids()
	if len(uids) != messages || uids[0] != 1 || uids[len(uids)-1] != messages {
		t.Fatalf("uids = %v", uids)
	}

	if !bytes.Contains(built.sink.ingested[0].raw, []byte("Subject: report")) ||
		!bytes.Contains(built.sink.ingested[0].raw, []byte("body")) {
		t.Fatalf("body = %q", built.sink.ingested[0].raw)
	}

	if built.sink.ingested[0].src.InternalDate.Unix() != when.Unix() {
		t.Fatalf("internaldate = %s", built.sink.ingested[0].src.InternalDate)
	}

	row := built.mailbox(t, "Reports")
	if row.UIDValidity == 0 || row.NextUID != messages+1 || row.LastResult != resultOK ||
		row.LastError != "" || !row.HasSuccess || row.LastStart == 0 || row.LastEnd == 0 {
		t.Fatalf("mailbox = %+v", row)
	}

	if len(built.events(t)) != 0 {
		t.Fatalf("events = %+v", built.events(t))
	}

	text := scrape(registry)
	if !strings.Contains(text, `rua_fetch_runs_total{result="ok"}`) ||
		!strings.Contains(text, "rua_last_successful_fetch_timestamp_seconds") {
		t.Fatalf("metrics = %s", text)
	}
}

func TestIncremental(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	row := built.mailbox(t, "Reports")
	if row.NextUID != 1 || row.UIDValidity == 0 || len(built.sink.uids()) != 0 {
		t.Fatalf("empty mailbox = %+v uids %v", row, built.sink.uids())
	}

	when := time.Unix(1_700_000_100, 0).UTC()
	built.deliver(t, "Reports", "one", "alpha", when)
	built.deliver(t, "Reports", "two", "beta", when)

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(built.sink.uids(), []uint32{1, 2}) {
		t.Fatalf("uids = %v", built.sink.uids())
	}

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(built.sink.uids(), []uint32{1, 2}) {
		t.Fatalf("refetched uids = %v", built.sink.uids())
	}

	if built.mailbox(t, "Reports").NextUID != 3 {
		t.Fatalf("next = %d", built.mailbox(t, "Reports").NextUID)
	}

	built.deliver(t, "Reports", "three", "gamma", when)

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "Reports")

	if !slices.Equal(built.sink.uids(), []uint32{1, 2, 3}) {
		t.Fatalf("uids = %v", built.sink.uids())
	}

	if built.mailbox(t, "Reports").NextUID != 4 {
		t.Fatalf("next = %d", built.mailbox(t, "Reports").NextUID)
	}
}

//nolint:gocyclo // One fetch scenario checked end to end.
func TestSinkErrorResumes(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	when := time.Unix(1_700_000_200, 0).UTC()
	built.deliver(t, "Reports", "one", "a", when)
	built.deliver(t, "Reports", "two", "b", when)
	built.deliver(t, "Reports", "three", "c", when)

	built.sink.failOn = 2
	built.sink.failErr = textError("boom " + testPassword + " " + strings.Repeat("x", maxErrorLen))

	err := built.fetch(t)
	if err == nil {
		t.Fatal("expected sink error")
	}

	if !slices.Equal(built.sink.uids(), []uint32{1}) {
		t.Fatalf("uids = %v", built.sink.uids())
	}

	row := built.mailbox(t, "Reports")
	if row.NextUID != 2 || row.LastResult != resultError ||
		strings.Contains(row.LastError, testPassword) ||
		!strings.Contains(row.LastError, redacted) ||
		len(row.LastError) > maxErrorLen ||
		!utf8.ValidString(row.LastError) {
		t.Fatalf("mailbox = %+v", row)
	}

	built.sink.failOn = 0
	built.sink.failErr = nil

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "Reports")

	if !slices.Equal(built.sink.uids(), []uint32{1, 2, 3}) {
		t.Fatalf("uids = %v", built.sink.uids())
	}

	row = built.mailbox(t, "Reports")
	if row.NextUID != 4 || row.LastResult != resultOK || row.LastError != "" || !row.HasSuccess {
		t.Fatalf("mailbox = %+v", row)
	}
}

//nolint:gocyclo // One fetch scenario checked end to end.
func TestUIDValidityReset(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")
	built.deliver(t, "Reports", "old", "one", time.Unix(1_700_000_300, 0).UTC())

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	row := built.mailbox(t, "Reports")
	if row.UIDValidity != 1 || row.NextUID != 2 {
		t.Fatalf("mailbox = %+v", row)
	}

	err = built.user.Delete("Reports")
	if err != nil {
		t.Fatal(err)
	}

	built.create(t, "Reports")
	built.deliver(t, "Reports", "new", "two", time.Unix(1_700_000_400, 0).UTC())

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	events := built.events(t)
	if len(events) != 1 || events[0].Kind != eventUIDValidityReset ||
		events[0].Mailbox != "Reports" || events[0].Detail != "1->2" {
		t.Fatalf("events = %+v", events)
	}

	row = built.mailbox(t, "Reports")
	if row.UIDValidity != 2 || row.NextUID != 2 || row.LastResult != resultOK {
		t.Fatalf("mailbox = %+v", row)
	}

	if len(built.sink.ingested) != 2 ||
		!bytes.Contains(built.sink.ingested[1].raw, []byte("Subject: new")) {
		t.Fatalf("ingested = %+v", built.sink.ingested)
	}

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	if len(built.events(t)) != 1 {
		t.Fatalf("events = %+v", built.events(t))
	}

	built.assertReadOnly(t, "Reports")
}

func TestPendingBacklog(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	// Before the first fetch the mailbox row the fetcher creates has no
	// pending value yet.
	err := built.fetcher.ensureMailbox(t.Context(), "Reports")
	if err != nil {
		t.Fatal(err)
	}

	if row := built.mailbox(t, "Reports"); row.HasPending {
		t.Fatalf("pending before first fetch = %+v", row)
	}

	when := time.Unix(1_700_001_000, 0).UTC()
	built.deliver(t, "Reports", "one", "a", when)
	built.deliver(t, "Reports", "two", "b", when)
	built.deliver(t, "Reports", "three", "c", when)

	built.sink.failOn = 2
	built.sink.failErr = errSinkFailed

	err = built.fetch(t)
	if err == nil {
		t.Fatal("expected sink error")
	}

	// One message handed off, two still pending.
	if row := built.mailbox(t, "Reports"); !row.HasPending || row.Pending != 2 {
		t.Fatalf("pending after failed fetch = %+v", row)
	}

	built.sink.failOn = 0
	built.sink.failErr = nil

	err = built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	if row := built.mailbox(t, "Reports"); !row.HasPending || row.Pending != 0 {
		t.Fatalf("pending after successful fetch = %+v", row)
	}
}

func TestOversizedSkip(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	smallSubject := "small"
	smallBody := "yes"
	bigSubject := "big"
	bigBody := strings.Repeat("OVERSIZE-BODY-TOKEN ", 30)
	built.fetcher.cfg.MaxMessageBytes = int64(len(messageRaw(smallSubject, smallBody)))

	when := time.Unix(1_700_000_500, 0).UTC()
	built.deliver(t, "Reports", smallSubject, smallBody, when)
	built.deliver(t, "Reports", bigSubject, bigBody, when)

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "Reports")

	if len(built.sink.ingested) != 1 || built.sink.ingested[0].src.UID != 1 ||
		!bytes.Contains(built.sink.ingested[0].raw, []byte(smallBody)) {
		t.Fatalf("ingested = %+v", built.sink.ingested)
	}

	if len(built.sink.skipped) != 1 {
		t.Fatalf("skipped = %+v", built.sink.skipped)
	}

	skipped := built.sink.skipped[0]
	if skipped.src.UID != 2 || skipped.size != int64(len(messageRaw(bigSubject, bigBody))) ||
		!bytes.Contains(skipped.header, []byte("Subject: big")) ||
		bytes.Contains(skipped.header, []byte("OVERSIZE-BODY-TOKEN")) {
		t.Fatalf("skipped = %+v header %q", skipped.src, skipped.header)
	}

	if built.mailbox(t, "Reports").NextUID != 3 {
		t.Fatalf("next = %d", built.mailbox(t, "Reports").NextUID)
	}
}

func TestMissingMailbox(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"missing"})
	built.create(t, "INBOX")
	built.deliver(t, "INBOX", "hidden", "nope", time.Unix(1_700_000_600, 0).UTC())

	err := built.fetch(t)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}

	if len(built.sink.uids()) != 0 {
		t.Fatalf("uids = %v", built.sink.uids())
	}

	if built.recorder.count("UID SEARCH") != 0 || built.recorder.count("EXAMINE") != 1 {
		t.Fatalf("commands = %v", built.recorder.snapshot())
	}

	row := built.mailbox(t, "missing")
	if row.LastResult != resultError || row.LastError == "" || row.HasSuccess {
		t.Fatalf("mailbox = %+v", row)
	}

	built.assertReadOnly(t, "INBOX")
}

func TestSiblingMailboxContinues(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"missing", "Reports"})
	built.create(t, "Reports")
	built.deliver(t, "Reports", "keep", "yes", time.Unix(1_700_000_700, 0).UTC())

	registry := metrics.New()
	built.fetcher.metrics = telemetry.New(registry)

	err := built.fetch(t)
	if err == nil {
		t.Fatal("expected error")
	}

	if !slices.Equal(built.sink.uids(), []uint32{1}) ||
		built.sink.ingested[0].src.Mailbox != "Reports" {
		t.Fatalf("uids = %+v", built.sink.ingested)
	}

	missing := built.mailbox(t, "missing")

	okRow := built.mailbox(t, "Reports")
	if missing.LastResult != resultError || okRow.LastResult != resultOK || !okRow.HasSuccess ||
		okRow.LastError != "" {
		t.Fatalf("missing = %+v reports = %+v", missing, okRow)
	}

	text := scrape(registry)
	if !strings.Contains(text, `rua_fetch_runs_total{result="error"}`) ||
		strings.Contains(text, "rua_last_successful_fetch_timestamp_seconds") {
		t.Fatalf("metrics = %s", text)
	}

	built.assertReadOnly(t, "Reports")
}

func TestCancelSkipsMetrics(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")
	built.deliver(t, "Reports", "hold", "body", time.Unix(1_700_000_800, 0).UTC())

	registry := metrics.New()
	built.fetcher.metrics = telemetry.New(registry)
	built.sink.block = make(chan struct{})
	built.sink.started = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- built.fetcher.FetchOnce(ctx)
	}()

	select {
	case <-built.sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not reach the sink")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not return")
	}

	row := built.mailbox(t, "Reports")
	if row.LastResult != "" || row.LastError != "" || row.NextUID != 1 {
		t.Fatalf("mailbox = %+v", row)
	}

	if strings.Contains(scrape(registry), "rua_fetch_runs_total") {
		t.Fatalf("metrics = %s", scrape(registry))
	}
}

func TestTwoMailboxesOneLogin(t *testing.T) {
	t.Parallel()

	built := newRig(t, []string{"A", "B"})
	built.create(t, "A")
	built.create(t, "B")

	when := time.Unix(1_700_000_900, 0).UTC()
	built.deliver(t, "A", "a", "a", when)
	built.deliver(t, "B", "b", "b", when)

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "A", "B")

	if built.recorder.count("LOGIN") != 1 || built.recorder.count("EXAMINE") != 2 {
		t.Fatalf("commands = %v", built.recorder.snapshot())
	}

	if built.mailbox(t, "A").LastResult != resultOK ||
		built.mailbox(t, "B").LastResult != resultOK {
		t.Fatal("mailbox status")
	}
}

func TestScrub(t *testing.T) {
	t.Parallel()

	if scrub(errSample, "").Error() != "abc" || !errors.Is(scrub(errSample, "nope"), errSample) {
		t.Fatal("scrub")
	}

	got := clipError(textError("x "+testPassword), testPassword)
	if strings.Contains(got, testPassword) || !strings.Contains(got, redacted) {
		t.Fatalf("clip = %q", got)
	}

	text := strings.Repeat("a", maxErrorLen-1) + "é"

	cut := clip(text, maxErrorLen)
	if !utf8.ValidString(cut) || strings.Contains(cut, "é") || len(cut) != maxErrorLen-1 {
		t.Fatalf("cut = %q len %d", cut, len(cut))
	}
}

type textError string

func (err textError) Error() string {
	return string(err)
}

var errSample = errors.New("abc")

func scrape(registry *metrics.Registry) string {
	var buf bytes.Buffer

	registry.WritePrometheus(&buf)

	return buf.String()
}
