package ingest

import (
	"fmt"
	"strings"

	"github.com/zekihan/rua/internal/report"
)

// evalItem is one successfully parsed report in document order.
type evalItem struct { //nolint:govet // Class, data, parsed forms: logical grouping.
	class     report.Class
	data      []byte
	aggregate *report.Aggregate
	tlsReport *report.TLS
	failure   *report.Failure
}

// evalOutcome is what evaluation derives from extraction output:
// class, status, error and the parsed reports to store as rows.
// Fresh ingest and replay of stored documents both produce it
// through evaluateDocuments, so they cannot diverge.
type evalOutcome struct {
	class        report.Class
	status       string
	errText      string
	firstErr     string
	items        []evalItem
	aggregates   []*report.Aggregate
	tlsReports   []*report.TLS
	failures     []report.Failure
	failureCount int
	reportRows   int
}

func (out *evalOutcome) classify(class report.Class) {
	if out.class == report.ClassOther {
		out.class = class
	}
}

func (out *evalOutcome) fail(errText string) {
	if out.firstErr == "" && errText != "" {
		out.firstErr = errText
	}
}

func (out *evalOutcome) failErr(err error) {
	if err != nil {
		out.fail(err.Error())
	}
}

func evaluateDocuments(docs []storedDoc) *evalOutcome {
	out := &evalOutcome{
		class: report.ClassOther, status: StatusOK, errText: "", firstErr: "",
		items: nil, aggregates: nil, tlsReports: nil, failures: nil,
		failureCount: 0, reportRows: 0,
	}

	for index := 0; index < len(docs); index++ {
		doc := &docs[index]

		switch doc.kind {
		case docOuterHeaders:
			continue
		case docExtractError:
			out.fail(doc.errText)
		case docAggregate:
			out.evalAggregate(doc)
		case docTLS:
			out.evalTLS(doc)
		case docFeedback:
			var original *storedDoc
			if index+1 < len(docs) && docs[index+1].kind == docOriginalHeaders {
				original = &docs[index+1]
				index++
			}

			out.evalFeedback(doc, original)
		case docOriginalHeaders:
			out.fail(doc.errText)
		default:
			out.fail("unknown document kind " + doc.kind)
		}
	}

	out.status, out.errText = deriveStatus(out)

	return out
}

func deriveStatus(out *evalOutcome) (string, string) {
	if out.firstErr == "" {
		return StatusOK, ""
	}

	status := StatusError
	if out.reportRows+len(out.failures) > 0 {
		status = StatusPartial
	}

	return status, boundedText(out.firstErr, report.MaxLong)
}

func (out *evalOutcome) evalAggregate(doc *storedDoc) {
	out.classify(report.ClassAggregate)

	if doc.errText != "" {
		out.fail(doc.errText)

		return
	}

	value, err := parseAggregate(doc.data)
	if err != nil {
		out.failErr(fmt.Errorf("parse DMARC aggregate: %w", err))

		return
	}

	out.aggregates = append(out.aggregates, value)
	out.reportRows++
	out.items = append(out.items, evalItem{
		class: report.ClassAggregate, data: doc.data, aggregate: value,
		tlsReport: nil, failure: nil,
	})
}

func (out *evalOutcome) evalTLS(doc *storedDoc) {
	out.classify(report.ClassTLS)

	if doc.errText != "" {
		out.fail(doc.errText)

		return
	}

	value, err := parseTLS(doc.data)
	if err != nil {
		out.failErr(fmt.Errorf("parse TLS report: %w", err))

		return
	}

	if len(value.Policies) == 0 {
		out.failErr(errNoTLSPolicies)

		return
	}

	out.tlsReports = append(out.tlsReports, value)

	_, domains := groupTLSPolicies(value)
	out.reportRows += len(domains)
	out.items = append(out.items, evalItem{
		class: report.ClassTLS, data: doc.data, aggregate: nil,
		tlsReport: value, failure: nil,
	})
}

func (out *evalOutcome) evalFeedback(doc, original *storedDoc) {
	originalErr := ""
	if original != nil {
		originalErr = original.errText
	}

	if doc.errText != "" {
		out.fail(doc.errText)
		out.fail(originalErr)

		return
	}

	if len(doc.data) == 0 {
		out.fail(originalErr)

		return
	}

	entity, err := readHeader(doc.data)
	if err != nil {
		out.fail(originalErr)
		out.failErr(fmt.Errorf("read feedback fields: %w", err))

		return
	}

	if !strings.EqualFold(entity.Header.Get("Feedback-Type"), "auth-failure") {
		out.fail(originalErr)

		return
	}

	out.classify(report.ClassFailure)
	out.failureCount++

	if originalErr != "" {
		out.fail(originalErr)

		return
	}

	var headers []byte
	if original != nil {
		headers = original.data
	}

	value, err := parseFailure(doc.data, headers)
	if err != nil {
		out.failErr(fmt.Errorf("parse DMARC failure: %w", err))

		return
	}

	out.failures = append(out.failures, *value)
	out.items = append(out.items, evalItem{
		class: report.ClassFailure, data: doc.data, aggregate: nil,
		tlsReport: nil, failure: value,
	})
}
