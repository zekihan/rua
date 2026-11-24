package query

// The types in this file are the JSON contract of /api; web/src/api/types.ts
// mirrors them, so change both together. Dates are YYYY-MM-DD in the
// requested time zone (tz). Timestamps (fields ending in At, plus Begin and
// End) are unix seconds. Counts are message counts (sum of record count)
// unless named otherwise. Slices are never null. Rates are fractions in
// [0, 1], null when the denominator is 0.

// Verdicts.
const (
	VerdictPass      = "pass"
	VerdictForwarded = "forwarded"
	VerdictFail      = "fail"
)

// Range echoes the resolved range of an aggregating request. From and To are
// inclusive dates.
type Range struct {
	From string `json:"from"`
	To   string `json:"to"`
	TZ   string `json:"tz"`
}

// Verdicts splits a message count by verdict.
type Verdicts struct {
	Messages  int64 `json:"messages"`
	Pass      int64 `json:"pass"`
	Forwarded int64 `json:"forwarded"`
	Fail      int64 `json:"fail"`
}

// Count is a value with a message count.
type Count struct {
	Value    string `json:"value"`
	Messages int64  `json:"messages"`
}

// Policy is a published DMARC policy.
type Policy struct {
	P         string `json:"p"`
	SP        string `json:"sp"`
	NP        string `json:"np"`
	Pct       *int   `json:"pct"`
	ADKIM     string `json:"adkim"`
	ASPF      string `json:"aspf"`
	FO        string `json:"fo"`
	Testing   string `json:"testing"`
	Discovery string `json:"discovery"`
}

// DomainsResponse is GET /api/domains.
type DomainsResponse struct {
	Domains []Domain `json:"domains"`
}

type Domain struct {
	Domain string `json:"domain"`
	// First and last report window (begin of first, end of last), unix.
	FirstReportAt int64 `json:"firstReportAt"`
	LastReportAt  int64 `json:"lastReportAt"`
	DMARCReports  int64 `json:"dmarcReports"`
	TLSReports    int64 `json:"tlsReports"`
}

// Overview is GET /api/overview?domain=&from=&to=&tz=.
type Overview struct {
	Range Range `json:"range"`
	// Days covers every date of the range, zero-filled.
	Days           []DayVerdicts   `json:"days"`
	PolicyChanges  []PolicyChange  `json:"policyChanges"`
	TopFailing     []SourceVolume  `json:"topFailing"` // top 10 by fail+forwarded
	NewUnknown     []NewSource     `json:"newUnknown"`
	NewExpected    []NewSource     `json:"newExpected"`
	QuietReporters []QuietReporter `json:"quietReporters"`
	Totals         OverviewTotals  `json:"totals"`
	// LongWindows counts reports in range whose window exceeds 48 hours.
	LongWindows int64 `json:"longWindows"`
}

type OverviewTotals struct {
	PassRate  *float64 `json:"passRate"`
	Messages  int64    `json:"messages"`
	Pass      int64    `json:"pass"`
	Forwarded int64    `json:"forwarded"`
	Fail      int64    `json:"fail"`
	// Quarantined and Rejected are receiver dispositions.
	Quarantined int64 `json:"quarantined"`
	Rejected    int64 `json:"rejected"`
	Reports     int64 `json:"reports"`
}

type DayVerdicts struct {
	Date      string `json:"date"`
	Messages  int64  `json:"messages"`
	Pass      int64  `json:"pass"`
	Forwarded int64  `json:"forwarded"`
	Fail      int64  `json:"fail"`
}

// PolicyChange marks the first report that showed a changed policy.
type PolicyChange struct {
	Before   *Policy  `json:"before"` // null for the first policy seen
	After    Policy   `json:"after"`
	Domain   string   `json:"domain"`
	Date     string   `json:"date"`
	Fields   []string `json:"fields"`   // changed field names, e.g. ["p","pct"]
	ReportID int64    `json:"reportId"` // rua report row id
}

type SourceVolume struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Expected  bool   `json:"expected"`
	Messages  int64  `json:"messages"`
	Pass      int64  `json:"pass"`
	Forwarded int64  `json:"forwarded"`
	Fail      int64  `json:"fail"`
}

type NewSource struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	FirstSeen string `json:"firstSeen"` // date
	Messages  int64  `json:"messages"`
	Pass      int64  `json:"pass"`
	Forwarded int64  `json:"forwarded"`
	Fail      int64  `json:"fail"`
	Expected  bool   `json:"expected"`
}

// QuietReporter reported before the range but nothing for 7 days.
type QuietReporter struct {
	Org          string   `json:"org"`
	Domains      []string `json:"domains"`
	LastReportAt int64    `json:"lastReportAt"` // end of its last window
}

// SourcesResponse is GET /api/sources?domain=&from=&to=&tz=&unknown=1.
type SourcesResponse struct {
	Range   Range       `json:"range"`
	Sources []SourceRow `json:"sources"`
}

type SourceRow struct {
	// SPFAligned and DKIMAligned are the shares of messages whose evaluated
	// (aligned) SPF or DKIM result was pass.
	SPFAligned  *float64 `json:"spfAligned"`
	DKIMAligned *float64 `json:"dkimAligned"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	LastSeen    string   `json:"lastSeen"`
	FirstSeen   string   `json:"firstSeen"` // date, within the range
	Key         string   `json:"key"`
	Rule        string   `json:"rule"`
	DKIMDomains []string `json:"dkimDomains"`
	Forwarded   int64    `json:"forwarded"`
	Fail        int64    `json:"fail"`
	Pass        int64    `json:"pass"`
	IPs         int      `json:"ips"`
	Messages    int64    `json:"messages"`
	Expected    bool     `json:"expected"`
}

// SourceDetail is GET /api/sources/{key}?domain=&from=&to=&tz=.
type SourceDetail struct {
	Range        Range         `json:"range"`
	IPs          []IPRow       `json:"ips"`
	Days         []DayVerdicts `json:"days"`
	Combinations []Combination `json:"combinations"`
	EnvelopeFrom []Count       `json:"envelopeFrom"`
	HeaderFrom   []Count       `json:"headerFrom"`
	// DKIMSignatures values are "domain" or "selector._domainkey.domain".
	DKIMSignatures []Count   `json:"dkimSignatures"`
	Reporters      []Count   `json:"reporters"`
	Source         SourceRow `json:"source"`
}

type IPRow struct {
	IP           string `json:"ip"`
	PTR          string `json:"ptr"`
	ASOrg        string `json:"asOrg"`
	Messages     int64  `json:"messages"`
	Pass         int64  `json:"pass"`
	Forwarded    int64  `json:"forwarded"`
	Fail         int64  `json:"fail"`
	ASN          uint32 `json:"asn"`
	PTRConfirmed bool   `json:"ptrConfirmed"`
}

// Combination is one SPF result x DKIM result x alignment combination.
type Combination struct {
	SPF         string `json:"spf"`  // best raw SPF result
	DKIM        string `json:"dkim"` // best raw DKIM result
	Disposition string `json:"disposition"`
	Verdict     string `json:"verdict"`
	Messages    int64  `json:"messages"`
	SPFAligned  bool   `json:"spfAligned"`  // evaluated spf == pass
	DKIMAligned bool   `json:"dkimAligned"` // evaluated dkim == pass
}

// ReportsResponse is GET
// /api/reports?domain=&from=&to=&tz=&reporter=&kind=&cursor=&limit=.
type ReportsResponse struct {
	// NextCursor is opaque; empty when there are no more rows.
	NextCursor string          `json:"nextCursor"`
	Reports    []ReportSummary `json:"reports"`
}

type ReportSummary struct {
	PassRate    *float64 `json:"passRate"`
	DuplicateOf *int64   `json:"duplicateOf"`
	Domain      string   `json:"domain"`
	ReportID    string   `json:"reportId"`
	Date        string   `json:"date"` // day of the window midpoint in tz
	Org         string   `json:"org"`
	Kind        string   `json:"kind"` // dmarc-aggregate or tls-report
	ID          int64    `json:"id"`
	Begin       int64    `json:"begin"`
	End         int64    `json:"end"`
	Messages    int64    `json:"messages"`
	TLSFailed   int64    `json:"tlsFailed"` // TLS reports: failed sessions
	Duplicate   bool     `json:"duplicate"`
	Conflict    bool     `json:"conflict"`
	LongWindow  bool     `json:"longWindow"`
}

// ReportDetail is GET /api/reports/{id}?tz=.
type ReportDetail struct {
	Policy       *Policy         `json:"policy"`
	Email        string          `json:"email"`
	ExtraContact string          `json:"extraContact"`
	Schema       string          `json:"schema"`
	Records      []RecordDetail  `json:"records"`
	TLSPolicies  []TLSPolicy     `json:"tlsPolicies"`
	Copies       []ReportSummary `json:"copies"` // duplicates of this report or its original
	Report       ReportSummary   `json:"report"`
	MessageID    int64           `json:"messageId"`
}

type RecordDetail struct {
	HeaderFrom   string       `json:"headerFrom"`
	SourceKey    string       `json:"sourceKey"`
	SourceName   string       `json:"sourceName"`
	Disposition  string       `json:"disposition"`
	DKIM         string       `json:"dkim"`
	SPF          string       `json:"spf"`
	Verdict      string       `json:"verdict"`
	SourceIP     string       `json:"sourceIp"`
	EnvelopeFrom string       `json:"envelopeFrom"`
	EnvelopeTo   string       `json:"envelopeTo"`
	Reasons      []Reason     `json:"reasons"`
	Auth         []AuthResult `json:"auth"`
	Count        int64        `json:"count"`
}

type Reason struct {
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

type AuthResult struct {
	Method   string `json:"method"`
	Domain   string `json:"domain"`
	Selector string `json:"selector"`
	Scope    string `json:"scope"`
	Result   string `json:"result"`
}

// TLSResponse is GET /api/tls?from=&to=&tz=.
type TLSResponse struct {
	Range   Range       `json:"range"`
	Domains []TLSDomain `json:"domains"`
}

type TLSDomain struct {
	Domain       string `json:"domain"`
	Success      int64  `json:"success"`
	Failure      int64  `json:"failure"`
	Reports      int64  `json:"reports"`
	LastReportAt int64  `json:"lastReportAt"`
}

// TLSDomainDetail is GET /api/tls/{domain}?from=&to=&tz=.
type TLSDomainDetail struct {
	Range     Range        `json:"range"`
	Domain    string       `json:"domain"`
	Days      []TLSDay     `json:"days"`
	Failures  []TLSFailure `json:"failures"`
	Policies  []TLSPolicy  `json:"policies"`
	Reporters []Count      `json:"reporters"` // value is org, count is sessions
	Success   int64        `json:"success"`
	Failure   int64        `json:"failure"`
}

type TLSDay struct {
	Date    string `json:"date"`
	Success int64  `json:"success"`
	Failure int64  `json:"failure"`
}

// TLSFailure groups failure details by result type.
type TLSFailure struct {
	ResultType   string   `json:"resultType"`
	MXHosts      []string `json:"mxHosts"`
	ReceivingIPs []string `json:"receivingIps"`
	ReasonCodes  []string `json:"reasonCodes"`
	Sessions     int64    `json:"sessions"`
}

// TLSPolicy is a distinct reported policy (type and strings).
type TLSPolicy struct {
	Type    string   `json:"type"`
	Domain  string   `json:"domain"`
	Strings []string `json:"strings"`
	MXHosts []string `json:"mxHosts"`
	// Failures is only filled in report detail.
	Failures   []TLSFailureDetail `json:"failures"`
	Success    int64              `json:"success"`
	Failure    int64              `json:"failure"`
	Reports    int64              `json:"reports"`
	LastSeenAt int64              `json:"lastSeenAt"`
}

type TLSFailureDetail struct {
	ResultType     string `json:"resultType"`
	SendingIP      string `json:"sendingIp"`
	ReceivingIP    string `json:"receivingIp"`
	ReceivingMX    string `json:"receivingMx"`
	ReasonCode     string `json:"reasonCode"`
	FailedSessions int64  `json:"failedSessions"`
}

// Status is GET /api/status.
type Status struct {
	Fetch         FetchState `json:"fetch"`
	LastSuccessAt *int64     `json:"lastSuccessAt"`
	// Problems are messages with status error, partial or skipped, newest
	// first, at most 200.
	Problems          []MessageRow    `json:"problems"`
	Mailboxes         []MailboxStatus `json:"mailboxes"`
	Classes           []ClassCount    `json:"classes"`
	Other             []MessageRow    `json:"other"` // class other, newest first, at most 200
	Conflicts         []ReportSummary `json:"conflicts"`
	Events            []Event         `json:"events"`
	FailureReports    []FailureReport `json:"failureReports"` // newest first, at most 200
	StaleAfterSeconds int64           `json:"staleAfterSeconds"`
	Duplicates        int64           `json:"duplicates"`
	ParserVersion     int             `json:"parserVersion"`
	EnrichQueue       int             `json:"enrichQueue"`
	// Stale is true when IMAP is enabled and the last successful fetch of
	// any mailbox is missing or older than StaleAfterSeconds.
	Stale     bool `json:"stale"`
	Reparsing bool `json:"reparsing"`
}

type FetchState struct {
	NextFetchAt     *int64 `json:"nextFetchAt"`
	CooldownUntilAt *int64 `json:"cooldownUntilAt"`
	Enabled         bool   `json:"enabled"`
	Running         bool   `json:"running"`
}

type MailboxStatus struct {
	// Pending is how many messages the last fetch found but did not
	// process (for example after an error); null before the first fetch.
	Pending        *int64 `json:"pending"`
	LastFetchStart *int64 `json:"lastFetchStartAt"`
	LastFetchEnd   *int64 `json:"lastFetchEndAt"`
	LastSuccessAt  *int64 `json:"lastSuccessAt"`
	Name           string `json:"name"`
	LastResult     string `json:"lastResult"` // ok, error or ""
	LastError      string `json:"lastError"`
	UIDValidity    uint32 `json:"uidValidity"`
	NextUID        uint32 `json:"nextUid"`
	// Configured is false for a mailbox no longer in IMAP_MAILBOXES; it is
	// shown for history but ignored for staleness.
	Configured bool `json:"configured"`
}

// ReportersResponse is GET /api/reporters?domain=&from=&to=&tz=: every
// receiver that ever sent an aggregate report for the domain filter, with
// its activity in the range and its last report overall.
type ReportersResponse struct {
	Range     Range      `json:"range"`
	Reporters []Reporter `json:"reporters"`
}

type Reporter struct {
	Org     string   `json:"org"`
	Domains []string `json:"domains"`
	// Reports and Messages count original aggregate reports in the range.
	Reports  int64 `json:"reports"`
	Messages int64 `json:"messages"`
	// FirstReportAt and LastReportAt are window ends over all history.
	FirstReportAt int64 `json:"firstReportAt"`
	LastReportAt  int64 `json:"lastReportAt"`
	// Quiet uses the Overview quiet-reporter rule.
	Quiet bool `json:"quiet"`
}

type ClassCount struct {
	Class    string `json:"class"`
	Status   string `json:"status"`
	Messages int64  `json:"messages"`
}

type MessageRow struct {
	Mailbox      string `json:"mailbox"`
	From         string `json:"from"`
	Subject      string `json:"subject"`
	Class        string `json:"class"`
	Status       string `json:"status"`
	Error        string `json:"error"`
	ID           int64  `json:"id"`
	InternalDate int64  `json:"internalDateAt"`
	Size         int64  `json:"size"`
	UID          uint32 `json:"uid"`
	HasRaw       bool   `json:"hasRaw"` // false for skipped messages
}

type Event struct {
	Kind    string `json:"kind"`
	Mailbox string `json:"mailbox"`
	Detail  string `json:"detail"`
	At      int64  `json:"at"`
}

type FailureReport struct {
	Domain      string `json:"domain"`
	SourceIP    string `json:"sourceIp"`
	AuthFailure string `json:"authFailure"`
	From        string `json:"from"`
	To          string `json:"to"`
	Subject     string `json:"subject"`
	MessageID   int64  `json:"messageId"`
	ArrivalAt   int64  `json:"arrivalAt"`
}

// FetchResponse is POST /api/fetch.
type FetchResponse struct {
	Status string `json:"status"`
}

// ErrorBody is the error response. Errors are returned as {"error": {"code": "...", "message":
// "..."}} with
// a 4xx/5xx status. Codes: bad_request, not_found, fetch_running,
// fetch_cooldown, fetch_disabled, forbidden, internal.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
