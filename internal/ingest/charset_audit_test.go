package ingest

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/zekihan/rua/internal/report"
)

func latin1Fixture(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile("../../testdata/dmarc/latin1.xml")
	mustSucceed(t, err)

	return raw
}

func TestIngestDeclaredLatin1Aggregate(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())

	result := mustIngest(t, pipe, latin1Fixture(t))

	assertEqual(t, result.Class, report.ClassAggregate)
	assertEqual(t, result.Status, StatusOK)
	assertEqual(t, result.Reports, 1)
	assertEqual(t, scalar[string](t, st, "SELECT org FROM reports"), "Café")
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM records"), 1)
}

func TestIngestUnsupportedAggregateEncoding(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())
	valid := latin1Fixture(t)
	invalid := bytes.Replace(valid, []byte("ISO-8859-1"), []byte("x-unknown-encoding"), 1)

	result := mustIngest(t, pipe, zipFixture(t, [][]byte{invalid, valid}, 1))

	assertEqual(t, result.Class, report.ClassAggregate)
	assertEqual(t, result.Status, StatusPartial)
	assertEqual(t, result.Reports, 1)
	assertEqual(t, scalar[string](t, st, "SELECT org FROM reports"), "Café")

	errorText := scalar[string](t, st, "SELECT error FROM messages WHERE id=?", result.MessageID)
	if !strings.Contains(errorText, `opening charset "x-unknown-encoding"`) {
		t.Fatalf("stored error = %q, want unsupported XML encoding error", errorText)
	}
}
