package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewLoaderRejectsInvalidFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sources.yaml")
	writeSources(t, path, "- name: x\n", time.Unix(1_000, 0))

	_, err := NewLoader(path)
	if err == nil {
		t.Fatal("expected error")
	}

	_, err = NewLoader(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestLoaderEmptyPath(t *testing.T) {
	t.Parallel()

	loader, err := NewLoader("")
	if err != nil {
		t.Fatal(err)
	}

	if loader.Current() == nil || len(loader.Current().Rules()) != 0 {
		t.Fatal("expected empty set")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- loader.Run(ctx)
	}()

	select {
	case err = <-done:
		t.Fatalf("returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	cancel()

	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestLoaderReloadsAndKeepsPrevious(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sources.yaml")
	writeSources(t, path, ruleDocument("First"), time.Unix(1_000, 0))

	loader, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}

	if loader.Current().Rules()[0].Name != "First" {
		t.Fatalf("initial = %+v", loader.Current().Rules())
	}

	logs := &lockedBuffer{mu: sync.Mutex{}, buf: bytes.Buffer{}}
	loader.logger = slog.New(slog.NewTextHandler(logs, nil))
	loader.SetInterval(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- loader.Run(ctx)
	}()

	writeSources(t, path, ruleDocument("Second"), time.Unix(2_000, 0))
	waitFor(t, func() bool {
		rules := loader.Current().Rules()

		return len(rules) == 1 && rules[0].Name == "Second"
	})

	writeSources(t, path, "not: [yaml\n", time.Unix(3_000, 0))
	waitFor(t, func() bool {
		return bytes.Contains(logs.snapshot(), []byte("Source rules reload failed"))
	})

	rules := loader.Current().Rules()
	if len(rules) != 1 || rules[0].Name != "Second" {
		t.Fatalf("kept %+v", rules)
	}

	writeSources(t, path, ruleDocument("Third"), time.Unix(4_000, 0))
	waitFor(t, func() bool {
		rules = loader.Current().Rules()

		return len(rules) == 1 && rules[0].Name == "Third"
	})

	cancel()

	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return")
	}
}

func ruleDocument(name string) string {
	return "- name: " + name + "\n  match:\n    asns: [64500]\n"
}

func writeSources(t *testing.T, path, content string, when time.Time) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Chtimes(path, when, when)
	if err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()

	for {
		if cond() {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("condition not met")
		case <-tick.C:
		}
	}
}

type lockedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	count, err := buffer.buf.Write(data)
	if err != nil {
		return 0, fmt.Errorf("buffer log: %w", err)
	}

	return count, nil
}

func (buffer *lockedBuffer) snapshot() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	return bytes.Clone(buffer.buf.Bytes())
}
