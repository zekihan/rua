package dmarc

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zekihan/rua/internal/report"
)

func TestFailureFixtures(t *testing.T) {
	tests := []struct {
		name, headers string
		want          report.Failure
	}{
		{
			"dmarc.arf",
			"dmarc.headers",
			report.Failure{
				Domain:       "example.com",
				SourceIP:     "198.51.100.10",
				AuthFailure:  "dmarc",
				Arrival:      1704070923,
				OriginalFrom: "José <sender@example.com>",
				OriginalTo:   "Zoë <recipient@example.net>",
				Subject:      "Account update",
			},
		},
		{"spf.arf", "", report.Failure{
			Domain: "example.org", SourceIP: "2001:db8::12", AuthFailure: "spf",
			Arrival: 0, OriginalFrom: "", OriginalTo: "", Subject: "",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var headers []byte

			if tt.headers != "" {
				headers = fixture(t, "failure", tt.headers)
			}

			got, err := ParseFailure(fixture(t, "failure", tt.name), headers)
			if err != nil {
				t.Fatal(err)
			}

			if *got != tt.want {
				t.Errorf("failure = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFailureRejectsInvalidFeedback(t *testing.T) {
	for _, feedback := range []string{"", "Feedback-Type: abuse", "Feedback-Type auth-failure"} {
		t.Run(feedback, func(t *testing.T) {
			got, err := ParseFailure([]byte(feedback), nil)
			if err == nil || got != nil {
				t.Fatalf("ParseFailure = (%v, %v), want an error and no report", got, err)
			}
		})
	}
}

func TestFailureSanitizesHeaders(t *testing.T) {
	feedback := []byte("Feedback-Type: auth-failure\r\nReported-Domain: example.com\xff")
	headers := []byte(
		"Subject: " + strings.Repeat("€", report.MaxMedium) + "\r\nFrom: me\xff@example.net",
	)

	got, err := ParseFailure(feedback, headers)
	if err != nil {
		t.Fatal(err)
	}

	if got.Domain != "example.com�" || got.OriginalFrom != "me�@example.net" ||
		len(got.Subject) != report.MaxMedium-1 || !utf8.ValidString(got.Subject) {
		t.Errorf(
			"unexpected sanitized headers: domain %q, from %q, subject bytes %d",
			got.Domain,
			got.OriginalFrom,
			len(got.Subject),
		)
	}
}

func FuzzParseFailure(f *testing.F) {
	f.Add(fixture(f, "failure", "dmarc.arf"), fixture(f, "failure", "dmarc.headers"))
	f.Add(fixture(f, "failure", "spf.arf"), []byte(nil))

	f.Fuzz(func(t *testing.T, feedback, headers []byte) {
		_, _ = ParseFailure(feedback, headers)
	})
}
