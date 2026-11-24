package demo

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/sources"
	"github.com/zekihan/rua/internal/store"
)

const secondsPerDay int64 = int64(dayLength / time.Second)

type counts struct {
	messages    int64
	reports     int64
	records     int64
	recordSum   int64
	authResults int64
	tlsPolicies int64
	tlsFailures int64
	failureRows int64
	ipRows      int64
	policies    int64
	beginSum    int64
	successSum  int64
}

type loaded struct {
	st   *store.Store
	snap counts
}

func TestGenerate(t *testing.T) {
	now := time.Date(2026, time.June, 15, 16, 5, 0, 0, time.UTC)
	first := load(t, now)

	second := load(t, now)
	if first.snap != second.snap {
		t.Fatalf("counts differ\nfirst %#v\nsecond %#v", first.snap, second.snap)
	}

	assertMessages(t, first.st)
	assertReports(t, first.st, now)
	assertPolicies(t, first.st, now)
	assertEnrichment(t, first.st)
	t.Logf("counts %#v", first.snap)
}

func TestSources(t *testing.T) {
	set, err := sources.Parse([]byte(Sources))
	if err != nil {
		t.Fatal(err)
	}

	rules := set.Rules()
	if len(rules) != 3 {
		t.Fatalf("rules = %d", len(rules))
	}

	for _, rule := range rules {
		if !rule.Expected || len(rule.Match.CIDRs) == 0 ||
			len(rule.Match.DKIMDomains) == 0 || len(rule.Match.ASNs) == 0 {
			t.Fatalf("rule = %+v", rule)
		}
	}

	assertRule(t, set, ipProvider4, []string{domainCom, dkimProvider}, asnProvider, sourceProvider)
	assertRule(t, set, ipProvider6, []string{dkimProvider}, asnProvider, sourceProvider)
	assertRule(t, set, ipNews4, []string{dkimNewsCom}, asnNews, sourceNews)
	assertRule(t, set, ipNews4, []string{dkimNewsNet}, asnNews, sourceNews)
	assertRule(t, set, ipCRM, []string{dkimCRM}, asnCRM, sourceCRM)
	assertUnknown(t, set, ipList4, []string{domainCom}, asnList)
	assertUnknown(t, set, ipForward4, []string{domainNet}, asnForward)
	assertUnknown(t, set, ipSpoof4, []string{domainCom}, asnSpoof)
	assertUnknown(t, set, ipList6, []string{domainOrg}, asnList)
}

func TestCatalogIPs(t *testing.T) {
	for _, info := range ipCatalog() {
		if !documentationIP(info.ip) {
			t.Fatalf("ip %s is outside documentation ranges", info.ip)
		}

		if !documentationASN(info.asn) {
			t.Fatalf("asn %d for %s is outside documentation ranges", info.asn, info.ip)
		}
	}
}

func load(t *testing.T, now time.Time) loaded {
	t.Helper()

	st, err := store.Open(t.Context(), store.Memory)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		closeErr := st.Close()
		if closeErr != nil {
			t.Error(closeErr)
		}
	})

	pipe := ingest.New(st, ingest.Options{Metrics: nil, Now: nil, NewIPs: nil, RetentionDays: 0})
	started := time.Now()
	err = Generate(t.Context(), st, pipe, now)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatal(err)
	}

	checkBudget(t, elapsed)
	t.Logf("Generate %s", elapsed)

	return loaded{st: st, snap: mustCounts(t, st)}
}

func mustCounts(t *testing.T, st *store.Store) counts {
	t.Helper()

	const query = `
		SELECT
			(SELECT COUNT(*) FROM messages),
			(SELECT COUNT(*) FROM reports),
			(SELECT COUNT(*) FROM records),
			(SELECT COALESCE(SUM(count), 0) FROM records),
			(SELECT COUNT(*) FROM auth_results),
			(SELECT COUNT(*) FROM tls_policies),
			(SELECT COUNT(*) FROM tls_failures),
			(SELECT COUNT(*) FROM failure_reports),
			(SELECT COUNT(*) FROM ip_info),
			(SELECT COUNT(*) FROM policies),
			(SELECT COALESCE(SUM(begin), 0) FROM reports),
			(SELECT COALESCE(SUM(success), 0) FROM tls_policies)`

	var snap counts

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		return tx.QueryRowxContext(ctx, query).Scan(
			&snap.messages, &snap.reports, &snap.records, &snap.recordSum, &snap.authResults,
			&snap.tlsPolicies, &snap.tlsFailures, &snap.failureRows, &snap.ipRows, &snap.policies,
			&snap.beginSum, &snap.successSum,
		)
	})
	if err != nil {
		t.Fatal(err)
	}

	return snap
}

func assertMessages(t *testing.T, st *store.Store) {
	t.Helper()

	checks := []struct {
		name  string
		query string
		args  []any
		want  int64
	}{
		{
			name:  "parse error",
			query: `SELECT COUNT(*) FROM messages WHERE status = 'error' AND class = 'dmarc-aggregate' AND subject = ?`,
			args:  []any{subjectBroken},
			want:  1,
		},
		{
			name:  "other",
			query: `SELECT COUNT(*) FROM messages WHERE status = 'ok' AND class = 'other' AND subject = ?`,
			args:  []any{subjectOther},
			want:  1,
		},
		{
			name: "skipped",
			query: `SELECT COUNT(*) FROM messages
				WHERE status = 'skipped' AND class = 'other' AND subject = ? AND blob IS NULL`,
			args: []any{subjectOversized}, want: 1,
		},
		{
			name:  "failure messages",
			query: `SELECT COUNT(*) FROM messages WHERE class = 'dmarc-failure' AND status = 'ok'`,
			args:  nil, want: 2,
		},
		{
			name:  "failure rows",
			query: `SELECT COUNT(*) FROM failure_reports`,
			args:  nil, want: 2,
		},
		{
			name:  "failure source",
			query: `SELECT COUNT(*) FROM failure_reports WHERE domain = ? AND source_ip = ?`,
			args:  []any{domainCom, ipSpoof4}, want: 1,
		},
		{
			name:  "gzip copy",
			query: `SELECT COUNT(*) FROM messages WHERE subject = ? AND status = 'ok'`,
			args:  []any{subjectGzip}, want: 1,
		},
		{
			name:  "zip copy",
			query: `SELECT COUNT(*) FROM messages WHERE subject = ? AND status = 'ok'`,
			args:  []any{subjectZip}, want: 1,
		},
	}
	for _, check := range checks {
		got := queryInt(t, st, check.query, check.args...)
		if got != check.want {
			t.Fatalf("%s = %d, want %d", check.name, got, check.want)
		}
	}
}

func assertReports(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM reports WHERE duplicate_of IS NOT NULL AND conflict = 0`,
	); got != 4 {
		t.Fatalf("duplicates = %d", got)
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM reports WHERE conflict = 1 AND duplicate_of IS NOT NULL`,
	); got != 1 {
		t.Fatalf("conflicts = %d", got)
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM records r
		JOIN reports ON reports.id = r.report WHERE reports.conflict = 1`); got != 0 {
		t.Fatalf("conflict records = %d", got)
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM reports
		WHERE schema = 'dmarc-2.0' AND domain = ? AND duplicate_of IS NULL`, domainOrg); got == 0 {
		t.Fatal("missing DMARCbis reports")
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM reports WHERE schema = 'dmarc-2.0' AND domain != ?`,
		domainOrg,
	); got != 0 {
		t.Fatalf("DMARCbis reports outside example.org = %d", got)
	}

	longWindow := int64(longHours) * int64(time.Hour/time.Second)
	if got := queryInt(t, st, `SELECT COUNT(*) FROM reports
		WHERE kind = 'dmarc-aggregate' AND duplicate_of IS NULL AND (end - begin) > ?`,
		int64(flagHours)*int64(time.Hour/time.Second)); got == 0 {
		t.Fatal("missing report windows longer than 48h")
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM reports WHERE begin % ? != 0`,
		secondsPerDay,
	); got == 0 {
		t.Fatal("every report window is aligned to midnight")
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM records WHERE source_ip LIKE '%:%'`); got == 0 {
		t.Fatal("missing IPv6 records")
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM records WHERE reasons LIKE ?`,
		"%"+reasonForwarded+"%",
	); got == 0 {
		t.Fatal("missing forwarded reasons")
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM records WHERE reasons LIKE ?`,
		"%"+reasonList+"%",
	); got == 0 {
		t.Fatal("missing mailing-list reasons")
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM records r
		JOIN auth_results a ON a.record = r.id
		WHERE r.dkim = 'fail' AND a.method = 'dkim' AND a.result = 'pass' AND a.domain = r.header_from`); got == 0 {
		t.Fatal("missing raw DKIM passes on evaluated failures")
	}

	assertQuiet(t, st, now)

	if longWindow <= int64(flagHours)*int64(time.Hour/time.Second) {
		t.Fatal("long window does not exceed 48h")
	}
}

func assertQuiet(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()

	cedarEnd := queryInt(t, st, `SELECT MAX(end) FROM reports
		WHERE org = ? AND kind = 'dmarc-aggregate' AND duplicate_of IS NULL`, orgCedar)
	if cedarEnd <= now.AddDate(0, 0, -(quietDays+1)).Unix() ||
		cedarEnd > now.AddDate(0, 0, -(quietDays-1)).Unix() {
		t.Fatalf("cedar last end = %d", cedarEnd)
	}

	quillBegin := queryInt(t, st, `SELECT MIN(begin) FROM reports
		WHERE org = ? AND kind = 'dmarc-aggregate' AND duplicate_of IS NULL`, orgQuill)
	if quillBegin <= now.AddDate(0, 0, -recentDays).Unix() ||
		quillBegin >= now.AddDate(0, 0, -(recentDays-1)).Unix() {
		t.Fatalf("quill first begin = %d", quillBegin)
	}

	spoofBegin := queryInt(t, st, `SELECT MIN(reports.begin) FROM records
		JOIN reports ON reports.id = records.report
		WHERE records.source_ip = ? AND reports.duplicate_of IS NULL`, ipSpoof4)
	if spoofBegin <= now.AddDate(0, 0, -spooferDays).Unix() ||
		spoofBegin >= now.AddDate(0, 0, -(spooferDays-1)).Unix() {
		t.Fatalf("spoofer first begin = %d", spoofBegin)
	}
}

func assertPolicies(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()

	if got := queryInt(t, st, `SELECT COUNT(*) FROM policies
		JOIN reports ON reports.id = policies.report
		WHERE reports.domain = ? AND policies.p != ?`, domainCom, dispReject); got != 0 {
		t.Fatalf("example.com policies that are not reject = %d", got)
	}

	phases := queryPhases(t, st)

	want := []string{"none:100", "quarantine:50", "quarantine:100"}
	if len(phases) != len(want) {
		t.Fatalf("example.net phases = %v", phases)
	}

	offsets := []int{spanDays, partialPolicyDays, fullPolicyDays}

	for index := range phases {
		if phases[index].Label != want[index] {
			t.Fatalf("phase %d = %s, want %s", index, phases[index].Label, want[index])
		}

		low := now.AddDate(0, 0, -offsets[index]).Unix()
		high := now.AddDate(0, 0, -(offsets[index] - 1)).Unix()

		if phases[index].Begin <= low || phases[index].Begin >= high {
			t.Fatalf(
				"phase %s begin = %d, want within (%d, %d)",
				phases[index].Label,
				phases[index].Begin,
				low,
				high,
			)
		}
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM tls_failures WHERE result_type = ?`,
		failureFetch,
	); got == 0 {
		t.Fatal("missing sts-policy-fetch-error failures")
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM tls_failures WHERE result_type = ?`,
		failureExpired,
	); got == 0 {
		t.Fatal("missing certificate-expired failures")
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM tls_policies WHERE type = ? AND domain = ?`,
		policyNone,
		domainNet,
	); got != 1 {
		t.Fatalf("no-policy-found reports = %d", got)
	}
}

type phase struct {
	Label string `db:"label"`
	Begin int64  `db:"begin"`
}

func queryPhases(t *testing.T, st *store.Store) []phase {
	t.Helper()

	const query = `
		SELECT policies.p || ':' || policies.pct AS label, MIN(reports.begin) AS begin
		FROM policies
		JOIN reports ON reports.id = policies.report
		WHERE reports.domain = ? AND reports.duplicate_of IS NULL
		GROUP BY policies.p, policies.pct
		ORDER BY MIN(reports.begin)`

	var phases []phase

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		err := tx.SelectContext(ctx, &phases, query, domainNet)
		if err != nil {
			return fmt.Errorf("query policy phases: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return phases
}

func assertEnrichment(t *testing.T, st *store.Store) {
	t.Helper()

	if got := queryInt(t, st, `SELECT COUNT(*) FROM records r
		LEFT JOIN ip_info i ON i.ip = r.source_ip WHERE i.ip IS NULL`); got != 0 {
		t.Fatalf("records missing ip_info = %d", got)
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM ip_info
		WHERE NOT ((asn BETWEEN 64496 AND 64511) OR (asn BETWEEN 65536 AND 65551))`); got != 0 {
		t.Fatalf("ip_info asns outside documentation ranges = %d", got)
	}

	if got := queryInt(
		t,
		st,
		`SELECT COUNT(*) FROM ip_info WHERE ptr_confirmed = 1 AND ptr != ''`,
	); got == 0 {
		t.Fatal("missing forward-confirmed PTR rows")
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM ip_info WHERE ptr_confirmed = 0`); got == 0 {
		t.Fatal("missing unconfirmed PTR rows")
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM ip_info WHERE ip LIKE '%:%'`); got == 0 {
		t.Fatal("missing IPv6 ip_info rows")
	}

	if got := queryInt(t, st, `SELECT COUNT(*) FROM ip_info WHERE as_org = ''`); got != 0 {
		t.Fatalf("ip_info rows without an AS org = %d", got)
	}
}

func queryInt(t *testing.T, st *store.Store, query string, args ...any) int64 {
	t.Helper()

	var value int64

	err := st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		return tx.QueryRowxContext(ctx, query, args...).Scan(&value)
	})
	if err != nil {
		t.Fatal(err)
	}

	return value
}

func assertRule(
	t *testing.T, set *sources.Set, ip string, dkim []string, asn uint32, name string,
) {
	t.Helper()

	got := set.Classify(sources.Input{
		IP: netipMust(t, ip), PTR: "", ASOrg: "", DKIMDomains: dkim, ASN: asn, PTRConfirmed: false,
	})
	if got.Kind != sources.KindRule || got.Name != name || !got.Expected {
		t.Fatalf("classify %s = %+v", ip, got)
	}
}

func assertUnknown(t *testing.T, set *sources.Set, ip string, dkim []string, asn uint32) {
	t.Helper()

	got := set.Classify(sources.Input{
		IP: netipMust(
			t,
			ip,
		),
		PTR:          "",
		ASOrg:        orgSpoofH,
		DKIMDomains:  dkim,
		ASN:          asn,
		PTRConfirmed: false,
	})
	if got.Expected || got.Kind == sources.KindRule {
		t.Fatalf("classify %s = %+v", ip, got)
	}
}

func netipMust(t *testing.T, ip string) netip.Addr {
	t.Helper()

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		t.Fatal(err)
	}

	if addr.String() != ip {
		t.Fatalf("ip %s canonical form is %s", ip, addr.String())
	}

	return addr
}

func documentationIP(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.String() != ip {
		return false
	}

	prefixes := []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func documentationASN(asn uint32) bool {
	return (asn >= 64496 && asn <= 64511) || (asn >= 65536 && asn <= 65551)
}
