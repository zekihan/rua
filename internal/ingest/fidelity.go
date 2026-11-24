package ingest

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-message"

	"github.com/zekihan/rua/internal/report"
)

const (
	headerRuaStatus = "X-Rua-Ingest-Status"
	headerRuaError  = "X-Rua-Ingest-Error"
)

// evalOutcome is the in-memory result of extracting and parsing a
// message, used to validate that a stored copy reparses identically.
type evalOutcome struct {
	class        report.Class
	status       string
	errText      string
	aggregates   []*report.Aggregate
	tlsReports   []*report.TLS
	failures     []report.Failure
	failureDocs  []document
	failureCount int
	reportRows   int
	hasFailure   bool
}

const severityError = 2

func statusSeverity(status string) int {
	switch status {
	case StatusOK:
		return 0
	case StatusPartial:
		return 1
	case StatusError:
		return severityError
	default:
		return -1
	}
}

func singleLineError(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.Join(strings.Fields(value), " ")

	return boundedText(value, report.MaxLong)
}

func applyRuaHeaders(entity *message.Entity, outcome *evalOutcome) {
	if entity == nil || outcome == nil || !outcome.hasFailure {
		return
	}

	stored := entity.Header.Get(headerRuaStatus)
	if statusSeverity(stored) < 0 {
		return
	}

	if statusSeverity(stored) <= statusSeverity(outcome.status) {
		return
	}

	outcome.status = stored
	if storedErr := entity.Header.Get(headerRuaError); storedErr != "" {
		outcome.errText = singleLineError(storedErr)
	}
}

func deriveStatus(firstErr error, reportRows, failureRows int) (string, string) {
	if firstErr == nil {
		return StatusOK, ""
	}

	status := StatusError
	if reportRows+failureRows > 0 {
		status = StatusPartial
	}

	return status, boundedText(firstErr.Error(), report.MaxLong)
}

// evalMessage runs extraction and parsing in memory, mirroring
// storeDocument's parse error handling, without writing rows.
func evalMessage(raw []byte, trustRua bool) evalOutcome {
	var (
		entity    *message.Entity
		headerErr error
	)

	if !looksBare(raw) {
		entity, headerErr = message.Read(bytes.NewReader(raw))
	}

	out := evalOutcome{
		class: report.ClassOther, status: StatusOK, errText: "",
		aggregates: nil, tlsReports: nil, failures: nil, failureDocs: nil,
		failureCount: 0, reportRows: 0, hasFailure: false,
	}
	ext := newExtraction(nil)
	ext.accept = func(doc document) error {
		return collectDocument(ext, &out, doc)
	}

	err := ext.extract(raw, entity, headerErr)
	if err != nil {
		ext.failed(err)
	}

	out.class = ext.class
	out.hasFailure = ext.hasFailure
	out.failureCount = ext.failureCount
	out.status, out.errText = deriveStatus(
		ext.firstError, out.reportRows, len(out.failures),
	)

	if trustRua {
		applyRuaHeaders(entity, &out)
	}

	return out
}

func collectDocument(ext *extraction, out *evalOutcome, doc document) error {
	switch doc.class {
	case report.ClassAggregate:
		value, err := parseAggregate(doc.data)
		if err != nil {
			ext.failed(fmt.Errorf("parse DMARC aggregate: %w", err))

			return nil
		}

		out.aggregates = append(out.aggregates, value)
		out.reportRows++
	case report.ClassTLS:
		value, err := parseTLS(doc.data)
		if err != nil {
			ext.failed(fmt.Errorf("parse TLS report: %w", err))

			return nil
		}

		if len(value.Policies) == 0 {
			ext.failed(errNoTLSPolicies)

			return nil
		}

		out.tlsReports = append(out.tlsReports, value)

		_, domains := groupTLSPolicies(value)
		out.reportRows += len(domains)
	case report.ClassFailure:
		out.failureDocs = append(out.failureDocs, doc)

		value, err := parseFailure(doc.data, doc.originalHeaders)
		if err != nil {
			ext.failed(fmt.Errorf("parse DMARC failure: %w", err))

			return nil
		}

		out.failures = append(out.failures, *value)
	case report.ClassOther:
		return nil
	default:
		return fmt.Errorf("%w: %s", errUnexpectedKind, doc.class)
	}

	return nil
}

func parsedDigests[T any](values []*T) [][]byte {
	digests := make([][]byte, 0, len(values))

	for _, value := range values {
		digest, err := canonicalDigest(value)
		if err != nil {
			digests = append(digests, nil)

			continue
		}

		digests = append(digests, digest)
	}

	return digests
}

func sameReportContent(want, got *evalOutcome) bool {
	if !slices.Equal(want.failures, got.failures) {
		return false
	}

	wantAgg := parsedDigests(want.aggregates)
	gotAgg := parsedDigests(got.aggregates)

	if len(wantAgg) != len(gotAgg) {
		return false
	}

	for index := range wantAgg {
		if !bytes.Equal(wantAgg[index], gotAgg[index]) {
			return false
		}
	}

	wantTLS := parsedDigests(want.tlsReports)
	gotTLS := parsedDigests(got.tlsReports)

	if len(wantTLS) != len(gotTLS) {
		return false
	}

	for index := range wantTLS {
		if !bytes.Equal(wantTLS[index], gotTLS[index]) {
			return false
		}
	}

	return true
}

// buildStoredCopy validates fidelity directly: it reparses the stored
// copy in memory and compares class, status, error and report/failure
// content with the original outcome.
func buildStoredCopy(raw []byte, orig *evalOutcome) []byte {
	if orig == nil || !orig.hasFailure {
		return raw
	}

	sanitized := sanitizeViaWalker(raw)
	candidate := sanitized.data

	if sanitized.failed || sanitized.redacted != orig.failureCount || len(candidate) == 0 {
		candidate = buildMinimalCopy(raw, orig)
	}

	candidateOutcome := evalMessage(candidate, true)
	if !sameReportContent(orig, &candidateOutcome) {
		candidate = buildMinimalCopy(raw, orig)
		candidateOutcome = evalMessage(candidate, true)
	}

	if orig.status != candidateOutcome.status || orig.errText != candidateOutcome.errText {
		candidate = injectRuaHeaders(candidate, orig.status, orig.errText)
	}

	return candidate
}

func topHeaderSeparator(data []byte) (int, int) {
	best, bestLen := -1, 0

	for _, sep := range []string{"\r\n\r\n", "\n\n", "\r\n\n", "\n\r\n"} {
		if index := bytes.Index(data, []byte(sep)); index >= 0 &&
			(best < 0 || index < best) {
			best, bestLen = index, len(sep)
		}
	}

	return best, bestLen
}

// injectRuaHeaders writes the fidelity headers into the stored copy's
// top-level header. Incoming X-Rua-* fields were already stripped by
// the sanitizer/minimal builder, so a sender cannot inject them.
func injectRuaHeaders(data []byte, status, errText string) []byte {
	if statusSeverity(status) < 0 {
		return data
	}

	errText = singleLineError(errText)

	var lines string

	lines += headerRuaStatus + ": " + status + "\r\n"
	if errText != "" {
		lines += headerRuaError + ": " + errText + "\r\n"
	}

	index, length := topHeaderSeparator(data)
	if index < 0 {
		return []byte(lines + "\r\n" + string(data))
	}

	var out bytes.Buffer

	out.Write(data[:index])
	out.WriteString("\r\n")
	out.WriteString(lines)
	// Keep one blank line between the header block and the body.
	out.Write(data[index+length-2:])
	// The slice data[index+length-2:] starts with the final CRLF of the
	// separator for CRLF separators; for LF-only the arithmetic still
	// leaves the blank line because lines ends with CRLF.

	return out.Bytes()
}
