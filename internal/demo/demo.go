// Package demo generates a realistic synthetic dataset: aggregate reports
// from several receivers over a year, forwarded mail, a spoofer, a policy
// change, TLS reports with failures, duplicates and a few broken messages.
// The data is fed through the ingest pipeline so it exercises the real code.
package demo

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/store"
)

const (
	spanDays = 400

	dayLength = 24 * time.Hour

	// PCGStream mixes the UTC day into the second PCG seed.
	pcgStream = 0x9e3779b97f4a7c15

	base10       = 10
	xmlCap       = 4096
	mimeSlack    = 160
	failureSlack = 320
	halfSplit    = 2
	incidentLead = 2

	offsetMin  = 1
	offsetSpan = 20

	quietDays         = 15
	recentDays        = 10
	spooferDays       = 5
	partialPolicyDays = 60
	fullPolicyDays    = 20

	incidentAgo    = 30
	incidentLength = 4

	longEvery      = 7
	longHours      = 50
	flagHours      = 48
	longWindowLead = 3

	volumeFloor     = 48
	volumeStep      = 3
	weekendPercent  = 40
	jitterMin       = 95
	jitterSpan      = 11
	newsShare       = 3
	newsBurstShare  = 3
	newsBurstNum    = 2
	crmDivisor      = 8
	forwardDivisor  = 20
	v6Divisor       = 5
	orgDivisor      = 10
	fullPct         = 100
	partialPct      = 50
	tlsSuccessScale = 20

	gzipDay    = 40
	zipDay     = 80
	mimeDay    = 120
	mimeOrgDay = 200

	noiseEvery     = 3
	forwarderKinds = 3

	incidentFetch    = 40
	incidentExpired  = 25
	noPolicySuccess  = 4
	noPolicyFailures = 2

	oversizedBytes int64 = 250 << 20

	mailboxDemo = "demo"

	domainCom = "example.com"
	domainNet = "example.net"
	domainOrg = "example.org"

	resultPass     = "pass"
	resultFail     = "fail"
	dispNone       = "none"
	dispReject     = "reject"
	dispQuarantine = "quarantine"
	methodDKIM     = "dkim"
	methodSPF      = "spf"
	scopeMFrom     = "mfrom"

	reasonForwarded = "forwarded"
	reasonList      = "mailing_list"

	selectorMail     = "mail"
	selectorProvider = "provider"
	selectorNews     = "news"
	selectorCRM      = "crm"
	selectorList     = "list"

	sourceProvider = "Mail provider"
	sourceNews     = "Newsletter service"
	sourceCRM      = "CRM service"

	dkimProvider  = "mx.example-provider.test"
	dkimNewsCom   = "news.example.com"
	dkimNewsNet   = "news.example.net"
	dkimCRM       = "crm.example.invalid"
	spfCRM        = "send.crm.example.invalid"
	spfList       = "list.example-relay.test"
	spfForward    = "fwd.example-forwarder.test"
	envelopeNews  = "bounce.news.example.com"
	envelopeCRM   = "bounce.send.crm.example.invalid"
	envelopeNoise = "bounce.example-noise.test"

	ipProvider4 = "192.0.2.10"
	ipProvider6 = "2001:db8::10"
	ipNews4     = "198.51.100.20"
	ipCRM       = "203.0.113.30"
	ipList4     = "192.0.2.40"
	ipList6     = "2001:db8:2::42"
	ipForward4  = "198.51.100.41"
	ipSpoof4    = "203.0.113.99"
	ipSpoof6    = "2001:db8:3::99"

	asnProvider = 64500
	asnNews     = 64501
	asnCRM      = 64502
	asnList     = 64503
	asnForward  = 64504
	asnSpoof    = 64496
	asnNoise    = 64505
	asnNoise6   = 65536

	orgNorthwind = "Northwind Mail"
	orgHarbor    = "Harbor Post"
	orgCedar     = "Cedar Relay"
	orgQuill     = "Quill Inbox"

	mailNorthwind = "dmarc@northwind-mail.example"
	mailHarbor    = "reports@harbor-post.example"
	mailCedar     = "rua@cedar-relay.example"
	mailQuill     = "dmarc@quill-inbox.example"

	prefixNorthwind = "nw"
	prefixHarbor    = "hp"
	prefixCedar     = "cr"
	prefixQuill     = "qi"

	orgProvider = "Example Mail Host"
	orgNewsHost = "Example Newsletter Host"
	orgCRMHost  = "Example CRM Host"
	orgListHost = "Example List Relay"
	orgForwardH = "Example Forwarder"
	orgSpoofH   = "Example Transit"
	orgNoiseH   = "Example Noise Transit"
	orgNoise6H  = "Example V6 Transit"

	subjectGzip      = "gzip copy of aggregate report"
	subjectZip       = "zip copy of aggregate report"
	subjectMIME      = "second copy of aggregate report"
	subjectConflict  = "conflicting copy of aggregate report"
	subjectBroken    = "malformed aggregate report"
	subjectOther     = "mailbox note"
	subjectOversized = "oversized aggregate report"
	subjectFailCom   = "DMARC failure for example.com"
	subjectFailNet   = "DMARC failure for example.net"

	alignRelaxed = "r"
	alignStrict  = "s"
	foAll        = "1"
	foNone       = "0"
	testingOff   = "n"
	discoveryPSL = "psl"

	mediaGzip = "application/gzip"
	mediaZip  = "application/zip"
	mediaXML  = "text/xml"
	fileGzip  = "report.xml.gz"
	fileZip   = "report.zip"
	fileXML   = "report.xml"

	stsVersion = "version: STSv1"
	stsMode    = "mode: enforce"
	stsMaxAge  = "max_age: 86400"

	failureFetch    = "sts-policy-fetch-error"
	failureExpired  = "certificate-expired"
	failureValidate = "validation-failure"
	policySTS       = "sts"
	policyNone      = "no-policy-found"
	mxSuffix        = "mail."
)

// Sources is the SOURCES_FILE document for the generated dataset.
// The mail provider, the newsletter service and the CRM service are expected.
const Sources = `
- name: Mail provider
  expected: true
  match:
    cidrs:
      - 192.0.2.10/32
      - 2001:db8::10/128
    dkim_domains:
      - mx.example-provider.test
    asns:
      - 64500
- name: Newsletter service
  expected: true
  match:
    cidrs:
      - 198.51.100.20/32
    dkim_domains:
      - news.example.com
      - news.example.net
    asns:
      - 64501
- name: CRM service
  expected: true
  match:
    cidrs:
      - 203.0.113.30/32
    dkim_domains:
      - crm.example.invalid
    asns:
      - 64502
`

var (
	errNilStore     = errors.New("nil store")
	errNilPipeline  = errors.New("nil pipeline")
	errUnknownCopy  = errors.New("unknown duplicate delivery")
	errEmptyRecords = errors.New("aggregate report has no records")
	reportDomains   = [...]string{domainCom, domainNet, domainOrg}
	noiseAddresses  = [...]string{
		"203.0.113.160",
		"203.0.113.161",
		"203.0.113.162",
		"203.0.113.163",
		"2001:db8:4::10",
		"2001:db8:4::11",
		"2001:db8:4::12",
		"2001:db8:4::13",
	}
)

// Generate ingests a deterministic dataset ending at now.
// Its random seed is the UTC day of now.
// IP information is written to st, and pipe receives every report message.
func Generate(
	ctx context.Context, st *store.Store, pipe *ingest.Pipeline, now time.Time,
) error {
	if st == nil {
		return errNilStore
	}

	if pipe == nil {
		return errNilPipeline
	}

	gen := newGenerator(now)

	err := gen.aggregates(ctx, pipe)
	if err != nil {
		return err
	}

	err = gen.tlsReports(ctx, pipe)
	if err != nil {
		return err
	}

	err = gen.edges(ctx, pipe)
	if err != nil {
		return err
	}

	err = writeIPInfo(ctx, st, gen.now)
	if err != nil {
		return err
	}

	return nil
}

type generator struct {
	rng       *rand.Rand
	now       time.Time
	start     time.Time
	reporters []reporter
	buf       []byte
	uid       uint32
}

type reporter struct {
	name   string
	email  string
	prefix string
	offset int
	first  int
	last   int
	long   bool
	bisOrg bool
	tls    bool
}

func newGenerator(now time.Time) *generator {
	utc := now.UTC()
	day := utc.Truncate(dayLength).Unix()
	seed := uint64(day) //nolint:gosec // G115: the UTC day seed is non-negative.

	gen := &generator{
		rng: rand.New( //nolint:gosec // Demo data is reproducible, not secret.
			rand.NewPCG(seed, seed^pcgStream),
		),
		now:       utc,
		start:     utc.Add(-spanDays * dayLength),
		reporters: baseReporters(),
		buf:       make([]byte, 0, xmlCap),
		uid:       0,
	}

	for index := range gen.reporters {
		gen.reporters[index].offset = offsetMin + gen.rng.IntN(offsetSpan)
	}

	return gen
}

func baseReporters() []reporter {
	last := spanDays - 1

	return []reporter{
		{
			name: orgNorthwind, email: mailNorthwind, prefix: prefixNorthwind,
			offset: 0, first: 0, last: last, long: false, bisOrg: false, tls: true,
		},
		{
			name: orgHarbor, email: mailHarbor, prefix: prefixHarbor,
			offset: 0, first: 0, last: last, long: true, bisOrg: true, tls: true,
		},
		{
			name: orgCedar, email: mailCedar, prefix: prefixCedar,
			offset: 0, first: 0, last: last - quietDays, long: false, bisOrg: false, tls: false,
		},
		{
			name:   orgQuill,
			email:  mailQuill,
			prefix: prefixQuill,
			offset: 0,
			first:  spanDays - recentDays,
			last:   last,
			long:   false,
			bisOrg: false,
			tls:    false,
		},
	}
}

func (gen *generator) ingest(
	ctx context.Context, pipe *ingest.Pipeline, raw []byte, when time.Time,
) error {
	gen.uid++

	_, err := pipe.Ingest(ctx, raw, ingest.Source{
		Mailbox: mailboxDemo, UID: gen.uid, InternalDate: when,
	})
	if err != nil {
		return fmt.Errorf("ingest demo message: %w", err)
	}

	return nil
}

func (gen *generator) window(dayIndex, offsetHours, lengthHours int) (time.Time, time.Time, bool) {
	begin := gen.start.Add(time.Duration(dayIndex*int(dayLength/time.Hour)+offsetHours) * time.Hour)

	end := begin.Add(time.Duration(lengthHours) * time.Hour)
	if !begin.Before(gen.now) {
		return time.Time{}, time.Time{}, false
	}

	if end.After(gen.now) {
		end = gen.now
	}

	if !end.After(begin) {
		return time.Time{}, time.Time{}, false
	}

	return begin, end, true
}
