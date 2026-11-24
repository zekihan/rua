package query

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/zekihan/go-commons/metrics"

	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/sources"
	"github.com/zekihan/rua/internal/store"
	"github.com/zekihan/rua/internal/telemetry"
)

var testNow = time.Date(2025, time.June, 15, 12, 0, 0, 0, time.UTC)

func unixTime(year int, month time.Month, day, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC).Unix()
}

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()

	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func eqSlice[T comparable](t *testing.T, got, want []T) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func aggregateXML(
	org, reportID, domain string, begin, end int64, policyP string, pct int,
	ip string, count int64, evalDKIM, evalSPF, disposition string,
	headerFrom, dkimDomain, dkimResult, dkimSelector string,
	reasonType string,
) string {
	reason := ""
	if reasonType != "" {
		reason = "<reason><type>" + reasonType + "</type><comment>x</comment></reason>"
	}

	authDKIM := ""
	if dkimDomain != "" {
		authDKIM = "<dkim><domain>" + dkimDomain + "</domain><selector>" + dkimSelector +
			"</selector><result>" + dkimResult + "</result></dkim>"
	}

	return fmt.Sprintf(
		`<feedback><report_metadata><org_name>%s</org_name>`+
			`<email>reports@example.net</email><report_id>%s</report_id>`+
			`<date_range><begin>%d</begin><end>%d</end></date_range></report_metadata>`+
			`<policy_published><domain>%s</domain><adkim>r</adkim><aspf>r</aspf>`+
			`<p>%s</p><sp>%s</sp><pct>%d</pct></policy_published>`+
			`<record><row><source_ip>%s</source_ip><count>%d</count>`+
			`<policy_evaluated><disposition>%s</disposition><dkim>%s</dkim><spf>%s</spf>%s</policy_evaluated></row>`+
			`<identifiers><header_from>%s</header_from><envelope_from>%s</envelope_from>`+
			`<envelope_to>dest.example.net</envelope_to></identifiers>`+
			`<auth_results>%s<spf><domain>%s</domain><result>%s</result></spf></auth_results></record></feedback>`,
		org,
		reportID,
		begin,
		end,
		domain,
		policyP,
		policyP,
		pct,
		ip,
		count,
		disposition,
		evalDKIM,
		evalSPF,
		reason,
		headerFrom,
		headerFrom,
		authDKIM,
		headerFrom,
		evalSPF,
	)
}

func tlsJSON(
	org, reportID string,
	begin, end time.Time,
	domain string,
	success, failure int64,
) string {
	return fmt.Sprintf(
		`{"organization-name":%q,"contact-info":"tls@example.net",`+
			`"report-id":%q,"date-range":{"start-datetime":%q,"end-datetime":%q},`+
			`"policies":[{"policy":{"policy-type":"sts","policy-domain":%q,`+
			`"policy-string":["mode: enforce","mx: mx.example.com"],"mx-host":["mx.example.com"]},`+
			`"summary":{"total-successful-session-count":%d,"total-failure-session-count":%d},`+
			`"failure-details":[{"result-type":"certificate-expired","sending-mta-ip":"192.0.2.9",`+
			`"receiving-ip":"198.51.100.7","receiving-mx-hostname":"mx.example.com",`+
			`"failure-reason-code":"5.7.26","failed-session-count":%d}]}]}`,
		org,
		reportID,
		begin.Format(time.RFC3339),
		end.Format(time.RFC3339),
		domain,
		success,
		failure,
		failure,
	)
}

type fixture struct {
	svc *Service
	st  *store.Store
}

func newFixture(t *testing.T, set *sources.Set) *fixture {
	t.Helper()

	st, err := store.Open(t.Context(), store.Memory)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = st.Close() })

	var deps Deps

	deps.Store = st
	deps.Sources = func() *sources.Set { return set }
	deps.Fetch = nil
	deps.Reparsing = nil
	deps.EnrichQueue = nil
	deps.Now = func() time.Time { return testNow }
	deps.StaleAfter = 3 * time.Hour
	deps.IMAPEnabled = true
	deps.Mailboxes = []string{"INBOX"}

	return &fixture{svc: New(deps), st: st}
}

func (fx *fixture) ingest(t *testing.T, raw string) {
	t.Helper()

	var opts ingest.Options

	opts.Metrics = nil
	opts.Now = func() time.Time { return testNow }
	opts.NewIPs = nil
	opts.RetentionDays = 0

	pipe := ingest.New(fx.st, opts)

	var src ingest.Source

	_, err := pipe.Ingest(t.Context(), []byte(raw), src)
	if err != nil {
		t.Fatal(err)
	}
}

func (fx *fixture) filter(t *testing.T, rawQuery string) Filter {
	t.Helper()

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatal(err)
	}

	filter, err := fx.svc.ParseFilter(values)
	if err != nil {
		t.Fatal(err)
	}

	return filter
}

func (fx *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()

	err := fx.st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		_, execErr := tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return fmt.Errorf("exec: %w", execErr)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedBasic(t *testing.T, fx *fixture) {
	t.Helper()
	fx.ingest(t, aggregateXML("Google", "r-pass", "example.com",
		unixTime(2025, time.June, 9, 0), unixTime(2025, time.June, 11, 0), "none", 100,
		"192.0.2.1", 10, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))
	fx.ingest(t, aggregateXML("Google", "r-fail", "example.com",
		unixTime(2025, time.June, 11, 0), unixTime(2025, time.June, 13, 0), "reject", 100,
		"192.0.2.2", 5, "fail", "fail", "reject", "example.com", "other.net", "pass", "s1", ""))

	dup := strings.Replace(
		aggregateXML(
			"Google",
			"r-pass",
			"example.com",
			unixTime(2025, time.June, 9, 0),
			unixTime(2025, time.June, 11, 0),
			"none",
			100,
			"192.0.2.1",
			10,
			"pass",
			"fail",
			"none",
			"example.com",
			"example.com",
			"pass",
			"s1",
			"",
		),
		"><", ">\n<", 1)
	fx.ingest(t, dup)
}

func TestVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dkim    string
		spf     string
		header  string
		adkim   string
		want    string
		domains []string
		reasons []Reason
	}{
		{
			name:    "eval dkim pass",
			dkim:    "pass",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictPass,
			domains: nil,
			reasons: nil,
		},
		{
			name:    "eval spf pass",
			dkim:    "fail",
			spf:     "pass",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictPass,
			domains: nil,
			reasons: nil,
		},
		{
			name:    "dkim exact",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "s",
			want:    VerdictForwarded,
			domains: []string{"example.com"},
			reasons: nil,
		},
		{
			name:    "dkim sub relaxed",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictForwarded,
			domains: []string{"mail.example.com"},
			reasons: nil,
		},
		{
			name:    "dkim sub strict",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "s",
			want:    VerdictFail,
			domains: []string{"mail.example.com"},
			reasons: nil,
		},
		{
			name:    "dkim default relaxed",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "",
			want:    VerdictForwarded,
			domains: []string{"mail.example.com"},
			reasons: nil,
		},
		{
			name:    "dkim other",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictFail,
			domains: []string{"other.net"},
			reasons: nil,
		},
		{
			name:    "reason forwarded",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictForwarded,
			domains: nil,
			reasons: []Reason{{Type: "forwarded", Comment: ""}},
		},
		{
			name:    "reason mailing list",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictForwarded,
			domains: nil,
			reasons: []Reason{{Type: "mailing_list", Comment: ""}},
		},
		{
			name:    "reason other",
			dkim:    "fail",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictFail,
			domains: nil,
			reasons: []Reason{{Type: "local_policy", Comment: ""}},
		},
		{
			name:    "pass wins",
			dkim:    "pass",
			spf:     "fail",
			header:  "example.com",
			adkim:   "r",
			want:    VerdictPass,
			domains: nil,
			reasons: []Reason{{Type: "forwarded", Comment: ""}},
		},
		{
			name:    "empty header",
			dkim:    "fail",
			spf:     "fail",
			header:  "",
			adkim:   "r",
			want:    VerdictFail,
			domains: []string{"example.com"},
			reasons: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			eq(
				t,
				recordVerdict(
					test.dkim,
					test.spf,
					test.header,
					test.adkim,
					test.domains,
					test.reasons,
				),
				test.want,
			)
		})
	}
}

func expectBadRequest(t *testing.T, fx *fixture, values url.Values) {
	t.Helper()

	_, err := fx.svc.ParseFilter(values)
	eq(t, errors.Is(err, ErrBadRequest), true)
}

func TestParseFilter(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	expectBadRequest(t, fx, url.Values{"tz": {"Local"}})
	expectBadRequest(t, fx, url.Values{"tz": {"Not/AZone"}})
	expectBadRequest(t, fx, url.Values{"from": {"2025-02-30"}})
	expectBadRequest(t, fx, url.Values{"from": {"2025-06-10"}, "to": {"2025-06-01"}})

	filter, err := fx.svc.ParseFilter(url.Values{})
	if err != nil {
		t.Fatal(err)
	}

	eq(t, filter.From, "2025-05-17")
	eq(t, filter.To, "2025-06-15")
	eq(t, filter.TZ.String(), "UTC")
	eq(t, filter.Limit, 50)
}

func overviewFor(t *testing.T, fx *fixture, rawQuery string) Overview {
	t.Helper()

	overview, err := fx.svc.Overview(t.Context(), fx.filter(t, rawQuery))
	if err != nil {
		t.Fatal(err)
	}

	return overview
}

func TestOverviewTotals(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	seedBasic(t, fx)

	overview := overviewFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC")
	eq(t, overview.Totals.Messages, int64(15))
	eq(t, overview.Totals.Pass, int64(10))
	eq(t, overview.Totals.Fail, int64(5))
	eq(t, overview.Totals.Reports, int64(2))
	eq(t, overview.Totals.Rejected, int64(5))
	eq(t, len(overview.Days), 15)
	eq(t, len(overview.TopFailing), 1)
	eq(t, overview.TopFailing[0].Fail, int64(5))
	eq(t, len(overview.NewUnknown), 2)

	if overview.Totals.PassRate == nil {
		t.Fatal("passRate nil")
	}

	eq(t, *overview.Totals.PassRate, 10.0/15.0)
}

func TestOverviewDaysAndPolicy(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	seedBasic(t, fx)

	overview := overviewFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC")
	eq(t, overview.Days[9].Date, "2025-06-10")
	eq(t, overview.Days[9].Pass, int64(10))
	eq(t, overview.Days[11].Date, "2025-06-12")
	eq(t, overview.Days[11].Fail, int64(5))
	eq(t, len(overview.PolicyChanges), 2)
	eq(t, overview.PolicyChanges[0].Before == nil, true)
	eq(t, overview.PolicyChanges[1].After.P, "reject")
	eq(t, overview.PolicyChanges[1].Before == nil, false)
}

func TestCollectMetrics(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	seedBasic(t, fx)

	err := fx.svc.CollectMetrics(t.Context(), nil)
	eq(t, err == nil, true)

	registry := metrics.New()
	err = fx.svc.CollectMetrics(t.Context(), telemetry.New(registry))
	eq(t, err == nil, true)
}

func sourcesFor(t *testing.T, fx *fixture, rawQuery string) SourcesResponse {
	t.Helper()

	response, err := fx.svc.Sources(t.Context(), fx.filter(t, rawQuery))
	if err != nil {
		t.Fatal(err)
	}

	return response
}

func findSource(rows []SourceRow, key string) *SourceRow {
	for index := range rows {
		if rows[index].Key == key {
			return &rows[index]
		}
	}

	return nil
}

func TestSourcesClassification(t *testing.T) {
	t.Parallel()

	set, err := sources.Parse(
		[]byte(
			"- name: Mail provider\n  expected: true\n  match:\n    dkim_domains: [example.com]\n",
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	fx := newFixture(t, set)
	seedBasic(t, fx)
	fx.exec(t, `INSERT INTO ip_info (ip, ptr, ptr_confirmed, asn, as_org, looked_up_at)
		VALUES ('192.0.2.1', 'mail.example.com', 1, 64500, 'Example AS', 1)`)
	fx.exec(t, `INSERT INTO ip_info (ip, ptr, ptr_confirmed, asn, as_org, looked_up_at)
		VALUES ('192.0.2.2', '', 0, 64501, 'Other AS', 1)`)

	response := sourcesFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC")
	eq(t, len(response.Sources), 2)

	provider := findSource(response.Sources, "rule:Mail provider")
	eq(t, provider == nil, false)
	eq(t, provider.Expected, true)
	eq(t, provider.Pass, int64(10))
	eqSlice(t, provider.DKIMDomains, []string{"example.com"})

	other := findSource(response.Sources, "as:other as")
	eq(t, other == nil, false)
	eq(t, other.Fail, int64(5))

	unknown := sourcesFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC&unknown=1")
	eq(t, len(unknown.Sources), 1)
	eq(t, unknown.Sources[0].Key, "as:other as")
}

func TestSourceDetail(t *testing.T) {
	t.Parallel()

	set, err := sources.Parse(
		[]byte(
			"- name: Mail provider\n  expected: true\n  match:\n    dkim_domains: [example.com]\n",
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	fx := newFixture(t, set)
	seedBasic(t, fx)
	fx.exec(t, `INSERT INTO ip_info (ip, ptr, ptr_confirmed, asn, as_org, looked_up_at)
		VALUES ('192.0.2.1', 'mail.example.com', 1, 64500, 'Example AS', 1)`)

	detail, err := fx.svc.Source(
		t.Context(),
		"rule:Mail provider",
		fx.filter(t, "from=2025-06-01&to=2025-06-15&tz=UTC"),
	)
	if err != nil {
		t.Fatal(err)
	}

	eq(t, len(detail.IPs), 1)
	eq(t, detail.IPs[0].ASN, uint32(64500))
	eq(t, len(detail.Combinations), 1)
	eq(t, len(detail.DKIMSignatures), 1)
	eq(t, detail.DKIMSignatures[0].Value, "s1._domainkey.example.com")

	_, missingErr := fx.svc.Source(
		t.Context(),
		"ip:203.0.113.1",
		fx.filter(t, "from=2025-06-01&to=2025-06-15&tz=UTC"),
	)
	eq(t, errors.Is(missingErr, ErrNotFound), true)
}

func TestDSTBucketingNY(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fx.ingest(t, aggregateXML("Google", "dst-ny", "example.com",
		unixTime(2025, time.March, 8, 12), unixTime(2025, time.March, 10, 12), "reject", 100,
		"192.0.2.1", 7, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))

	overview := overviewFor(t, fx, "from=2025-03-08&to=2025-03-10&tz=America/New_York")
	eq(t, len(overview.Days), 3)
	eq(t, overview.Days[1].Date, "2025-03-09")
	eq(t, overview.Days[1].Messages, int64(7))
}

func TestDSTBucketingBerlinAndMidnight(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fx.ingest(t, aggregateXML("Google", "dst-berlin", "example.com",
		unixTime(2025, time.March, 29, 12), unixTime(2025, time.March, 31, 12), "reject", 100,
		"192.0.2.1", 3, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))

	overview := overviewFor(t, fx, "from=2025-03-29&to=2025-03-31&tz=Europe/Berlin")
	eq(t, overview.Days[1].Date, "2025-03-30")
	eq(t, overview.Days[1].Messages, int64(3))

	fx2 := newFixture(t, sources.Empty())
	fx2.ingest(t, aggregateXML("Google", "midnight", "example.com",
		unixTime(2025, time.June, 9, 4), unixTime(2025, time.June, 11, 4), "reject", 100,
		"192.0.2.1", 4, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))

	midnight := overviewFor(t, fx2, "from=2025-06-09&to=2025-06-11&tz=America/New_York")
	eq(t, midnight.Days[1].Date, "2025-06-10")
	eq(t, midnight.Days[1].Messages, int64(4))
}

func TestMidnightDSTDayBuckets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		tz    string
		dates []string
	}{
		{
			name: "sao paulo", tz: "America/Sao_Paulo",
			dates: []string{"2018-11-03", "2018-11-04", "2018-11-05"},
		},
		{
			name: "havana", tz: "America/Havana",
			dates: []string{"2019-03-09", "2019-03-10", "2019-03-11"},
		},
		{
			name: "beirut", tz: "Asia/Beirut",
			dates: []string{"2019-03-30", "2019-03-31", "2019-04-01"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			loc, err := time.LoadLocation(testCase.tz)
			if err != nil {
				t.Skipf("tzdata unavailable: %v", err)
			}

			fx := newFixture(t, sources.Empty())
			counts := []int64{3, 5, 7}

			for index, date := range testCase.dates {
				parsed, parseErr := time.Parse(dateLayout, date)
				if parseErr != nil {
					t.Fatal(parseErr)
				}

				noon := time.Date(
					parsed.Year(), parsed.Month(), parsed.Day(), 12, 0, 0, 0, loc,
				).Unix()
				fx.ingest(t, aggregateXML("Google", "mid-"+date, "example.com",
					noon-12*3600, noon+12*3600, "reject", 100,
					"192.0.2.1", counts[index], "pass", "fail", "none",
					"example.com", "example.com", "pass", "s1", ""))
			}

			query := "from=" + testCase.dates[0] + "&to=" + testCase.dates[2] +
				"&tz=" + testCase.tz
			overview := overviewFor(t, fx, query)
			eq(t, len(overview.Days), 3)

			gotDates := []string{
				overview.Days[0].Date,
				overview.Days[1].Date,
				overview.Days[2].Date,
			}
			eqSlice(t, gotDates, testCase.dates)

			for index := range counts {
				eq(t, overview.Days[index].Messages, counts[index])
			}

			single := overviewFor(t, fx, "from="+testCase.dates[1]+"&to="+testCase.dates[1]+
				"&tz="+testCase.tz)
			eq(t, len(single.Days), 1)
			eq(t, single.Days[0].Date, testCase.dates[1])
			eq(t, single.Days[0].Messages, counts[1])
		})
	}
}

func TestDayStartMidnightDST(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tz   string
		date string
		want string
	}{
		{tz: "America/Sao_Paulo", date: "2018-11-04", want: "2018-11-04 01:00"},
		{tz: "America/Havana", date: "2019-03-10", want: "2019-03-10 01:00"},
		{tz: "Asia/Beirut", date: "2019-03-31", want: "2019-03-31 01:00"},
		{tz: "America/Sao_Paulo", date: "2018-11-03", want: "2018-11-03 00:00"},
	}

	for _, testCase := range cases {
		loc, err := time.LoadLocation(testCase.tz)
		if err != nil {
			t.Skipf("tzdata unavailable: %v", err)
		}

		civil, err := parseCivilDate(testCase.date)
		if err != nil {
			t.Fatal(err)
		}

		got := time.Unix(civil.dayStart(loc), 0).In(loc).Format("2006-01-02 15:04")
		eq(t, got, testCase.want)
	}
}

func reportsFor(t *testing.T, fx *fixture, rawQuery string) ReportsResponse {
	t.Helper()

	response, err := fx.svc.Reports(t.Context(), fx.filter(t, rawQuery))
	if err != nil {
		t.Fatal(err)
	}

	return response
}

func hasDuplicate(rows []ReportSummary) bool {
	for index := range rows {
		if rows[index].Duplicate {
			return true
		}
	}

	return false
}

func seedWithTLS(t *testing.T, fx *fixture) {
	t.Helper()
	seedBasic(t, fx)
	fx.ingest(t, tlsJSON("Google", "tls-1",
		time.Date(2025, time.June, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2025, time.June, 12, 0, 0, 0, 0, time.UTC), "example.com", 90, 10))
}

func TestReportsPagination(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	seedWithTLS(t, fx)

	page1 := reportsFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC&limit=2")
	eq(t, len(page1.Reports), 2)
	eq(t, page1.NextCursor == "", false)

	page2 := reportsFor(
		t,
		fx,
		"from=2025-06-01&to=2025-06-15&tz=UTC&limit=2&cursor="+url.QueryEscape(page1.NextCursor),
	)
	eq(t, len(page2.Reports), 2)
	eq(t, hasDuplicate(page1.Reports) || hasDuplicate(page2.Reports), true)

	_, badErr := fx.svc.Reports(t.Context(), fx.filter(t, "cursor=not-base64!!"))
	_ = badErr
}

func TestBadCursor(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())

	filter := fx.filter(t, "from=2025-06-01&to=2025-06-15&tz=UTC")
	filter.Cursor = "!!!"

	_, err := fx.svc.Reports(t.Context(), filter)
	eq(t, errors.Is(err, ErrBadRequest), true)
}

func reportIDsByKind(t *testing.T, fx *fixture) (int64, int64) {
	t.Helper()

	all := reportsFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC&limit=50")

	var tlsID, dmarcID int64

	for index := range all.Reports {
		row := &all.Reports[index]

		if row.Kind == "tls-report" {
			tlsID = row.ID
			eq(t, row.Messages, int64(100))
			eq(t, row.TLSFailed, int64(10))
		}

		if row.Kind == "dmarc-aggregate" && !row.Duplicate && dmarcID == 0 {
			dmarcID = row.ID
		}
	}

	return tlsID, dmarcID
}

func TestReportDetailAndRaw(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	seedWithTLS(t, fx)

	tlsID, dmarcID := reportIDsByKind(t, fx)
	eq(t, tlsID == 0, false)
	eq(t, dmarcID == 0, false)

	detail, err := fx.svc.Report(t.Context(), dmarcID, fx.filter(t, "tz=UTC"))
	if err != nil {
		t.Fatal(err)
	}

	eq(t, detail.Policy == nil, false)
	eq(t, len(detail.Records), 1)

	raw, name, err := fx.svc.ReportRaw(t.Context(), dmarcID)
	if err != nil {
		t.Fatal(err)
	}

	eq(t, len(raw) == 0, false)
	eq(t, strings.HasSuffix(name, ".xml"), true)

	_, tlsName, err := fx.svc.ReportRaw(t.Context(), tlsID)
	if err != nil {
		t.Fatal(err)
	}

	eq(t, strings.HasSuffix(tlsName, ".json"), true)

	_, missingErr := fx.svc.Report(t.Context(), 99999, fx.filter(t, "tz=UTC"))
	eq(t, errors.Is(missingErr, ErrNotFound), true)
}

func TestTLSViewsAndDomains(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	seedWithTLS(t, fx)

	tlsList, err := fx.svc.TLS(t.Context(), fx.filter(t, "from=2025-06-01&to=2025-06-15&tz=UTC"))
	if err != nil {
		t.Fatal(err)
	}

	eq(t, len(tlsList.Domains), 1)
	eq(t, tlsList.Domains[0].Success, int64(90))
	eq(t, tlsList.Domains[0].Failure, int64(10))

	detail, err := fx.svc.TLSDomain(
		t.Context(),
		"example.com",
		fx.filter(t, "from=2025-06-01&to=2025-06-15&tz=UTC"),
	)
	if err != nil {
		t.Fatal(err)
	}

	eq(t, detail.Success, int64(90))
	eq(t, len(detail.Failures), 1)
	eq(t, detail.Failures[0].Sessions, int64(10))
	eq(t, len(detail.Policies), 1)
	eq(t, len(detail.Reporters), 1)

	domains, err := fx.svc.Domains(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	eq(t, len(domains.Domains), 1)
	eq(t, domains.Domains[0].DMARCReports, int64(2))
	eq(t, domains.Domains[0].TLSReports, int64(1))
}

func TestQuietAndLongWindow(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fx.ingest(t, aggregateXML("OldReporter", "old-1", "example.com",
		unixTime(2025, time.May, 1, 0), unixTime(2025, time.May, 2, 0), "reject", 100,
		"192.0.2.1", 2, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))
	fx.ingest(t, aggregateXML("Google", "long-1", "example.com",
		unixTime(2025, time.June, 5, 0), unixTime(2025, time.June, 10, 0), "reject", 100,
		"192.0.2.1", 2, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))

	overview := overviewFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC")
	eq(t, overview.LongWindows, int64(1))
	eq(t, len(overview.QuietReporters), 1)
	eq(t, overview.QuietReporters[0].Org, "OldReporter")
}

func statusFor(t *testing.T, fx *fixture) Status {
	t.Helper()

	status, err := fx.svc.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return status
}

func TestStatusStale(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	old := testNow.Add(-4 * time.Hour).Unix()
	fx.exec(
		t,
		`INSERT INTO mailboxes (name, uidvalidity, next_uid, last_success) VALUES ('INBOX', 1, 10, ?)`,
		old,
	)

	status := statusFor(t, fx)
	eq(t, status.Stale, true)
	eq(t, status.LastSuccessAt == nil, false)
	eq(t, *status.LastSuccessAt, old)
	eq(t, status.Problems == nil, false)
	eq(t, status.Mailboxes == nil, false)
}

func TestStatusFreshAndMessageRaw(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fresh := testNow.Add(-time.Hour).Unix()
	fx.exec(
		t,
		`INSERT INTO mailboxes (name, uidvalidity, next_uid, last_success) VALUES ('INBOX', 1, 10, ?)`,
		fresh,
	)
	eq(t, statusFor(t, fx).Stale, false)

	_, _, err := fx.svc.MessageRaw(t.Context(), 4242)
	eq(t, errors.Is(err, ErrNotFound), true)
}

func TestStatusStaleConfiguredOnly(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fresh := testNow.Add(-time.Hour).Unix()
	old := testNow.Add(-10 * time.Hour).Unix()

	fx.exec(
		t,
		`INSERT INTO mailboxes (name, uidvalidity, next_uid, last_success) VALUES ('INBOX', 1, 10, ?)`,
		fresh,
	)
	fx.exec(
		t,
		`INSERT INTO mailboxes (name, uidvalidity, next_uid, last_success) VALUES ('Retired', 1, 10, ?)`,
		old,
	)

	// The retired mailbox is listed but does not make the status stale.
	status := statusFor(t, fx)
	eq(t, status.Stale, false)
	eq(t, len(status.Mailboxes), 2)
	eq(t, status.Mailboxes[0].Name, "INBOX")
	eq(t, status.Mailboxes[0].Configured, true)
	eq(t, status.Mailboxes[1].Name, "Retired")
	eq(t, status.Mailboxes[1].Configured, false)

	// A configured mailbox with no row yet is stale and listed with nulls.
	fx.svc.deps.Mailboxes = []string{"INBOX", "Reports"}

	status = statusFor(t, fx)
	eq(t, status.Stale, true)
	eq(t, len(status.Mailboxes), 3)

	var reports *MailboxStatus

	for index := range status.Mailboxes {
		if status.Mailboxes[index].Name == "Reports" {
			reports = &status.Mailboxes[index]
		}
	}

	eq(t, reports == nil, false)
	eq(t, reports.Configured, true)
	eq(t, reports.LastSuccessAt == nil, true)
	eq(t, reports.Pending == nil, true)

	// A configured row without a success is stale even beside a fresh one.

	fx.exec(
		t,
		`INSERT INTO mailboxes (name, uidvalidity, next_uid) VALUES ('Reports', 1, 1)`,
	)
	eq(t, statusFor(t, fx).Stale, true)
}

func TestStatusMailboxPending(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fx.exec(
		t,
		`INSERT INTO mailboxes (name, uidvalidity, next_uid, pending) VALUES ('INBOX', 1, 10, 7)`,
	)

	status := statusFor(t, fx)
	eq(t, len(status.Mailboxes), 1)

	if status.Mailboxes[0].Pending == nil || *status.Mailboxes[0].Pending != 7 {
		t.Fatalf("pending = %v", status.Mailboxes[0].Pending)
	}
}

func TestPolicyChangesIgnoreLaggingReportersAndDefaults(t *testing.T) {
	t.Parallel()

	rng := new(resolvedRange)
	rng.toUnix = 1 << 40
	rng.loc = time.UTC
	full := 100
	row := func(id int64, org, policy string, pct *int) policyDBRow {
		value := new(policyDBRow)
		value.Pct, value.P, value.Domain, value.Org = pct, policy, "example.com", org
		value.ReportID, value.Begin, value.End = id, id*daySeconds, id*daySeconds+daySeconds

		return *value
	}

	rows := []policyDBRow{
		row(1, "A", "none", &full),
		row(2, "B", "none", nil),
		row(3, "A", "reject", &full),
		row(4, "B", "none", nil),
		row(5, "B", "reject", nil),
		row(6, "A", "reject", &full),
	}

	changes := []PolicyChange{}
	current := make(map[string]*Policy)
	views := make(map[[2]string]*Policy)

	for index := range rows {
		changes = appendPolicyChange(changes, &rows[index], current, views, rng)
	}

	eq(t, len(changes), 2)
	eq(t, changes[0].Before == nil, true)
	eq(t, changes[1].ReportID, int64(3))
	eqSlice(t, changes[1].Fields, []string{"p", "sp", "np"})
}

func TestParseFilterNormalizesDomainAndBoundsRange(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())

	filter, err := fx.svc.ParseFilter(url.Values{"domain": {" Example.COM "}})
	if err != nil {
		t.Fatal(err)
	}

	eq(t, filter.Domain, "example.com")

	_, err = fx.svc.ParseFilter(url.Values{"from": {"0001-01-01"}, "to": {"9999-12-31"}})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("huge range: %v", err)
	}
}

func seedBulkRecords(t *testing.T, fx *fixture, reportID int64, extra int) {
	t.Helper()

	err := fx.st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		var maxID int64

		getErr := tx.GetContext(ctx, &maxID, `SELECT COALESCE(MAX(id), 0) FROM records`)
		if getErr != nil {
			return fmt.Errorf("max record id: %w", getErr)
		}

		const batch = 500

		for done := 0; done < extra; done += batch {
			batchCount := min(batch, extra-done)

			var recSQL strings.Builder

			recSQL.WriteString(`INSERT INTO records
				(report, source_ip, count, disposition, dkim, spf, reasons,
				 header_from, envelope_from, envelope_to) VALUES `)

			var authSQL strings.Builder

			authSQL.WriteString(`INSERT INTO auth_results
				(record, method, domain, selector, scope, result) VALUES `)

			recArgs := make([]any, 0, batchCount*10)
			authArgs := make([]any, 0, batchCount*12)

			for index := range batchCount {
				if index > 0 {
					recSQL.WriteString(",")
					authSQL.WriteString(",")
				}

				recSQL.WriteString("(?, '192.0.2.1', 1, 'none', 'pass', 'fail', '[]'," +
					" 'example.com', 'example.com', 'dest.example.net')")

				recArgs = append(recArgs, reportID)

				recordID := maxID + int64(done+index) + 1

				authSQL.WriteString("(?, 'dkim', 'example.com', 's1', '', 'pass')," +
					"(?, 'spf', 'example.com', '', '', 'fail')")

				authArgs = append(authArgs, recordID, recordID)
			}

			_, recErr := tx.ExecContext(ctx, recSQL.String(), recArgs...)
			if recErr != nil {
				return fmt.Errorf("insert records: %w", recErr)
			}

			_, authErr := tx.ExecContext(ctx, authSQL.String(), authArgs...)
			if authErr != nil {
				return fmt.Errorf("insert auth results: %w", authErr)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLargeReport(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, sources.Empty())
	fx.ingest(t, aggregateXML("Google", "big-1", "example.com",
		unixTime(2025, time.June, 9, 0), unixTime(2025, time.June, 11, 0), "reject", 100,
		"192.0.2.1", 1, "pass", "fail", "none", "example.com", "example.com", "pass", "s1", ""))

	list := reportsFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC")
	eq(t, len(list.Reports), 1)

	const totalRecords = 40_000

	seedBulkRecords(t, fx, list.Reports[0].ID, totalRecords-1)

	list = reportsFor(t, fx, "from=2025-06-01&to=2025-06-15&tz=UTC")
	eq(t, len(list.Reports), 1)
	eq(t, list.Reports[0].Messages, int64(totalRecords))

	if list.Reports[0].PassRate == nil || *list.Reports[0].PassRate != 1 {
		t.Fatalf("passRate = %v", list.Reports[0].PassRate)
	}

	detail, err := fx.svc.Report(
		t.Context(), list.Reports[0].ID, fx.filter(t, "tz=UTC"),
	)
	if err != nil {
		t.Fatal(err)
	}

	eq(t, len(detail.Records), totalRecords)
	eq(t, len(detail.Records[0].Auth), 2)
}
