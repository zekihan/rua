package query

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestMessageRawRendersFailureDocuments(t *testing.T) {
	fx := newFixture(t, nil)

	xml := aggregateXML("Example", "render-agg-1", "example.com",
		unixTime(2025, 6, 9, 0), unixTime(2025, 6, 11, 0), "reject", 100,
		"192.0.2.1", 3, "pass", "fail", "none",
		"example.com", "example.com", "pass", "mail", "")

	raw := "From: reporter@example.net\r\nSubject: failure download\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: application/xml\r\n\r\n" + xml + "\r\n" +
		"--outer\r\nContent-Type: multipart/report; report-type=feedback-report; boundary=fb\r\n\r\n" +
		"--fb\r\nContent-Type: message/feedback-report\r\n\r\n" +
		"Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Reported-Domain: example.com\r\n" +
		"--fb\r\nContent-Type: message/rfc822\r\n\r\n" +
		"From: sender@example.com\r\nTo: recipient@example.net\r\n" +
		"Subject: original\r\n\r\nBODY-MARKER private original body\r\n" +
		"--fb--\r\n--outer--\r\n"

	fx.ingest(t, raw)

	var messageID int64

	err := fx.st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &messageID, "SELECT id FROM messages")
		if err != nil {
			return fmt.Errorf("read message id: %w", err)
		}

		var blob []byte

		err = tx.GetContext(ctx, &blob, "SELECT blob FROM messages WHERE id=?", messageID)
		if err != nil {
			return fmt.Errorf("read message blob: %w", err)
		}

		if len(blob) > 0 {
			t.Fatalf("failure-containing message kept a raw blob")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	data, filename, err := fx.svc.MessageRaw(t.Context(), messageID)
	if err != nil {
		t.Fatal(err)
	}

	if filename == "" {
		t.Fatalf("empty download filename")
	}

	for _, want := range []string{
		"Feedback-Type: auth-failure",
		"Reported-Domain: example.com",
		"Subject: original",
		"From: sender@example.com",
		"multipart/report; report-type=feedback-report",
		"render-agg-1",
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("rendered download is missing %q:\n%s", want, data)
		}
	}

	if bytes.Contains(data, []byte("BODY-MARKER")) {
		t.Fatalf("rendered download leaks the original body:\n%s", data)
	}
}
