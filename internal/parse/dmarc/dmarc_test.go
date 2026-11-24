package dmarc

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

var aggregates = []string{"google.xml", "outlook.xml", "yahoo.xml", "quirks.xml", "dmarc-2.0.xml"}

func fixture(tb testing.TB, group, name string) []byte {
	tb.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", group, name))
	if err != nil {
		tb.Fatal(err)
	}

	return data
}

func TestAggregateFixtures(t *testing.T) {
	tests := []struct {
		name, schema, org, id, domain, policy string
		pct, records                          int
		end                                   int64
	}{
		{
			"google.xml",
			"rfc7489",
			"Google",
			"12345678901234567890",
			"example.com",
			"reject",
			100,
			2,
			1704153599,
		},
		{
			"outlook.xml",
			"rfc7489",
			"Outlook",
			"6e5406ef-2518-44b0-be4f-aaf94f0d8880",
			"example.org",
			"reject",
			100,
			1,
			1704153600,
		},
		{
			"yahoo.xml",
			"rfc7489",
			"Yahoo",
			"1704067200-example.net",
			"example.net",
			"quarantine",
			25,
			1,
			1704153599,
		},
		{
			"quirks.xml",
			"rfc7489",
			"Example Receiver",
			"quirk-1",
			"example.com",
			"none",
			0,
			1,
			1704153599,
		},
		{
			"dmarc-2.0.xml",
			"dmarc-2.0",
			"Example Receiver",
			"bis-1",
			"example.org",
			"reject",
			-1,
			1,
			1704153599,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFixture(t, tt.name)
			if got.Schema != tt.schema || got.OrgName != tt.org || got.ReportID != tt.id ||
				got.Domain != tt.domain || got.Begin != 1704067200 || got.End != tt.end {
				t.Errorf("unexpected metadata: %+v", got)
			}

			pct := -1
			if got.Policy.Pct != nil {
				pct = *got.Policy.Pct
			}

			if got.Policy.P != tt.policy || pct != tt.pct || len(got.Records) != tt.records {
				t.Errorf("unexpected policy or record count: %+v", got)
			}
		})
	}
}

func parseFixture(t *testing.T, name string) *report.Aggregate {
	t.Helper()

	got, err := ParseAggregate(fixture(t, "dmarc", name))
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestAggregateGoogleRecords(t *testing.T) {
	got := parseFixture(t, "google.xml")

	want := []report.Record{
		{
			SourceIP: "192.0.2.10", Count: 42, Disposition: "none", DKIM: "pass", SPF: "fail",
			HeaderFrom: "example.com", EnvelopeFrom: "", EnvelopeTo: "", Reasons: nil,
			Auth: []report.AuthResult{
				{
					Method:      "dkim",
					Domain:      "example.com",
					Selector:    "mail",
					Scope:       "",
					Result:      "pass",
					HumanResult: "",
				},
				{
					Method:      "dkim",
					Domain:      "example.net",
					Selector:    "backup",
					Scope:       "",
					Result:      "fail",
					HumanResult: "",
				},
				{
					Method:      "spf",
					Domain:      "example.net",
					Selector:    "",
					Scope:       "mfrom",
					Result:      "pass",
					HumanResult: "",
				},
			},
		},
		{
			SourceIP:     "192.0.2.11",
			Count:        1,
			HeaderFrom:   "example.com",
			Disposition:  "none",
			DKIM:         "fail",
			SPF:          "fail",
			EnvelopeFrom: "",
			EnvelopeTo:   "",
			Reasons:      nil,
			Auth:         nil,
		},
	}
	if !reflect.DeepEqual(got.Records, want) {
		t.Errorf("records = %+v, want %+v", got.Records, want)
	}

	wantPolicy := report.Policy{
		P: "reject", SP: "quarantine", ADKIM: "r", ASPF: "s", FO: "1",
		NP: "", Pct: new(100), Testing: "", Discovery: "",
	}
	if !reflect.DeepEqual(got.Policy, wantPolicy) || got.Email != "dmarc-noreply@example.net" ||
		got.ExtraContact != "https://reports.example.net/help" {
		t.Errorf("unexpected policy or contact: %+v", got)
	}
}

func TestAggregateOutlookRecord(t *testing.T) {
	got := parseFixture(t, "outlook.xml")

	want := report.Record{
		SourceIP: "2001:db8::20", Count: 1, Disposition: "none", DKIM: "fail", SPF: "fail",
		HeaderFrom: "example.org", EnvelopeFrom: "example.org", EnvelopeTo: "example.net",
		Reasons: []report.Reason{{Type: "local_policy", Comment: "Delivered by receiver policy"}},
		Auth: []report.AuthResult{
			{
				Method:      "spf",
				Domain:      "example.org",
				Selector:    "",
				Scope:       "helo",
				Result:      "softfail",
				HumanResult: "",
			},
		},
	}
	if len(got.Records) != 1 || !reflect.DeepEqual(got.Records[0], want) {
		t.Errorf("records = %+v, want %+v", got.Records, want)
	}
}

func TestAggregateQuirks(t *testing.T) {
	got := parseFixture(t, "quirks.xml")

	want := report.Record{
		SourceIP:     "203.0.113.40",
		Count:        report.MaxCount,
		Disposition:  "pass",
		DKIM:         "pass",
		SPF:          "fail",
		HeaderFrom:   "example.com",
		EnvelopeFrom: "",
		EnvelopeTo:   "",
		Reasons: []report.Reason{
			{Type: "forwarded", Comment: "via example.org"},
			{Type: "other", Comment: ""},
		},
		Auth: []report.AuthResult{
			{
				Method:      "dkim",
				Domain:      "example.com",
				Selector:    "Mail",
				Scope:       "",
				Result:      "pass",
				HumanResult: "signature verified",
			},
			{
				Method:      "spf",
				Domain:      "example.net",
				Selector:    "",
				Scope:       "",
				Result:      "none",
				HumanResult: "",
			},
		},
	}
	if len(got.Records) != 1 || !reflect.DeepEqual(got.Records[0], want) {
		t.Errorf("records = %+v, want %+v", got.Records, want)
	}
}

func TestAggregateDMARC2Policy(t *testing.T) {
	data := fixture(t, "dmarc", "dmarc-2.0.xml")
	want := report.Policy{
		P: "reject", SP: "quarantine", NP: "reject", Pct: nil, ADKIM: "", ASPF: "", FO: "",
		Testing: "y", Discovery: "treewalk",
	}

	for _, input := range []string{
		string(data),
		strings.ReplaceAll(string(data), "<testing>y</testing>", "<t> Y </t><pct>unused</pct>"),
	} {
		got, err := ParseAggregate([]byte(input))
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(got.Policy, want) {
			t.Errorf("policy = %+v, want %+v", got.Policy, want)
		}
	}
}

func TestAggregateRejectsInvalidDocuments(t *testing.T) {
	base := string(fixture(t, "dmarc", "quirks.xml"))

	tests := map[string]string{
		"doctype": `<!DOCTYPE feedback>` + base,
		"entities": `<!DOCTYPE feedback [<!ENTITY ext SYSTEM "file:///example.org">]>` + strings.Replace(
			base,
			"quirk-1",
			"&ext;",
			1,
		),
		"deep nesting": strings.Replace(
			base,
			"</feedback>",
			strings.Repeat("<x>", 200)+strings.Repeat("</x>", 200)+"</feedback>",
			1,
		),
		"trailing XML":  base + "<feedback/>",
		"trailing text": base + "garbage",
		"missing org": strings.Replace(
			base,
			"<org_name>  Example Receiver </org_name>",
			"",
			1,
		),
		"missing ID":       strings.Replace(base, "<report_id>  quirk-1 </report_id>", "", 1),
		"missing domain":   strings.Replace(base, "<domain> EXAMPLE.COM </domain>", "", 1),
		"missing begin":    strings.Replace(base, "<begin> 1704067200 </begin>", "", 1),
		"missing end":      strings.Replace(base, "<end> 1704153599 </end>", "", 1),
		"reversed dates":   strings.Replace(base, "1704067200", "1704153600", 1),
		"invalid IP":       strings.Replace(base, "203.0.113.40", "203.0.113.400", 1),
		"scoped IPv6":      strings.Replace(base, "203.0.113.40", "2001:db8::20%zone", 1),
		"negative count":   strings.Replace(base, "1099511627776", "-1", 1),
		"overflowed count": strings.Replace(base, "1099511627776", "1099511627777", 1),
		"missing row":      strings.NewReplacer("<row>", "", "</row>", "").Replace(base),
		"missing policy_evaluated": strings.NewReplacer(
			"<policy_evaluated>", "", "</policy_evaluated>", "",
		).Replace(base),
		"missing evaluated dkim": strings.Replace(base, "<dkim> PASS </dkim>", "", 1),
		"missing evaluated spf":  strings.Replace(base, "<spf> FAIL </spf>", "", 1),
		"missing count":          strings.Replace(base, "<count> 1099511627776 </count>", "", 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseAggregate([]byte(input))
			if err == nil || got != nil {
				t.Fatalf("ParseAggregate = (%v, %v), want an error and no report", got, err)
			}
		})
	}
}

func TestAggregateDateRangeBounds(t *testing.T) {
	base := string(fixture(t, "dmarc", "quirks.xml"))

	reject := map[string]string{
		"begin before 1990": strings.Replace(base, "1704067200", "631151999", 1),
		"end at 2200":       strings.Replace(base, "1704153599", "7258118400", 1),
		"begin at 2200": strings.Replace(
			strings.Replace(base, "1704067200", "7258118400", 1),
			"1704153599", "7258118400", 1,
		),
	}
	for name, input := range reject {
		t.Run(name, func(t *testing.T) {
			got, err := ParseAggregate([]byte(input))
			if !errors.Is(err, errDateRange) || got != nil {
				t.Fatalf("ParseAggregate = (%v, %v), want date range error", got, err)
			}
		})
	}

	accept := map[string]string{
		"begin at 1990":   strings.Replace(base, "1704067200", "631152000", 1),
		"end before 2200": strings.Replace(base, "1704153599", "7258118399", 1),
	}
	for name, input := range accept {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAggregate([]byte(input))
			if err != nil {
				t.Fatalf("ParseAggregate error = %v, want nil", err)
			}
		})
	}
}

func TestAggregateRecordLimit(t *testing.T) {
	base := string(fixture(t, "dmarc", "quirks.xml"))
	input := strings.Replace(
		base,
		"</feedback>",
		strings.Repeat("<record/>", report.MaxRecords)+"</feedback>",
		1,
	)

	got, err := ParseAggregate([]byte(input))
	if !errors.Is(err, errTooManyRecords) || got != nil {
		t.Fatalf("ParseAggregate = (%v, %v), want a record limit error", got, err)
	}
}

func TestAggregateSanitizesText(t *testing.T) {
	base := string(fixture(t, "dmarc", "quirks.xml"))
	base = strings.Replace(base, "quirk-1", "id\xff\xfe", 1)
	base = strings.Replace(base, "Example Receiver", strings.Repeat("é", report.MaxMedium), 1)
	base = strings.Replace(base, "via example.org", strings.Repeat("€", report.MaxLong), 1)
	base = strings.Replace(base, "EXAMPLE.COM", strings.Repeat("Ö", report.MaxShort), 1)

	got, err := ParseAggregate([]byte("\uFEFF" + base))
	if err != nil {
		t.Fatal(err)
	}

	if got.ReportID != "id�" || len(got.OrgName) != report.MaxMedium ||
		len(got.Domain) != report.MaxShort-1 ||
		!utf8.ValidString(got.Domain) {
		t.Errorf(
			"unexpected sanitized metadata: ID %q, org bytes %d, domain bytes %d",
			got.ReportID,
			len(got.OrgName),
			len(got.Domain),
		)
	}

	comment := got.Records[0].Reasons[0].Comment
	if len(comment) != report.MaxLong-1 || !utf8.ValidString(comment) {
		t.Errorf("unexpected comment length or encoding: %d bytes", len(comment))
	}
}

func FuzzParseAggregate(f *testing.F) {
	for _, name := range aggregates {
		f.Add(fixture(f, "dmarc", name))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseAggregate(data)
	})
}
