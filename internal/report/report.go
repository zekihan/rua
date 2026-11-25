// Package report holds the parsed form of every report kind rua understands.
// Parsers produce these values, ingest stores them and nothing else depends
// on the wire formats.
package report

// ParserVersion identifies the classification, extraction and parsing
// behavior. Bump it whenever any of them changes so stored blobs are
// reparsed.
const ParserVersion = 4

// Report window bounds in unix seconds: [1990-01-01, 2200-01-01) UTC.
// Report timestamps outside this range are rejected at parse time.
const (
	MinReportUnix = 631152000
	MaxReportUnix = 7258118400
)

// Class is the classification of a whole message.
type Class string

const (
	ClassAggregate Class = "dmarc-aggregate"
	ClassTLS       Class = "tls-report"
	ClassFailure   Class = "dmarc-failure"
	ClassOther     Class = "other"
)

// Kind is the kind of a stored report row.
type Kind string

const (
	KindAggregate Kind = "dmarc-aggregate"
	KindTLS       Kind = "tls-report"
)

// Field limits. Every string taken from a report is truncated to its limit
// (on a UTF-8 boundary, invalid UTF-8 replaced) before it is stored.
const (
	MaxShort  = 255  // domains, IPs, results, selectors, enum-like values
	MaxMedium = 1024 // org names, contacts, report IDs, subjects, addresses
	MaxLong   = 4096 // free text: comments, policy strings, extra contact info
	// MaxRecords caps records per aggregate report and failure details per
	// TLS policy; a report exceeding it fails to parse.
	MaxRecords = 100_000
	// MaxCount caps a record's message count and every TLS session count,
	// so sums over MaxRecords values cannot overflow int64.
	MaxCount = 1 << 40
)

// Aggregate is one DMARC aggregate report (RFC 7489 appendix C or the
// DMARCbis dmarc-2.0 schema).
type Aggregate struct {
	Policy Policy
	// Schema is "rfc7489" or "dmarc-2.0".
	Schema       string
	OrgName      string
	Email        string
	ExtraContact string
	ReportID     string
	Domain       string // policy_published/domain, lower-cased
	// Records are kept exactly as reported, one per <record>.
	Records []Record
	// Begin and End are the report window in unix seconds.
	Begin int64
	End   int64
}

// Policy is the published policy as the receiver saw it. Empty strings mean
// the element was absent.
type Policy struct {
	P       string
	SP      string
	NP      string
	Pct     *int
	ADKIM   string // "r" or "s"
	ASPF    string
	FO      string
	Testing string // DMARCbis "t" tag ("y" or "n")
	// Discovery is the DMARCbis discovery_method (psl or treewalk).
	Discovery string
}

// Record is one <record> row.
type Record struct {
	SourceIP     string // canonical text form
	Disposition  string // none, quarantine, reject (pass in some reporters)
	DKIM         string // policy_evaluated dkim: pass or fail
	SPF          string // policy_evaluated spf: pass or fail
	HeaderFrom   string // lower-cased
	EnvelopeFrom string // lower-cased
	EnvelopeTo   string // lower-cased
	Reasons      []Reason
	Auth         []AuthResult
	Count        int64
}

// Reason is a policy_evaluated override reason.
type Reason struct {
	Type    string `json:"type"` // forwarded, mailing_list, local_policy, ...
	Comment string `json:"comment,omitempty"`
}

// AuthResult is one raw auth_results entry.
type AuthResult struct {
	Method      string // "dkim" or "spf"
	Domain      string // lower-cased
	Selector    string // DKIM only
	Scope       string // SPF only: mfrom or helo
	Result      string // pass, fail, none, neutral, ...
	HumanResult string
}

// TLS is one SMTP TLS report (RFC 8460).
type TLS struct {
	OrgName     string
	ContactInfo string
	ReportID    string
	Policies    []TLSPolicy
	Begin       int64
	End         int64
}

// TLSPolicy is one entry of a TLS report's policies array.
type TLSPolicy struct {
	Type     string   // sts, tlsa, no-policy-found
	Domain   string   // policy-domain, lower-cased
	Strings  []string // policy-string lines
	MXHosts  []string // mx-host patterns
	Failures []TLSFailure
	Success  int64 // total-successful-session-count
	Failure  int64 // total-failure-session-count
}

// TLSFailure is one failure-details entry.
type TLSFailure struct {
	ResultType          string
	SendingMTAIP        string
	ReceivingIP         string
	ReceivingMXHostname string
	ReceivingMXHelo     string
	FailureReasonCode   string
	AdditionalInfoURI   string
	FailedSessions      int64
}

// Failure is a DMARC failure (ruf) report. Only headers are kept.
type Failure struct {
	Domain       string // Reported-Domain
	SourceIP     string
	AuthFailure  string // Auth-Failure: dkim, spf, dmarc, ...
	OriginalFrom string
	OriginalTo   string
	Subject      string
	// Arrival is Arrival-Date in unix seconds, 0 when absent or invalid.
	Arrival int64
}
