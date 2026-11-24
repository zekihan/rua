// Package dmarc parses DMARC aggregate and failure reports.
package dmarc

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zekihan/rua/internal/parse/text"
	"github.com/zekihan/rua/internal/report"
)

type auth struct {
	Domain   string `xml:"domain"`
	Selector string `xml:"selector"`
	Scope    string `xml:"scope"`
	Result   string `xml:"result"`
	Human    string `xml:"human_result"`
}

const (
	dmarc2Namespace = "urn:ietf:params:xml:ns:dmarc-2.0"
	maxXMLDepth     = 128
)

var (
	errXMLDirective     = errors.New("XML directives are forbidden")
	errXMLDepth         = errors.New("XML nesting too deep")
	errXMLDocument      = errors.New("expected one XML document")
	errTooManyRecords   = errors.New("too many aggregate records")
	errMetadata         = errors.New("missing or invalid aggregate metadata")
	errRecordCount      = errors.New("invalid aggregate record count")
	errSourceIP         = errors.New("invalid aggregate source IP")
	errMissingRow       = errors.New("missing aggregate record row")
	errMissingEvaluated = errors.New("missing aggregate policy_evaluated")
	errMissingCount     = errors.New("missing aggregate record count")
	errDateRange        = errors.New("aggregate date range out of range")
)

type reason struct {
	Type    string `xml:"type"`
	Comment string `xml:"comment"`
}

type evaluated struct {
	Disposition string   `xml:"disposition"`
	DKIM        *string  `xml:"dkim"`
	SPF         *string  `xml:"spf"`
	Reasons     []reason `xml:"reason"`
}

type rowBody struct {
	Count     *string    `xml:"count"`
	Evaluated *evaluated `xml:"policy_evaluated"`
	IP        string     `xml:"source_ip"`
}

type row struct {
	Body     *rowBody `xml:"row"`
	Header   string   `xml:"identifiers>header_from"`
	From     string   `xml:"identifiers>envelope_from"`
	To       string   `xml:"identifiers>envelope_to"`
	DKIMAuth []auth   `xml:"auth_results>dkim"`
	SPFAuth  []auth   `xml:"auth_results>spf"`
}
type document struct {
	Pct       *string  `xml:"policy_published>pct"`
	XMLName   xml.Name `xml:"feedback"`
	Org       string   `xml:"report_metadata>org_name"`
	Email     string   `xml:"report_metadata>email"`
	Extra     string   `xml:"report_metadata>extra_contact_info"`
	ID        string   `xml:"report_metadata>report_id"`
	Begin     string   `xml:"report_metadata>date_range>begin"`
	End       string   `xml:"report_metadata>date_range>end"`
	Domain    string   `xml:"policy_published>domain"`
	P         string   `xml:"policy_published>p"`
	SP        string   `xml:"policy_published>sp"`
	NP        string   `xml:"policy_published>np"`
	ADKIM     string   `xml:"policy_published>adkim"`
	ASPF      string   `xml:"policy_published>aspf"`
	FO        string   `xml:"policy_published>fo"`
	Testing   string   `xml:"policy_published>testing"`
	T         string   `xml:"policy_published>t"`
	Discovery string   `xml:"policy_published>discovery_method"`
	Records   []row    `xml:"record"`
}

type xmlLimits struct {
	depth   int
	roots   int
	records int
}

func (limits *xmlLimits) start(name xml.Name) error {
	limits.depth++
	switch {
	case limits.depth > maxXMLDepth:
		return errXMLDepth
	case limits.depth == 1:
		limits.roots++
		if limits.roots > 1 {
			return errXMLDocument
		}
	case limits.depth == 2 && name.Local == "record":
		limits.records++
		if limits.records > report.MaxRecords {
			return errTooManyRecords
		}
	}

	return nil
}

// checkXML bounds nesting and record count before unmarshalling.
func checkXML(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	var limits xmlLimits

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("decode XML: %w", err)
		}

		switch element := token.(type) {
		case xml.Directive:
			return errXMLDirective
		case xml.StartElement:
			err = limits.start(element.Name)
			if err != nil {
				return err
			}
		case xml.EndElement:
			limits.depth--
		case xml.CharData:
			if limits.depth == 0 && len(bytes.TrimSpace(element)) != 0 {
				return errXMLDocument
			}
		}
	}
}

// ParseAggregate parses one decompressed aggregate report XML document.
func ParseAggregate(data []byte) (*report.Aggregate, error) {
	// The XML decoder rejects invalid UTF-8 before field values can be sanitized.
	if !utf8.Valid(data) {
		data = []byte(strings.ToValidUTF8(string(data), "�"))
	}

	data = bytes.TrimPrefix(data, []byte("\uFEFF"))

	err := checkXML(data)
	if err != nil {
		return nil, err
	}

	var doc document

	err = xml.Unmarshal(data, &doc)
	if err != nil {
		return nil, fmt.Errorf("decode aggregate: %w", err)
	}

	return doc.aggregate()
}

func (doc *document) aggregate() (*report.Aggregate, error) {
	begin, beginErr := strconv.ParseInt(strings.TrimSpace(doc.Begin), 10, 64)
	end, endErr := strconv.ParseInt(strings.TrimSpace(doc.End), 10, 64)

	if strings.TrimSpace(doc.Org) == "" || strings.TrimSpace(doc.ID) == "" ||
		strings.TrimSpace(doc.Domain) == "" || beginErr != nil || endErr != nil || begin > end {
		return nil, errMetadata
	}

	if begin < report.MinReportUnix || begin >= report.MaxReportUnix ||
		end < report.MinReportUnix || end >= report.MaxReportUnix {
		return nil, errDateRange
	}

	policy, schema, err := doc.policy()
	if err != nil {
		return nil, err
	}

	out := &report.Aggregate{
		Schema:       schema,
		OrgName:      text.Limit(doc.Org, report.MaxMedium),
		Email:        text.Limit(doc.Email, report.MaxMedium),
		ExtraContact: text.Limit(doc.Extra, report.MaxLong),
		ReportID:     text.Limit(doc.ID, report.MaxMedium),
		Domain:       text.LowerShort(doc.Domain),
		Begin:        begin,
		End:          end,
		Policy:       policy,
		Records:      nil,
	}
	for index := range doc.Records {
		record, err := doc.Records[index].record()
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", index+1, err)
		}

		out.Records = append(out.Records, record)
	}

	return out, nil
}

func (doc *document) policy() (report.Policy, string, error) {
	policy := report.Policy{
		P:         text.LowerShort(doc.P),
		SP:        text.LowerShort(doc.SP),
		NP:        text.LowerShort(doc.NP),
		Pct:       nil,
		ADKIM:     text.LowerShort(doc.ADKIM),
		ASPF:      text.LowerShort(doc.ASPF),
		FO:        text.LowerShort(doc.FO),
		Testing:   text.LowerShort(doc.Testing),
		Discovery: text.LowerShort(doc.Discovery),
	}
	if policy.Testing == "" {
		policy.Testing = text.LowerShort(doc.T)
	}

	if doc.XMLName.Space == dmarc2Namespace {
		return policy, "dmarc-2.0", nil
	}

	if doc.Pct != nil {
		pct, err := strconv.Atoi(strings.TrimSpace(*doc.Pct))
		if err != nil {
			return report.Policy{}, "", fmt.Errorf("invalid policy pct: %w", err)
		}

		policy.Pct = &pct
	}

	return policy, "rfc7489", nil
}

func (wire *row) record() (report.Record, error) {
	if wire.Body == nil {
		return report.Record{}, errMissingRow
	}

	if wire.Body.Count == nil {
		return report.Record{}, errMissingCount
	}

	if wire.Body.Evaluated == nil || wire.Body.Evaluated.DKIM == nil ||
		wire.Body.Evaluated.SPF == nil {
		return report.Record{}, errMissingEvaluated
	}

	ip, ipErr := netip.ParseAddr(strings.TrimSpace(wire.Body.IP))
	if ipErr != nil || ip.Zone() != "" {
		return report.Record{}, errSourceIP
	}

	count, err := strconv.ParseInt(strings.TrimSpace(*wire.Body.Count), 10, 64)
	if err != nil || count < 0 || count > report.MaxCount {
		return report.Record{}, errRecordCount
	}

	record := report.Record{
		SourceIP:     ip.String(),
		Count:        count,
		Disposition:  text.LowerShort(wire.Body.Evaluated.Disposition),
		DKIM:         text.LowerShort(*wire.Body.Evaluated.DKIM),
		SPF:          text.LowerShort(*wire.Body.Evaluated.SPF),
		HeaderFrom:   text.LowerShort(wire.Header),
		EnvelopeFrom: text.LowerShort(wire.From),
		EnvelopeTo:   text.LowerShort(wire.To),
		Reasons:      nil,
		Auth:         nil,
	}
	for _, reason := range wire.Body.Evaluated.Reasons {
		record.Reasons = append(record.Reasons, report.Reason{
			Type:    text.LowerShort(reason.Type),
			Comment: text.Limit(reason.Comment, report.MaxLong),
		})
	}

	record.Auth = appendAuth(record.Auth, "dkim", wire.DKIMAuth)
	record.Auth = appendAuth(record.Auth, "spf", wire.SPFAuth)

	return record, nil
}

func appendAuth(results []report.AuthResult, method string, entries []auth) []report.AuthResult {
	for _, entry := range entries {
		results = append(results, report.AuthResult{
			Method:      method,
			Domain:      text.LowerShort(entry.Domain),
			Selector:    text.Limit(entry.Selector, report.MaxShort),
			Scope:       text.LowerShort(entry.Scope),
			Result:      text.LowerShort(entry.Result),
			HumanResult: text.Limit(entry.Human, report.MaxLong),
		})
	}

	return results
}
