package tlsrpt

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zekihan/rua/internal/report"
)

func fixture(tb testing.TB, name string) []byte {
	tb.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "tlsrpt", name))
	if err != nil {
		tb.Fatal(err)
	}

	return data
}

func TestTLSFixtures(t *testing.T) {
	tests := []struct {
		name string
		want report.TLS
	}{
		{"google.json", report.TLS{
			OrgName: "Google Inc.", ContactInfo: "smtp-tls-reporting@example.net",
			ReportID: "2024-01-01T00:00:00Z_example.com", Begin: 1704067200, End: 1704153599,
			Policies: []report.TLSPolicy{
				{
					Type:    "sts",
					Domain:  "example.com",
					Success: 0,
					Failure: 4,
					Strings: nil,
					MXHosts: nil,
					Failures: []report.TLSFailure{
						{
							ResultType:          "sts-policy-fetch-error",
							SendingMTAIP:        "192.0.2.10",
							FailedSessions:      4,
							ReceivingIP:         "",
							ReceivingMXHostname: "",
							ReceivingMXHelo:     "",
							FailureReasonCode:   "",
							AdditionalInfoURI:   "",
						},
					},
				},
			},
		}},
		{"microsoft.json", report.TLS{
			OrgName: "Microsoft Corporation", ContactInfo: "https://postmaster.example.net/",
			ReportID: "example.org:2024-01-01", Begin: 1704067200, End: 1704153600,
			Policies: []report.TLSPolicy{
				{
					Type:    "sts",
					Domain:  "example.org",
					Success: 5000000000,
					Failure: 2,
					Strings: []string{
						"version: STSv1",
						"mode: enforce",
						"mx: *.mail.example.org",
						"max_age: 86400",
					},
					MXHosts: []string{"*.mail.example.org", "mx.example.org"},
					Failures: []report.TLSFailure{
						{
							ResultType:          "certificate-expired",
							SendingMTAIP:        "2001:db8::20",
							ReceivingIP:         "203.0.113.20",
							ReceivingMXHostname: "mx.mail.example.org",
							ReceivingMXHelo:     "mx.mail.example.org",
							FailedSessions:      2,
							FailureReasonCode:   "certificate has expired",
							AdditionalInfoURI:   "https://postmaster.example.net/cert",
						},
					},
				},
				{
					Type: "no-policy-found", Domain: "example.net", Success: 9, Failure: 0,
					MXHosts: []string{"mx.example.net"}, Strings: nil, Failures: nil,
				},
			},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(fixture(t, tt.name))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("TLS report = %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestTLSRejectsInvalidDocuments(t *testing.T) {
	base := string(fixture(t, "google.json"))

	const depth = 10001

	nested := strings.Repeat("[", depth) + strings.Repeat("]", depth)

	tests := map[string]string{
		"no organization": strings.Replace(base, "organization-name", "unknown", 1),
		"no report ID":    strings.Replace(base, "report-id", "unknown", 1),
		"no date range":   strings.Replace(base, "date-range", "unknown", 1),
		"missing start":   strings.Replace(base, "start-datetime", "unknown", 1),
		"missing end":     strings.Replace(base, "end-datetime", "unknown", 1),
		"reversed dates":  strings.Replace(base, "2024-01-01T23:59:59Z", "2023-01-01T00:00:00Z", 1),
		"no policies":     strings.Replace(base, "policies", "unknown", 1),
		"no domain":       strings.Replace(base, "policy-domain", "unknown", 1),
		"negative count": strings.Replace(
			base,
			`"failed-session-count": 4`,
			`"failed-session-count": -1`,
			1,
		),
		"invalid detail": strings.Replace(
			base,
			`"failure-details":`,
			`"failure-details": {}, "ignored":`,
			1,
		),
		"deep nesting": `{"unknown":` + nested + "," + strings.TrimPrefix(base, "{"),
		"no summary":   strings.Replace(base, `"summary":`, `"unknown":`, 1),
		"missing successful count": strings.Replace(
			base, `"total-successful-session-count": 0,`, ``, 1,
		),
		"missing failure count": strings.Replace(
			base, `, "total-failure-session-count": 4`, ``, 1,
		),
		"missing failed session count": strings.Replace(
			base, `"failed-session-count": 4`, `"unknown": 4`, 1,
		),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Parse([]byte(input))
			if err == nil || got != nil {
				t.Fatalf("Parse = (%v, %v), want an error and no report", got, err)
			}
		})
	}
}

func TestTLSDateRangeBounds(t *testing.T) {
	base := string(fixture(t, "google.json"))

	reject := map[string]string{
		"start before 1990": strings.Replace(
			base, "2024-01-01T00:00:00Z", "1989-12-31T23:59:59Z", 1,
		),
		"end at 2200": strings.Replace(
			base, "2024-01-01T23:59:59Z", "2200-01-01T00:00:00Z", 1,
		),
		"start at 2200": strings.Replace(
			strings.Replace(base, "2024-01-01T00:00:00Z", "2200-01-01T00:00:00Z", 1),
			"2024-01-01T23:59:59Z", "2200-01-02T00:00:00Z", 1,
		),
	}
	for name, input := range reject {
		t.Run(name, func(t *testing.T) {
			got, err := Parse([]byte(input))
			if !errors.Is(err, errDateRange) || got != nil {
				t.Fatalf("Parse = (%v, %v), want date range error", got, err)
			}
		})
	}

	accept := map[string]string{
		"start at 1990": strings.Replace(
			base, "2024-01-01T00:00:00Z", "1990-01-01T00:00:00Z", 1,
		),
		"end before 2200": strings.Replace(
			base, "2024-01-01T23:59:59Z", "2199-12-31T23:59:59Z", 1,
		),
	}
	for name, input := range accept {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(input))
			if err != nil {
				t.Fatalf("Parse error = %v, want nil", err)
			}
		})
	}
}

func TestTLSFailureDetailLimit(t *testing.T) {
	base := string(fixture(t, "google.json"))
	input := strings.Replace(base, `"failure-details": [`,
		`"failure-details": [`+strings.Repeat("{},", report.MaxRecords), 1)

	got, err := Parse([]byte(input))
	if !errors.Is(err, errTooManyFailures) || got != nil {
		t.Fatalf("Parse = (%v, %v), want a failure detail limit error", got, err)
	}
}

func TestTLSSanitizesText(t *testing.T) {
	base := string(fixture(t, "microsoft.json"))
	base = strings.Replace(base, "Microsoft Corporation", "Microsoft\xff", 1)
	base = strings.Replace(base, "EXAMPLE.ORG", strings.Repeat("Ö", report.MaxShort), 1)
	base = strings.Replace(base, "version: STSv1", strings.Repeat("€", report.MaxLong), 1)
	base = strings.Replace(
		base,
		"https://postmaster.example.net/",
		strings.Repeat("é", report.MaxMedium),
		1,
	)
	base = strings.Replace(base, "additional-information", "additional-info-uri", 1)

	got, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}

	policy := got.Policies[0]
	if got.OrgName != "Microsoft�" || len(got.ContactInfo) != report.MaxMedium ||
		len(policy.Domain) != report.MaxShort-1 || !utf8.ValidString(policy.Domain) {
		t.Errorf("unexpected sanitized metadata: org %q, contact bytes %d, policy domain bytes %d",
			got.OrgName, len(got.ContactInfo), len(policy.Domain))
	}

	line := policy.Strings[0]
	if len(line) != report.MaxLong-1 || !utf8.ValidString(line) {
		t.Errorf("unexpected policy line length or encoding: %d bytes", len(line))
	}

	if uri := policy.Failures[0].AdditionalInfoURI; uri != "https://postmaster.example.net/cert" {
		t.Errorf("AdditionalInfoURI = %q", uri)
	}
}

func FuzzParseTLS(f *testing.F) {
	for _, name := range []string{"google.json", "microsoft.json"} {
		f.Add(fixture(f, name))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(data)
	})
}
