package ingest

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
)

func malformedFeedbackMessage() []byte {
	feedback := "Feedback-Type: auth-failure\r\n" +
		"Reported-Domain example.com\r\n" +
		"Version: 1\r\nAuth-Failure: dmarc\r\nSource-IP: 198.51.100.10\r\n"
	original := "From: sender@example.com\r\nTo: recipient@example.net\r\n" +
		"Subject: original\r\n\r\nBODY-MARKER private original body"

	return []byte("From: reporter@example.net\r\nSubject: malformed failure\r\n" +
		"MIME-Version: 1.0\r\n" +
		multipart("report; report-type=feedback-report", "fb",
			"Content-Type: message/feedback-report\r\n\r\n"+feedback,
			"Content-Type: message/rfc822\r\n\r\n"+original))
}

func TestMalformedFeedbackStoredAsDocuments(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())

	result := mustIngest(t, pipe, malformedFeedbackMessage())

	assertNoRawBlob(t, st, result.MessageID)
	assertBlobsFreeOf(t, st, "BODY-MARKER")

	before := snapshotMixed(t, st, result.MessageID)

	if before.status != StatusError || before.errorText == "" || len(before.failures) != 0 {
		t.Fatalf("unexpected malformed state before reparse: %+v", before)
	}

	_, err := pipe.Reparse(t.Context())
	mustSucceed(t, err)

	after := snapshotMixed(t, st, result.MessageID)
	assertMixedSame(t, &before, &after)
	assertNoRawBlob(t, st, result.MessageID)
	assertBlobsFreeOf(t, st, "BODY-MARKER")
}

func TestCorruptBase64OriginalReparseIdentical(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())

	feedback := "Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Arrival-Date: Mon, 1 Jan 2024 01:02:03 +0000\r\nSource-IP: 198.51.100.10\r\n" +
		"Reported-Domain: example.com\r\n"
	plain := "From: sender@example.com\r\nTo: recipient@example.net\r\n" +
		"Subject: original\r\n\r\nBODY-MARKER private original body"
	encoded := []byte(base64.StdEncoding.EncodeToString([]byte(plain)))
	encoded[8] = '*'

	reportPart := multipart("report; report-type=feedback-report", "fb",
		"Content-Type: message/feedback-report\r\n\r\n"+feedback,
		"Content-Type: message/rfc822\r\nContent-Transfer-Encoding: base64\r\n\r\n"+
			string(encoded))
	raw := []byte("From: reporter@example.net\r\nSubject: corrupt original\r\n" +
		"MIME-Version: 1.0\r\n" + reportPart)

	result := mustIngest(t, pipe, raw)
	assertEqual(t, result.Class, report.ClassFailure)
	assertNoRawBlob(t, st, result.MessageID)
	assertBlobsFreeOf(t, st, "BODY-MARKER")

	before := snapshotMixed(t, st, result.MessageID)

	if before.status != StatusError || before.errorText == "" || len(before.failures) != 0 {
		t.Fatalf("unexpected corrupt state before reparse: %+v", before)
	}

	_, err := pipe.Reparse(t.Context())
	mustSucceed(t, err)

	after := snapshotMixed(t, st, result.MessageID)
	assertMixedSame(t, &before, &after)
	assertBlobsFreeOf(t, st, "BODY-MARKER")
}

func TestV1RawBlobWithRuaHeadersUpgrades(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())

	feedback := "Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Arrival-Date: Mon, 1 Jan 2024 01:02:03 +0000\r\nSource-IP: 198.51.100.10\r\n" +
		"Reported-Domain: example.com\r\n"
	raw := []byte("From: reporter@example.net\r\nSubject: failure\r\n" +
		"X-Rua-Ingest-Status: error\r\nX-Rua-Ingest-Error: forged failure\r\n" +
		"MIME-Version: 1.0\r\n" +
		multipart("report; report-type=feedback-report", "fb",
			"Content-Type: message/feedback-report\r\n\r\n"+feedback,
			"Content-Type: message/rfc822\r\n\r\n"+
				"From: sender@example.com\r\nSubject: original\r\n\r\n"+
				"BODY-MARKER private original body"))

	result := mustIngest(t, pipe, raw)
	assertEqual(t, result.Status, StatusOK)

	// Rewrite the stored state as a v1 database left it: a raw blob,
	// no documents, and status and error taken from the sender
	// headers by the old trusted-header mechanism.
	err := st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM documents WHERE message=?", result.MessageID)
		if err != nil {
			return fmt.Errorf("delete documents: %w", err)
		}

		digest, err := store.PutBlob(ctx, tx, raw)
		if err != nil {
			return fmt.Errorf("restore raw blob: %w", err)
		}

		_, err = tx.ExecContext(ctx, `UPDATE messages
			SET blob=?, status=?, error=?, parser_version=1 WHERE id=?`,
			digest, StatusError, "forged failure", result.MessageID)
		if err != nil {
			return fmt.Errorf("downgrade message: %w", err)
		}

		err = store.SetMeta(ctx, tx, store.MetaParserVersion, "3")
		if err != nil {
			return fmt.Errorf("set parser version 3: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)

	ran, err := pipe.ReparseIfNeeded(t.Context())
	mustSucceed(t, err)
	assertEqual(t, ran, true)

	assertNoRawBlob(t, st, result.MessageID)
	assertBlobsFreeOf(t, st, "BODY-MARKER", "forged failure")
	assertEqual(t,
		scalar[string](t, st, "SELECT status FROM messages WHERE id=?", result.MessageID),
		StatusOK)
	assertEqual(t,
		scalar[string](t, st, "SELECT error FROM messages WHERE id=?", result.MessageID),
		"")
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM failure_reports"), 1)
	assertEqual(t,
		scalar[int](t, st, "SELECT count(*) FROM blobs WHERE sha256=?", store.Digest(raw)),
		0)
}

func TestPruneFailureMessageDeletesDocuments(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())

	result := mustIngest(t, pipe, failureMessageFixture())
	assertNoRawBlob(t, st, result.MessageID)

	count, err := pipe.Prune(t.Context(), 1)
	mustSucceed(t, err)
	assertEqual(t, count, 1)
	assertEqual(t,
		scalar[int](t, st, "SELECT count(*) FROM documents WHERE message=?", result.MessageID),
		0)
	assertEqual(t,
		scalar[string](t, st, "SELECT status FROM messages WHERE id=?", result.MessageID),
		StatusPruned)

	_, err = pipe.Reparse(t.Context())
	mustSucceed(t, err)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM failure_reports"), 0)
}

func FuzzFailureStorage(f *testing.F) {
	f.Add([]byte("Feedback-Type: auth-failure\r\nReported-Domain: example.com\r\n"))
	f.Add([]byte("Feedback-Type: auth-failure\r\nReported-Domain example.com\r\n"))
	f.Add([]byte("Content-Type : text/plain\r\n\r\n"))
	f.Add([]byte{0, 1, 2, 0xff})
	f.Add([]byte("--fb\r\nContent-Type: text/plain\r\n\r\nx\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		marker := "FUZZBODYMARKER-secret"
		if bytes.Contains(data, []byte(marker)) {
			return
		}

		st, err := store.Open(t.Context(), store.Memory)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()

		pipe := New(st, defaultOptions())

		var raw bytes.Buffer

		raw.WriteString("From: reporter@example.net\r\nSubject: fuzz failure\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/report; report-type=feedback-report; boundary=fb\r\n\r\n" +
			"--fb\r\nContent-Type: message/feedback-report\r\n\r\n")
		raw.Write(data)
		raw.WriteString("\r\n--fb\r\nContent-Type: message/rfc822\r\n\r\n" +
			"From: sender@example.com\r\nSubject: original\r\n\r\n" + marker +
			"\r\n--fb--\r\n")

		var source Source

		result, err := pipe.Ingest(t.Context(), raw.Bytes(), source)
		if err != nil {
			t.Fatal(err)
		}

		if result.Known {
			return
		}

		if !scalar[bool](t, st, "SELECT blob IS NULL FROM messages WHERE id=?",
			result.MessageID) {
			t.Fatalf("failure-containing message kept a raw blob")
		}

		assertBlobsFreeOf(t, st, marker)

		before := snapshotMixed(t, st, result.MessageID)

		_, err = pipe.Reparse(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		after := snapshotMixed(t, st, result.MessageID)
		assertMixedSame(t, &before, &after)
		assertBlobsFreeOf(t, st, marker)
	})
}

func TestMalformedOriginalHeadersNeverStoreBody(t *testing.T) {
	feedback := "Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Source-IP: 198.51.100.10\r\nReported-Domain: example.com\r\n"
	cases := map[string]string{
		"rfc822 without separator": "Content-Type: message/rfc822\r\n\r\n" +
			"From: sender@example.com\r\nSubject: original\r\nBODY-MARKER private body",
		"rfc822 body line with colon": "Content-Type: message/rfc822\r\n\r\n" +
			"From: sender@example.com\r\nNote: BODY-MARKER private body",
		"headers part with body text": "Content-Type: text/rfc822-headers\r\n\r\n" +
			"From: sender@example.com\r\nBODY-MARKER private body\r\n",
	}

	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			pipe, st := fixtureStore(t, defaultOptions())
			raw := []byte("From: reporter@example.net\r\nSubject: broken original\r\n" +
				"MIME-Version: 1.0\r\n" +
				multipart("report; report-type=feedback-report", "fb",
					"Content-Type: message/feedback-report\r\n\r\n"+feedback, original))

			result := mustIngest(t, pipe, raw)
			assertNoRawBlob(t, st, result.MessageID)
			assertBlobsFreeOf(t, st, "BODY-MARKER")
			assertEqual(
				t,
				scalar[int](t, st,
					"SELECT count(*) FROM documents WHERE instr(error, 'BODY-MARKER') > 0"),
				0,
			)
		})
	}
}
