package ingest

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/report"
	"github.com/zekihan/rua/internal/store"
)

const (
	sampleXML = `<feedback id="a" end="1000" count="1"/>`
	sampleTLS = `{"organization-name":"Example","policies":` +
		`[{"domain":"a.example"},{"domain":"b.example"},{"domain":"a.example"}]}`
	sampleDate = "Tue, 03 Jun 2025 12:00:00 +0000"
	oldXML     = `<feedback id="old" end="1" count="1"/>`
)

var (
	errTestParse = errors.New("synthetic parse failure")
	testNow      = time.Unix(400_000, 0)
)

func defaultOptions() Options {
	var options Options

	return options
}

func fixtureStore(t *testing.T, options Options) (*Pipeline, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "rua.db"))
	mustSucceed(t, err)
	t.Cleanup(func() { mustSucceed(t, st.Close()) })

	return New(st, options), st
}

func fakeParsers(t *testing.T) {
	t.Helper()

	savedAggregate, savedTLS, savedFailure := parseAggregate, parseTLS, parseFailure

	t.Cleanup(
		func() { parseAggregate, parseTLS, parseFailure = savedAggregate, savedTLS, savedFailure },
	)

	parseAggregate = func(raw []byte) (*report.Aggregate, error) {
		var wire struct {
			ID    string `xml:"id,attr"`
			End   int64  `xml:"end,attr"`
			Count int64  `xml:"count,attr"`
		}

		err := xml.Unmarshal(raw, &wire)
		if err != nil || wire.ID == "" {
			return nil, errTestParse
		}

		value := new(report.Aggregate)
		value.Schema = "rfc7489"
		value.OrgName = "Example"
		value.Domain = "example.test"
		value.ReportID = wire.ID
		value.End = wire.End
		value.Policy.P = "reject"

		var record report.Record

		record.SourceIP = "192.0.2.1"
		record.Count = wire.Count
		record.Reasons = []report.Reason{{Type: "forwarded", Comment: "test"}}

		var auth report.AuthResult

		auth.Method = "dkim"
		auth.Result = "pass"
		record.Auth = append(record.Auth, auth)
		value.Records = append(value.Records, record)

		return value, nil
	}
	parseTLS = func(raw []byte) (*report.TLS, error) {
		var wire struct {
			Policies []struct {
				Domain  string `json:"domain"`
				Success int64  `json:"success"`
			} `json:"policies"`
		}

		err := json.Unmarshal(raw, &wire)
		if err != nil {
			return nil, fmt.Errorf("decode test TLS: %w", err)
		}

		var value report.TLS

		value.OrgName = "Example"
		value.ReportID = "tls-1"
		value.End = 1000

		for _, row := range wire.Policies {
			var policy report.TLSPolicy

			policy.Domain = row.Domain
			policy.Success = row.Success
			value.Policies = append(value.Policies, policy)
		}

		var failure report.TLSFailure

		failure.ResultType = "certificate-expired"
		if len(value.Policies) > 0 {
			value.Policies[0].Failures = []report.TLSFailure{failure}
		}

		return &value, nil
	}
	parseFailure = func(_, headers []byte) (*report.Failure, error) {
		if !bytes.Contains(headers, []byte("Subject: original")) ||
			bytes.Contains(headers, []byte("private body")) {
			return nil, errTestParse
		}

		value := new(report.Failure)
		value.Arrival = 1
		value.Subject = "original"

		return value, nil
	}
}

func mustSucceed(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

func assertEqual[Value comparable](t *testing.T, got, want Value) {
	t.Helper()

	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func scalar[Value any](t *testing.T, st *store.Store, query string, args ...any) Value {
	t.Helper()

	var value Value

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &value, query, args...)
		if err != nil {
			return fmt.Errorf("test query: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)

	return value
}

func mustIngest(t *testing.T, pipe *Pipeline, raw []byte) Result {
	t.Helper()

	var source Source

	result, err := pipe.Ingest(t.Context(), raw, source)
	mustSucceed(t, err)

	return result
}

func gzipFixture(t *testing.T, data []byte, repeat int) []byte {
	t.Helper()

	var compressed bytes.Buffer

	gz, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	mustSucceed(t, err)
	writeRepeated(t, gz, data, repeat)
	mustSucceed(t, gz.Close())

	return compressed.Bytes()
}

func zipFixture(t *testing.T, entries [][]byte, repeat int) []byte {
	t.Helper()

	var compressed bytes.Buffer

	archive := zip.NewWriter(&compressed)
	for index, body := range entries {
		entry, err := archive.Create(strconv.Itoa(index) + ".bin")
		mustSucceed(t, err)
		writeRepeated(t, entry, body, repeat)
	}

	mustSucceed(t, archive.Close())

	return compressed.Bytes()
}

func writeRepeated(t *testing.T, writer io.Writer, data []byte, repeat int) {
	t.Helper()

	for range repeat {
		_, err := writer.Write(data)
		mustSucceed(t, err)
	}
}

func multipart(subtype, boundary string, parts ...string) string {
	var data strings.Builder
	fmt.Fprintf(&data, "Content-Type: multipart/%s; boundary=%s\r\n\r\n", subtype, boundary)

	for _, part := range parts {
		fmt.Fprintf(&data, "--%s\r\n%s\r\n", boundary, part)
	}

	fmt.Fprintf(&data, "--%s--\r\n", boundary)

	return data.String()
}

func attachment(mediaType string, data []byte) string {
	return "Content-Type: " + mediaType + "\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=fixture.bin\r\n\r\n" +
		base64.StdEncoding.EncodeToString(data)
}

func feedbackFixture() string {
	return multipart("report; report-type=feedback-report", "feedback",
		"Content-Type: message/feedback-report\r\n\r\nFeedback-Type: auth-failure",
		"Content-Type: message/rfc822\r\n\r\nSubject: original\r\n\r\nprivate body")
}

func TestIngestMIME(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, defaultOptions())
	doc := []byte(sampleXML)
	gz := gzipFixture(t, doc, 1)
	zipped := zipFixture(t, [][]byte{doc, []byte("<feedback/>")}, 1)
	bundled := multipart("mixed", "outer",
		attachment("application/octet-stream", zipped),
		attachment("application/octet-stream", gz),
		attachment("application/xml", doc),
		multipart("mixed", "inner", "Content-Type: text/plain; charset=unknown\r\n\r\n"+sampleXML),
		attachment(typeTLSGzip, gzipFixture(t, []byte(sampleTLS), 1)),
		feedbackFixture())
	raw := []byte("From: =?ISO-8859-1?Q?Andr=E9?= <report@example.test>\r\n" +
		"Subject: " + strings.Repeat("é", report.MaxMedium) + "\r\n" + bundled)

	result := mustIngest(t, pipe, raw)
	assertEqual(t, result.Class, report.ClassAggregate)
	assertEqual(t, result.Status, StatusPartial)
	assertEqual(t, result.Reports, 6)
	assertEqual(
		t,
		mustIngest(t, pipe, raw),
		Result{Known: true, Class: "", Status: "", MessageID: 0, Reports: 0},
	)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT from_addr FROM messages"),
		"André <report@example.test>",
	)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT subject FROM messages"),
		strings.Repeat("é", report.MaxMedium/2),
	)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM tls_policies"), 3)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM tls_failures"), 1)
	assertEqual(
		t,
		scalar[int](t, st, "SELECT count(*) FROM reports WHERE duplicate_of IS NOT NULL"),
		3,
	)
	assertEqual(t, scalar[string](t, st, "SELECT orig_subject FROM failure_reports"), "original")
	assertEqual(
		t,
		scalar[string](t, st, "SELECT reasons FROM records"),
		`[{"type":"forwarded","comment":"test"}]`,
	)
	assertEqual(t, scalar[string](t, st, "SELECT method FROM auth_results"), "dkim")
}

func TestOtherFailureAndSkip(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, defaultOptions())
	other := mustIngest(t, pipe, []byte("Subject: hello\r\n\r\nOrdinary text."))
	assertEqual(t, other.Class, report.ClassOther)
	assertEqual(t, other.Status, StatusOK)
	failure := mustIngest(t, pipe, []byte(feedbackFixture()))
	assertEqual(t, failure.Class, report.ClassFailure)
	assertEqual(t, failure.Status, StatusOK)

	headers := []byte("From: sender@example.test\r\nSubject: caf\xff")
	source := Source{Mailbox: "INBOX", UID: 42, UIDValidity: 0, InternalDate: testNow}
	skipped, err := pipe.Skip(t.Context(), headers, 1000, source)
	mustSucceed(t, err)
	assertEqual(t, skipped.Status, StatusSkipped)
	assertEqual(
		t,
		scalar[bool](t, st, "SELECT blob IS NULL FROM messages WHERE id=?", skipped.MessageID),
		true,
	)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT error FROM messages WHERE id=?", skipped.MessageID),
		"message of 1000 bytes is larger than the fetch limit",
	)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT subject FROM messages WHERE id=?", skipped.MessageID),
		"caf�",
	)
	assertEqual(t, scalar[int](t, st, "SELECT uid FROM messages WHERE id=?", skipped.MessageID), 42)
	assertEqual(
		t,
		scalar[int64](t, st, "SELECT internaldate FROM messages WHERE id=?", skipped.MessageID),
		testNow.Unix(),
	)

	ingested := mustIngest(t, pipe, headers)
	if ingested.Known || ingested.MessageID == skipped.MessageID {
		t.Fatalf("skip digest collided with the raw message: %+v", ingested)
	}
}

func storedMessageBlob(t *testing.T, st *store.Store, id int64) []byte {
	t.Helper()

	var blob []byte

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		var digest []byte

		err := tx.GetContext(ctx, &digest, "SELECT blob FROM messages WHERE id=?", id)
		if err != nil {
			return fmt.Errorf("read message blob digest: %w", err)
		}

		blob, err = store.GetBlob(ctx, tx, digest)
		if err != nil {
			return fmt.Errorf("read message blob: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)

	return blob
}

func failureMessageFixture() []byte {
	feedback := "Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Arrival-Date: Mon, 1 Jan 2024 01:02:03 +0000\r\nSource-IP: 198.51.100.10\r\n" +
		"Reported-Domain: example.com\r\n"
	original := "From: sender@example.com\r\nTo: recipient@example.net\r\n" +
		"Subject: original\r\n\r\nprivate original body"

	return []byte("From: reporter@example.net\r\nSubject: failure report\r\n" +
		"MIME-Version: 1.0\r\n" +
		multipart("report; report-type=feedback-report", "fb",
			"Content-Type: text/plain\r\n\r\nhuman secret explanation",
			"Content-Type: message/feedback-report\r\n\r\n"+feedback,
			"Content-Type: message/rfc822\r\n\r\n"+original))
}

func TestFailureBlobSanitized(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())
	raw := failureMessageFixture()

	result := mustIngest(t, pipe, raw)
	assertEqual(t, result.Class, report.ClassFailure)
	assertEqual(t, result.Status, StatusOK)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM failure_reports"), 1)
	assertEqual(t, scalar[string](t, st, "SELECT orig_subject FROM failure_reports"), "original")

	blob := storedMessageBlob(t, st, result.MessageID)
	if bytes.Contains(blob, []byte("private original body")) ||
		bytes.Contains(blob, []byte("human secret")) {
		t.Fatalf("stored failure blob leaks body: %q", blob)
	}

	if !bytes.Contains(blob, []byte("Feedback-Type: auth-failure")) ||
		!bytes.Contains(blob, []byte("Subject: original")) {
		t.Fatalf("stored failure blob lost headers: %q", blob)
	}

	rawDigest := store.Digest(raw)
	assertEqual(
		t,
		scalar[bool](t, st, "SELECT EXISTS(SELECT 1 FROM messages WHERE id=? AND sha256=?)",
			result.MessageID, rawDigest),
		true,
	)
	// Dedup still recognizes a refetch of the raw bytes.
	again := mustIngest(t, pipe, raw)
	if !again.Known {
		t.Fatalf("refetch of failure message not recognized: %+v", again)
	}

	// Simulate pre-fix stored data: restore the raw blob, then reparse.
	err := st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		digest, err := store.PutBlob(ctx, tx, raw)
		if err != nil {
			return fmt.Errorf("restore raw blob: %w", err)
		}

		_, err = tx.ExecContext(
			ctx,
			"UPDATE messages SET blob=? WHERE id=?",
			digest,
			result.MessageID,
		)
		if err != nil {
			return fmt.Errorf("point message at raw blob: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)

	_, err = pipe.Reparse(t.Context())
	mustSucceed(t, err)

	blob = storedMessageBlob(t, st, result.MessageID)
	if bytes.Contains(blob, []byte("private original body")) ||
		bytes.Contains(blob, []byte("human secret")) {
		t.Fatalf("reparse did not sanitize failure blob: %q", blob)
	}

	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM failure_reports"), 1)
	assertEqual(t, scalar[string](t, st, "SELECT orig_subject FROM failure_reports"), "original")
	assertEqual(
		t,
		scalar[int](t, st, "SELECT count(*) FROM blobs WHERE sha256=?", rawDigest),
		0,
	)
}

type storedFailureRow struct {
	Domain      string `db:"domain"`
	SourceIP    string `db:"source_ip"`
	AuthFailure string `db:"auth_failure"`
	OrigFrom    string `db:"orig_from"`
	OrigTo      string `db:"orig_to"`
	OrigSubject string `db:"orig_subject"`
	Arrival     int64  `db:"arrival"`
}

func storedFailureRows(t *testing.T, st *store.Store) []storedFailureRow {
	t.Helper()

	var rows []storedFailureRow

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		err := tx.SelectContext(ctx, &rows, `SELECT arrival, domain, source_ip, auth_failure,
			orig_from, orig_to, orig_subject FROM failure_reports ORDER BY domain, orig_subject`)
		if err != nil {
			return fmt.Errorf("read failure rows: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)

	return rows
}

func feedbackReportPart(boundary, domain, subject string) string {
	feedback := "Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Arrival-Date: Mon, 1 Jan 2024 01:02:03 +0000\r\nSource-IP: 198.51.100.10\r\n" +
		"Reported-Domain: " + domain + "\r\n"
	original := "From: sender@example.com\r\nTo: recipient@example.net\r\n" +
		"Subject: " + subject + "\r\n\r\nprivate original body " + subject

	return multipart(
		"report; report-type=feedback-report",
		boundary,
		"Content-Type: message/feedback-report\r\n\r\n"+feedback+"\r\ntrailing feedback secret "+domain,
		"Content-Type: message/rfc822\r\n\r\n"+original,
	)
}

func TestFailureBlobNestedTrailingAndMultiple(t *testing.T) {
	pipe, st := fixtureStore(t, defaultOptions())
	raw := []byte(
		"From: reporter@example.net\r\nTo: rua@example.com\r\nSubject: failure report\r\n" +
			"Date: Tue, 03 Jun 2025 12:00:00 +0000\r\nMessage-ID: <nested@example.net>\r\n" +
			"MIME-Version: 1.0\r\n" +
			multipart("mixed", "outer",
				"Content-Type: text/plain\r\n\r\nhuman secret explanation",
				feedbackReportPart("inner-a", "a.example.com", "original-a"),
				feedbackReportPart("inner-b", "b.example.com", "original-b")),
	)

	result := mustIngest(t, pipe, raw)
	assertEqual(t, result.Class, report.ClassFailure)
	assertEqual(t, result.Status, StatusOK)

	want := []storedFailureRow{
		{
			Arrival: 1704070923, Domain: "a.example.com", SourceIP: "198.51.100.10",
			AuthFailure: "dmarc", OrigFrom: "sender@example.com",
			OrigTo: "recipient@example.net", OrigSubject: "original-a",
		},
		{
			Arrival: 1704070923, Domain: "b.example.com", SourceIP: "198.51.100.10",
			AuthFailure: "dmarc", OrigFrom: "sender@example.com",
			OrigTo: "recipient@example.net", OrigSubject: "original-b",
		},
	}
	if got := storedFailureRows(t, st); !slices.Equal(got, want) {
		t.Fatalf("failure rows = %+v, want %+v", got, want)
	}

	blob := storedMessageBlob(t, st, result.MessageID)
	for _, marker := range []string{
		"trailing feedback secret", "private original body", "human secret explanation",
	} {
		if bytes.Contains(blob, []byte(marker)) {
			t.Fatalf("stored failure blob leaks %q: %q", marker, blob)
		}
	}

	if !bytes.Contains(blob, []byte("Subject: failure report")) ||
		!bytes.Contains(blob, []byte("Message-ID: <nested@example.net>")) ||
		bytes.Count(blob, []byte("Content-Type: message/feedback-report")) != 2 ||
		bytes.Count(blob, []byte("Content-Type: text/rfc822-headers")) != 2 {
		t.Fatalf("stored failure blob has wrong structure: %q", blob)
	}

	rawDigest := store.Digest(raw)
	assertEqual(
		t,
		scalar[bool](t, st, "SELECT EXISTS(SELECT 1 FROM messages WHERE id=? AND sha256=?)",
			result.MessageID, rawDigest),
		true,
	)

	_, err := pipe.Reparse(t.Context())
	mustSucceed(t, err)

	if got := storedFailureRows(t, st); !slices.Equal(got, want) {
		t.Fatalf("failure rows after reparse = %+v, want %+v", got, want)
	}

	if reparsed := storedMessageBlob(t, st, result.MessageID); !bytes.Equal(reparsed, blob) {
		t.Fatalf("sanitized blob not stable across reparse:\n%q\nwant:\n%q", reparsed, blob)
	}
}

const mixedAggregateXML = `<feedback><report_metadata><org_name>Example</org_name>` +
	`<report_id>mixed-agg-1</report_id><date_range><begin>1704067200</begin>` +
	`<end>1704153599</end></date_range></report_metadata>` +
	`<policy_published><domain>example.com</domain><p>reject</p><pct>100</pct></policy_published>` +
	`<record><row><source_ip>192.0.2.1</source_ip><count>3</count>` +
	`<policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf>` +
	`</policy_evaluated></row><identifiers><header_from>example.com</header_from></identifiers>` +
	`<auth_results><dkim><domain>example.com</domain><selector>mail</selector>` +
	`<result>pass</result></dkim></auth_results></record></feedback>`

const mixedTLSJSON = `{"organization-name":"Example","contact-info":"tls@example.net",` +
	`"report-id":"mixed-tls-1","date-range":{"start-datetime":"2024-01-01T00:00:00Z",` +
	`"end-datetime":"2024-01-01T23:59:59Z"},"policies":[{"policy":{"policy-type":"sts",` +
	`"policy-domain":"example.com"},"summary":{"total-successful-session-count":5,` +
	`"total-failure-session-count":1},"failure-details":[{"result-type":"certificate-expired",` +
	`"sending-mta-ip":"192.0.2.10","failed-session-count":1}]}]}`

func mixedFailurePart() string {
	feedback := "Feedback-Type: auth-failure\r\nVersion: 1\r\nAuth-Failure: dmarc\r\n" +
		"Arrival-Date: Mon, 1 Jan 2024 01:02:03 +0000\r\nSource-IP: 198.51.100.10\r\n" +
		"Reported-Domain: example.com\r\n"
	original := "From: sender@example.com\r\nTo: recipient@example.net\r\n" +
		"Subject: original\r\n\r\nBODY-MARKER private original body"

	return multipart("report; report-type=feedback-report", "fb-inner",
		"Content-Type: text/plain\r\n\r\nBODY-MARKER human secret",
		"Content-Type: message/feedback-report\r\n\r\n"+feedback,
		"Content-Type: message/rfc822\r\n\r\n"+original)
}

type mixedSnapshot struct {
	class     string
	failures  []storedFailureRow
	reportIDs []int64
	records   int
	tlsPols   int
	reports   int
}

func snapshotMixed(t *testing.T, st *store.Store, messageID int64) mixedSnapshot {
	t.Helper()

	var snap mixedSnapshot

	snap.class = scalar[string](t, st, "SELECT class FROM messages WHERE id=?", messageID)
	snap.failures = storedFailureRows(t, st)
	snap.reports = scalar[int](t, st, "SELECT count(*) FROM reports")
	snap.records = scalar[int](t, st, "SELECT count(*) FROM records")
	snap.tlsPols = scalar[int](t, st, "SELECT count(*) FROM tls_policies")

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		err := tx.SelectContext(
			ctx, &snap.reportIDs, "SELECT id FROM reports ORDER BY id",
		)
		if err != nil {
			return fmt.Errorf("read report ids: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)

	return snap
}

func TestMixedFailureAndReportBothOrders(t *testing.T) {
	cases := []struct {
		name    string
		docPart string
		kind    report.Class
		records int
		tlsPols int
	}{
		{"aggregate report first", attachment("application/xml", []byte(mixedAggregateXML)),
			report.ClassAggregate, 1, 0},
		{"aggregate failure first", attachment("application/xml", []byte(mixedAggregateXML)),
			report.ClassAggregate, 1, 0},
		{"tls report first", attachment(typeTLSJSON, []byte(mixedTLSJSON)),
			report.ClassTLS, 0, 1},
		{"tls failure first", attachment(typeTLSJSON, []byte(mixedTLSJSON)),
			report.ClassTLS, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipe, st := fixtureStore(t, defaultOptions())
			failureFirst := strings.Contains(tc.name, "failure first")

			var raw []byte
			if failureFirst {
				raw = []byte("From: reporter@example.net\r\nSubject: mixed\r\n" +
					"MIME-Version: 1.0\r\n" +
					multipart("mixed", "outer", mixedFailurePart(), tc.docPart))
			} else {
				raw = []byte("From: reporter@example.net\r\nSubject: mixed\r\n" +
					"MIME-Version: 1.0\r\n" +
					multipart("mixed", "outer", tc.docPart, mixedFailurePart()))
			}

			result := mustIngest(t, pipe, raw)
			if failureFirst {
				assertEqual(t, result.Class, report.ClassFailure)
			} else {
				assertEqual(t, result.Class, tc.kind)
			}

			assertEqual(t, result.Status, StatusOK)

			before := snapshotMixed(t, st, result.MessageID)
			assertEqual(t, before.reports, 1)
			assertEqual(t, before.records, tc.records)
			assertEqual(t, before.tlsPols, tc.tlsPols)

			if len(before.failures) != 1 || before.failures[0].OrigSubject != "original" {
				t.Fatalf("failure rows = %+v", before.failures)
			}

			blob := storedMessageBlob(t, st, result.MessageID)
			if bytes.Contains(blob, []byte("BODY-MARKER")) {
				t.Fatalf("stored mixed blob leaks body marker: %q", blob)
			}

			_, err := pipe.Reparse(t.Context())
			mustSucceed(t, err)

			after := snapshotMixed(t, st, result.MessageID)
			if after.class != before.class || after.reports != before.reports ||
				after.records != before.records || after.tlsPols != before.tlsPols ||
				!slices.Equal(after.reportIDs, before.reportIDs) ||
				!slices.Equal(after.failures, before.failures) {
				t.Fatalf("reparse changed mixed rows:\nbefore %+v\nafter %+v", before, after)
			}

			if reparsed := storedMessageBlob(t, st, result.MessageID); !bytes.Equal(
				reparsed, blob,
			) {
				t.Fatalf("sanitized mixed blob not stable:\n%q\nwant:\n%q", reparsed, blob)
			}
		})
	}
}

func finishWithin(t *testing.T, name string, fn func()) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		defer close(done)

		fn()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", name)
	}
}

var truncatedMultipart = map[string]string{
	"missing closing boundary": "From: reporter@example.net\r\n" +
		"Content-Type: multipart/report; report-type=feedback-report; boundary=fb\r\n\r\n" +
		"--fb\r\nContent-Type: message/feedback-report\r\n\r\nFeedback-Type: auth-failure\r\n",
	"truncated part headers": "From: reporter@example.net\r\n" +
		"Content-Type: multipart/report; report-type=feedback-report; boundary=fb\r\n\r\n" +
		"--fb\r\nContent-Type: message/feedback-repor",
	"truncated nested part headers": "From: reporter@example.net\r\n" +
		"Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: text/plain\r\nX-Trunc",
}

func TestSanitizeTruncatedMultipart(t *testing.T) {
	for name, raw := range truncatedMultipart {
		finishWithin(t, name, func() { _ = sanitizeFailureMessage([]byte(raw), nil) })
	}
}

func TestIngestTruncatedMultipart(t *testing.T) {
	for name, raw := range truncatedMultipart {
		pipe, _ := fixtureStore(t, defaultOptions())

		var source Source

		finishWithin(t, name, func() {
			_, _ = pipe.Ingest(t.Context(), []byte(raw), source)
		})
	}
}

func TestSkipIdenticalHeaders(t *testing.T) {
	fakeParsers(t)

	pipe, st := fixtureStore(t, defaultOptions())
	headers := []byte("From: sender@example.test\r\nSubject: same\r\n")
	firstSrc := Source{Mailbox: "INBOX", UID: 1, UIDValidity: 9, InternalDate: testNow}

	first, err := pipe.Skip(t.Context(), headers, 1000, firstSrc)
	mustSucceed(t, err)

	second, err := pipe.Skip(t.Context(), headers, 1100, Source{
		Mailbox: "INBOX", UID: 2, UIDValidity: 9, InternalDate: testNow,
	})
	mustSucceed(t, err)

	if second.Known || second.MessageID == first.MessageID {
		t.Fatalf("identical headers collapsed: %+v %+v", first, second)
	}

	again, err := pipe.Skip(t.Context(), headers, 1100, Source{
		Mailbox: "INBOX", UID: 2, UIDValidity: 9, InternalDate: testNow,
	})
	mustSucceed(t, err)

	if !again.Known {
		t.Fatal("same mailbox, uidvalidity and uid was stored again")
	}

	otherBox, err := pipe.Skip(t.Context(), headers, 1100, Source{
		Mailbox: "Other", UID: 2, UIDValidity: 9, InternalDate: testNow,
	})
	mustSucceed(t, err)

	if otherBox.Known {
		t.Fatal("mailbox was ignored")
	}

	otherGen, err := pipe.Skip(t.Context(), headers, 1100, Source{
		Mailbox: "INBOX", UID: 2, UIDValidity: 10, InternalDate: testNow,
	})
	mustSucceed(t, err)

	if otherGen.Known {
		t.Fatal("uidvalidity was ignored")
	}

	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM messages"), 4)
	assertEqual(t, scalar[int](t, st, "SELECT uid FROM messages WHERE id=?", first.MessageID), 1)
}

func TestExtractionLimits(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, defaultOptions())
	doc := []byte(sampleXML)
	slab := bytes.Repeat([]byte{' '}, 1<<20)
	repeat := (MaxReportBytes / len(slab)) + 1

	manyEntries := zipFixture(t, slices.Repeat([][]byte{doc}, MaxZipEntries+1), 1)
	wrongEntryCount := bytes.Clone(manyEntries)
	binary.LittleEndian.PutUint16(
		wrongEntryCount[len(wrongEntryCount)-zipEndSize+zipCountOffset:],
		1,
	)

	tests := []struct {
		err  error
		name string
		raw  []byte
	}{
		{name: "gzip bomb", raw: gzipFixture(t, slab, repeat), err: errReportLimit},
		{name: "zip bomb", raw: zipFixture(t, [][]byte{slab}, repeat), err: errReportLimit},
		{name: "wrong zip entry count", raw: wrongEntryCount, err: errZipEntries},
		{
			name: "zip entries",
			raw:  manyEntries,
			err:  errZipEntries,
		},
		{
			name: "nested zip",
			raw:  zipFixture(t, [][]byte{doc, zipFixture(t, [][]byte{doc}, 1)}, 1),
			err:  errNestedArchive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := mustIngest(t, pipe, test.raw)
			assertEqual(t, result.Status, StatusError)
			assertEqual(
				t,
				scalar[string](t, st, "SELECT error FROM messages WHERE id=?", result.MessageID),
				test.err.Error(),
			)
		})
	}

	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports"), 0)

	extract := newExtraction(func(document) error { return nil })
	extract.remaining = int64(len(doc))
	err := extract.part(bytes.NewReader(doc), "", true)
	mustSucceed(t, err)
	err = extract.part(bytes.NewReader(doc), "", true)
	mustSucceed(t, err)
	assertEqual(t, extract.firstError, errMessageLimit)
}

func TestDedupConflictAndReparse(t *testing.T) {
	fakeParsers(t)

	newRecords := make(chan bool, 1)
	options := defaultOptions()
	options.NewIPs = func() { newRecords <- true }
	pipe, st := fixtureStore(t, options)
	original := mustIngest(t, pipe, []byte(sampleXML))

	select {
	case <-newRecords:
	case <-time.After(time.Second):
		t.Fatal("missing post-commit notification")
	}

	gzipCopy := mustIngest(t, pipe, gzipFixture(t, []byte(sampleXML), 1))
	equivalent := mustIngest(t, pipe, []byte(`<feedback count="1" id="a" end="1000"></feedback>`))
	conflict := mustIngest(t, pipe, []byte(`<feedback id="a" end="1000" count="5"/>`))
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM records"), 1)

	for _, result := range []Result{gzipCopy, equivalent, conflict} {
		assertEqual(
			t,
			scalar[int64](
				t,
				st,
				"SELECT duplicate_of FROM reports WHERE message=?",
				result.MessageID,
			),
			original.MessageID,
		)
		assertEqual(
			t,
			scalar[bool](t, st, "SELECT conflict FROM reports WHERE message=?", result.MessageID),
			result == conflict,
		)
	}

	saved := parseAggregate
	parseAggregate = func(raw []byte) (*report.Aggregate, error) {
		assertEqual(t, pipe.Reparsing(), true)

		value, err := saved(raw)
		if value != nil {
			value.Records[0].Count += 10
		}

		return value, err
	}
	ran, err := pipe.ReparseIfNeeded(t.Context())
	mustSucceed(t, err)
	assertEqual(t, ran, true)
	assertEqual(t, scalar[int](t, st, "SELECT count FROM records"), 11)
	assertEqual(t, pipe.Reparsing(), false)
	<-newRecords

	count, err := pipe.Reparse(t.Context())
	mustSucceed(t, err)
	assertEqual(t, count, 4)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports WHERE conflict = 1"), 1)
	ran, err = pipe.ReparseIfNeeded(t.Context())
	mustSucceed(t, err)
	assertEqual(t, ran, false)
}

func TestReportIDsStableAcrossReparse(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, Options{
		Now: func() time.Time { return testNow }, Metrics: nil, NewIPs: nil, RetentionDays: 0,
	})

	reportID := func(messageID int64) int64 {
		t.Helper()

		return scalar[int64](t, st, "SELECT id FROM reports WHERE message=?", messageID)
	}

	first := mustIngest(t, pipe, []byte(`<feedback id="a" end="400000" count="1"/>`))
	expired := mustIngest(t, pipe, []byte(`<feedback id="b" end="1" count="1"/>`))
	third := mustIngest(t, pipe, []byte(`<feedback id="c" end="400000" count="1"/>`))
	duplicate := mustIngest(
		t, pipe, []byte(`<feedback count="1" id="a" end="400000"></feedback>`),
	)

	firstID := reportID(first.MessageID)
	expiredID := reportID(expired.MessageID)
	thirdID := reportID(third.MessageID)
	duplicateID := reportID(duplicate.MessageID)

	assertEqual(
		t,
		scalar[int64](t, st, "SELECT duplicate_of FROM reports WHERE id=?", duplicateID),
		firstID,
	)

	pruned, err := pipe.Prune(t.Context(), 1)
	mustSucceed(t, err)
	assertEqual(t, pruned, 1)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports WHERE id=?", expiredID), 0)

	fresh := mustIngest(t, pipe, []byte(`<feedback id="d" end="400000" count="1"/>`))
	freshID := reportID(fresh.MessageID)

	if freshID == expiredID || freshID <= duplicateID {
		t.Fatalf("new report id %d reuses or precedes old ids (expired %d, duplicate %d)",
			freshID, expiredID, duplicateID)
	}

	_, err = pipe.Reparse(t.Context())
	mustSucceed(t, err)

	assertEqual(t, reportID(first.MessageID), firstID)
	assertEqual(t, reportID(third.MessageID), thirdID)
	assertEqual(t, reportID(duplicate.MessageID), duplicateID)
	assertEqual(t, reportID(fresh.MessageID), freshID)
	assertEqual(
		t,
		scalar[int64](t, st, "SELECT duplicate_of FROM reports WHERE id=?", duplicateID),
		firstID,
	)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports WHERE id=?", expiredID), 0)
}

func TestTLSDomainConflict(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, defaultOptions())
	original := mustIngest(t, pipe, []byte(sampleTLS))
	assertEqual(t, original.Reports, 2)

	saved := parseTLS

	var calls int

	parseTLS = func(raw []byte) (*report.TLS, error) {
		calls++

		return saved(raw)
	}
	changed := strings.Replace(
		sampleTLS,
		`"domain":"b.example"`,
		`"domain":"b.example","success":1`,
		1,
	)
	duplicate := mustIngest(t, pipe, []byte(changed))
	assertEqual(t, calls, 2)
	assertEqual(t, duplicate.Reports, 2)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM tls_policies"), 3)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT domain FROM reports WHERE conflict = 1"),
		"b.example",
	)
}

func TestReparseVersionChange(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, defaultOptions())
	mustIngest(t, pipe, []byte(feedbackFixture()))

	orphan := []byte("old extracted data")
	err := st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		_, err := store.PutBlob(ctx, tx, orphan)
		if err != nil {
			return fmt.Errorf("insert obsolete blob: %w", err)
		}

		err = store.SetMeta(ctx, tx, store.MetaParserVersion, "0")
		if err != nil {
			return fmt.Errorf("set previous parser version: %w", err)
		}

		return nil
	})
	mustSucceed(t, err)
	ran, err := pipe.ReparseIfNeeded(t.Context())
	mustSucceed(t, err)
	assertEqual(t, ran, true)
	assertEqual(
		t,
		scalar[int](t, st, "SELECT count(*) FROM blobs WHERE sha256=?", store.Digest(orphan)),
		0,
	)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM failure_reports"), 1)
}

func TestImport(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(t, defaultOptions())
	dir := t.TempDir()
	mustSucceed(t, os.Mkdir(filepath.Join(dir, "reports"), 0o700))

	dated := "Date: " + sampleDate + "\r\nSubject: dated\r\n\r\n"
	mbox := "From sender date\n" + dated + ">From escaped\nFrom sender date\nSubject: second\n\nbody\n"

	files := map[string][]byte{
		"reports/doc.xml.gz": gzipFixture(t, []byte(sampleXML), 1),
		"reports/bad.xml":    []byte("<feedback/>"),
		"reports/tls.json":   []byte(sampleTLS),
		"reports/ignore.txt": []byte("ignored"),
		"first.eml":          []byte(dated + sampleXML),
		"mailbox":            []byte(mbox),
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		mustSucceed(t, os.WriteFile(path, data, 0o600))
		mustSucceed(t, os.Chtimes(path, testNow, testNow))
	}

	summary, err := pipe.Import(t.Context(), []string{dir})
	if !errors.Is(err, errImportReport) {
		t.Fatalf("import error = %v, want a recorded parse error", err)
	}

	assertEqual(t, summary, ImportSummary{Files: 5, Messages: 6, Failed: 1, Known: 0})
	assertEqual(
		t,
		scalar[int64](t, st, "SELECT min(internaldate) FROM messages WHERE subject=''"),
		testNow.Unix(),
	)
	assertEqual(
		t,
		scalar[int64](t, st, "SELECT min(internaldate) FROM messages WHERE subject='dated'"),
		1748952000,
	)
	assertEqual(t, scalar[bool](t, st, "SELECT EXISTS(SELECT 1 FROM messages WHERE sha256=?)",
		store.Digest([]byte(dated+"From escaped\n"))), true)

	summary, err = pipe.Import(t.Context(), []string{dir})
	mustSucceed(t, err)
	assertEqual(t, summary.Known, 6)
}

func TestPruneAndReplay(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(
		t,
		Options{
			Now: func() time.Time { return testNow }, Metrics: nil, NewIPs: nil, RetentionDays: 1,
		},
	)
	ran, err := pipe.ReparseIfNeeded(t.Context())
	mustSucceed(t, err)
	assertEqual(t, ran, false)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT value FROM meta WHERE key=?", store.MetaParserVersion),
		strconv.Itoa(report.ParserVersion),
	)

	old := mustIngest(t, pipe, []byte(oldXML))
	dupe := mustIngest(t, pipe, []byte(`<feedback id="old" end="400000" count="1"/>`))
	mixedRaw := []byte(multipart("mixed", "sample",
		attachment("application/xml", []byte(oldXML)),
		attachment("application/xml", []byte(`<feedback id="survivor" end="400000" count="1"/>`)),
		feedbackFixture()))
	mixed := mustIngest(t, pipe, mixedRaw)

	count, err := pipe.Prune(t.Context(), 0)
	mustSucceed(t, err)
	assertEqual(t, count, 0)
	count, err = pipe.Prune(t.Context(), 1)
	mustSucceed(t, err)
	// The expired copy inside the mixed message and the failure report go;
	// the expired original stays because its conflicting copy is recent.
	assertEqual(t, count, 2)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports"), 3)
	assertEqual(
		t,
		scalar[int](t, st, "SELECT count(*) FROM messages WHERE status=?", StatusPruned),
		0,
	)
	assertEqual(t, old.Status, StatusOK)
	assertEqual(t, dupe.Status, StatusOK)
	assertEqual(
		t,
		mustIngest(t, pipe, mixedRaw),
		Result{Known: true, Class: "", Status: "", MessageID: 0, Reports: 0},
	)

	count, err = pipe.Reparse(t.Context())
	mustSucceed(t, err)
	assertEqual(t, count, 3)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports"), 3)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM failure_reports"), 0)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT status FROM messages WHERE id=?", mixed.MessageID),
		StatusOK,
	)
}

func TestPruneWholeMessage(t *testing.T) {
	fakeParsers(t)
	pipe, st := fixtureStore(
		t,
		Options{
			Now: func() time.Time { return testNow }, Metrics: nil, NewIPs: nil, RetentionDays: 1,
		},
	)

	old := mustIngest(t, pipe, []byte(oldXML))

	count, err := pipe.Prune(t.Context(), 1)
	mustSucceed(t, err)
	assertEqual(t, count, 1)
	assertEqual(
		t,
		scalar[string](t, st, "SELECT status FROM messages WHERE id=? AND blob IS NULL",
			old.MessageID),
		StatusPruned,
	)

	count, err = pipe.Reparse(t.Context())
	mustSucceed(t, err)
	assertEqual(t, count, 0)
	assertEqual(t, scalar[int](t, st, "SELECT count(*) FROM reports"), 0)
}

func TestFailureWithoutArrivalUsesInternalDate(t *testing.T) {
	fakeParsers(t)

	saved := parseFailure
	parseFailure = func(feedback, headers []byte) (*report.Failure, error) {
		value, err := saved(feedback, headers)
		if err == nil {
			value.Arrival = 0
		}

		return value, err
	}

	pipe, st := fixtureStore(t, defaultOptions())
	mustIngest(t, pipe, []byte(feedbackFixture()))

	assertEqual(
		t,
		scalar[int64](t, st, "SELECT arrival FROM failure_reports"),
		scalar[int64](t, st, "SELECT internaldate FROM messages"),
	)

	count, err := pipe.Prune(t.Context(), 1)
	mustSucceed(t, err)
	assertEqual(t, count, 0)
}
