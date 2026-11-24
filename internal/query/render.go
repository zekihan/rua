package query

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Document kinds in the documents table, written by ingest for
// failure-containing messages.
const (
	docKindOuterHeaders    = "outer-headers"
	docKindAggregate       = "aggregate"
	docKindTLS             = "tls"
	docKindFeedback        = "feedback"
	docKindOriginalHeaders = "original-headers"
)

type renderDoc struct {
	kind string
	data []byte
}

func pickBoundary(base string, docs []renderDoc) string {
	boundary := base
	for boundaryInDocs(boundary, docs) {
		boundary += "-x"
	}

	return boundary
}

func boundaryInDocs(boundary string, docs []renderDoc) bool {
	for index := range docs {
		if bytes.Contains(docs[index].data, []byte(boundary)) {
			return true
		}
	}

	return false
}

func writePartBody(out *bytes.Buffer, data []byte) {
	out.Write(data)

	if !bytes.HasSuffix(data, []byte("\n")) {
		out.WriteString("\r\n")
	}
}

func writeDocPart(out *bytes.Buffer, boundary, contentType string, data []byte) {
	fmt.Fprintf(out, "--%s\r\nContent-Type: %s\r\n\r\n", boundary, contentType)
	writePartBody(out, data)
}

// renderDocuments builds a downloadable message on the fly from the
// stored documents of a failure-containing message. No original
// message body is ever stored, so none can appear here.
func renderDocuments(docs []renderDoc) []byte {
	var out bytes.Buffer

	boundary := pickBoundary("rua-documents", docs)
	feedbackBase := pickBoundary("rua-feedback", docs)

	writeRenderOuter(&out, docs, boundary)

	feedbackCount, aggregateCount, tlsCount := 0, 0, 0

	for index := 0; index < len(docs); index++ {
		doc := &docs[index]

		switch doc.kind {
		case docKindAggregate:
			if len(doc.data) == 0 {
				continue
			}

			aggregateCount++
			writeDocPart(&out, boundary,
				"application/xml; name=\"report-"+strconv.Itoa(aggregateCount)+".xml\"",
				doc.data)
		case docKindTLS:
			if len(doc.data) == 0 {
				continue
			}

			tlsCount++
			writeDocPart(&out, boundary,
				"application/tlsrpt+json; name=\"report-"+strconv.Itoa(tlsCount)+".json\"",
				doc.data)
		case docKindFeedback:
			var original *renderDoc
			if index+1 < len(docs) && docs[index+1].kind == docKindOriginalHeaders {
				original = &docs[index+1]
				index++
			}

			feedbackCount++
			writeFeedbackPart(&out, boundary,
				feedbackBase+"-"+strconv.Itoa(feedbackCount), doc, original)
		case docKindOriginalHeaders:
			writeDocPart(&out, boundary, "text/rfc822-headers", doc.data)
		case docKindOuterHeaders:
			continue
		default:
			continue
		}
	}

	fmt.Fprintf(&out, "--%s--\r\n", boundary)

	return out.Bytes()
}

func writeRenderOuter(out *bytes.Buffer, docs []renderDoc, boundary string) {
	var outer []byte

	for index := range docs {
		if docs[index].kind == docKindOuterHeaders {
			outer = docs[index].data

			break
		}
	}

	if len(outer) > 0 {
		out.Write(outer)

		if !bytes.HasSuffix(outer, []byte("\n")) {
			out.WriteString("\r\n")
		}
	}

	if !strings.Contains(strings.ToLower(string(outer)), "mime-version:") {
		out.WriteString("MIME-Version: 1.0\r\n")
	}

	fmt.Fprintf(out, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
}

func writeFeedbackPart(
	out *bytes.Buffer, boundary, feedbackBoundary string, doc, original *renderDoc,
) {
	fmt.Fprintf(out, "--%s\r\n", boundary)
	fmt.Fprintf(out,
		"Content-Type: multipart/report; report-type=feedback-report; boundary=%q\r\n\r\n",
		feedbackBoundary)
	writeDocPart(out, feedbackBoundary, "message/feedback-report", doc.data)

	if original != nil {
		writeDocPart(out, feedbackBoundary, "text/rfc822-headers", original.data)
	}

	fmt.Fprintf(out, "--%s--\r\n", feedbackBoundary)
}
