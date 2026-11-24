package imapsrc

import (
	"sync"
	"testing"
	"time"

	"github.com/zekihan/rua/internal/store"
)

func TestNextUIDDoesNotRewind(t *testing.T) {
	t.Parallel()

	fetcher, database := seedCursor(t)
	ctx := t.Context()

	err := fetcher.saveNextUID(ctx, "Reports", 5, 3)
	if err != nil {
		t.Fatal(err)
	}

	err = fetcher.saveNextUID(ctx, "Reports", 9, 40)
	if err != nil {
		t.Fatal(err)
	}

	row := mailboxOf(t, database, "Reports")
	if row.NextUID != 10 || row.UIDValidity != 5 {
		t.Fatalf("mailbox = %+v", row)
	}
}

func TestUIDValidityResetMatchesObserved(t *testing.T) {
	t.Parallel()

	fetcher, database := seedCursor(t)
	ctx := t.Context()

	applied, err := fetcher.resetValidity(ctx, "Reports", 4, 8)
	if err != nil || applied {
		t.Fatalf("stale reset applied %v err %v", applied, err)
	}

	row := mailboxOf(t, database, "Reports")
	if row.NextUID != 10 || row.UIDValidity != 5 || len(eventsOf(t, database)) != 0 {
		t.Fatalf("mailbox = %+v events = %+v", row, eventsOf(t, database))
	}

	applied, err = fetcher.resetValidity(ctx, "Reports", 5, 8)
	if err != nil || !applied {
		t.Fatalf("reset applied %v err %v", applied, err)
	}

	row = mailboxOf(t, database, "Reports")

	events := eventsOf(t, database)
	if row.UIDValidity != 8 || row.NextUID != 1 || len(events) != 1 || events[0].Detail != "5->8" {
		t.Fatalf("mailbox = %+v events = %+v", row, events)
	}
}

func seedCursor(t *testing.T) (*Fetcher, *store.Store) {
	t.Helper()

	database := openDB(t)
	fetcher := New(filledConfig(
		"127.0.0.1:143", tlsNone, "", "", "", nil, time.Hour, 0,
	), database, new(fakeSink), nil)

	ctx := t.Context()

	err := fetcher.ensureMailbox(ctx, "Reports")
	if err != nil {
		t.Fatal(err)
	}

	applied, err := fetcher.saveUIDValidity(ctx, "Reports", 5)
	if err != nil || !applied {
		t.Fatalf("save validity applied %v err %v", applied, err)
	}

	err = fetcher.saveNextUID(ctx, "Reports", 5, 10)
	if err != nil {
		t.Fatal(err)
	}

	return fetcher, database
}

func TestConcurrentFetchersDoNotRewind(t *testing.T) {
	t.Parallel()

	const messages = 3

	built := newRig(t, []string{"Reports"})
	built.create(t, "Reports")

	when := time.Unix(1_700_002_000, 0).UTC()
	for range messages {
		built.deliver(t, "Reports", "report", "body", when)
	}

	secondSink := new(fakeSink)
	secondSink.failOn = 2
	second := New(built.fetcher.cfg, built.database, secondSink, nil)
	second.jitterN = func() int64 { return 0 }

	var gate sync.WaitGroup

	gate.Add(2)

	release := make(chan struct{})
	built.fetcher.beforeSave = saveBarrier(2, &gate, nil)
	second.beforeSave = saveBarrier(2, &gate, release)

	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)

	go func() {
		firstErr <- built.fetcher.FetchOnce(t.Context())
	}()

	go func() {
		secondErr <- second.FetchOnce(t.Context())
	}()

	err := waitErr(t, firstErr)
	if err != nil {
		t.Fatal(err)
	}

	close(release)

	err = waitErr(t, secondErr)
	if err == nil {
		t.Fatal("expected the second fetch to stop on its sink")
	}

	row := built.mailbox(t, "Reports")
	if row.NextUID != messages+1 {
		t.Fatalf("next = %d", row.NextUID)
	}

	if len(built.sink.uids()) != messages || len(secondSink.uids()) != 1 {
		t.Fatalf("first %v second %v", built.sink.uids(), secondSink.uids())
	}

	if built.sink.ingested[0].src.UIDValidity == 0 {
		t.Fatal("missing uidvalidity")
	}
}

func saveBarrier(at uint32, gate *sync.WaitGroup, release <-chan struct{}) func(string, uint32) {
	var once sync.Once

	return func(_ string, next uint32) {
		if next != at {
			return
		}

		once.Do(func() {
			gate.Done()
			gate.Wait()

			if release != nil {
				<-release
			}
		})
	}
}

func eventsOf(t *testing.T, database *store.Store) []eventRow {
	t.Helper()

	built := new(rig)
	built.database = database

	return built.events(t)
}
