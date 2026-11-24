package ingest

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"

	"github.com/zekihan/rua/internal/report"
)

const (
	maxMIMEDepth        = 32
	archiveMagicBytes   = 4
	typeTLSJSON         = "application/tlsrpt+json"
	typeTLSGzip         = "application/tlsrpt+gzip"
	typeFeedback        = "message/feedback-report"
	typeOriginal        = "message/rfc822"
	typeOriginalHeaders = "text/rfc822-headers"
)

var (
	errReportLimit   = fmt.Errorf("report exceeds %d decompressed bytes", MaxReportBytes)
	errMessageLimit  = fmt.Errorf("message exceeds %d bytes across report parts", MaxMessageBytes)
	errZipEntries    = fmt.Errorf("zip archive exceeds %d entries", MaxZipEntries)
	errNestedArchive = errors.New("nested archives are unsupported")
	errMIMEDepth     = errors.New("MIME nesting limit exceeded")
	errNotReport     = errors.New("unrecognized report document")
)

// extraction is the output of MIME extraction: the stored documents
// in extraction order and whether the message is failure-containing.
// Evaluation turns it into class, status, error and rows; for a
// failure-containing message the documents are persisted and replayed
// through the same evaluation.
type extraction struct { //nolint:govet // Documents, reader, flag: logical grouping.
	docs              []storedDoc
	reader            *bodyReader
	failureContaining bool
}

func newExtraction() *extraction {
	return &extraction{
		docs:              nil,
		reader:            newBodyReader(),
		failureContaining: false,
	}
}

// extractMessage runs extraction over a raw message or bare document.
func extractMessage(
	raw []byte, entity *message.Entity, headerErr error,
) (*extraction, error) {
	ext := newExtraction()

	err := ext.extract(raw, entity, headerErr)
	if err != nil {
		return nil, err
	}

	return ext, nil
}

func (ext *extraction) appendDoc(kind string, data []byte, errText string) {
	ext.docs = append(ext.docs, storedDoc{kind: kind, data: data, errText: errText})
}

func (ext *extraction) failed(err error) {
	if err != nil {
		ext.appendDoc(docExtractError, nil, boundedText(err.Error(), report.MaxLong))
	}
}

// failedHint records an extraction error for a part whose media type
// already classified the message, so replay re-derives that class.
func (ext *extraction) failedHint(err error, hint report.Class) {
	if err == nil {
		return
	}

	if hint == report.ClassTLS {
		ext.appendDoc(docTLS, nil, boundedText(err.Error(), report.MaxLong))

		return
	}

	ext.failed(err)
}

func (ext *extraction) seen(mediaType string, params map[string]string) {
	if strings.EqualFold(mediaType, typeFeedback) || isFeedbackReport(mediaType, params) {
		ext.failureContaining = true
	}
}

func (ext *extraction) extract(raw []byte, entity *message.Entity, headerErr error) error {
	if len(raw) > MaxMessageBytes {
		ext.failed(errMessageLimit)

		return nil
	}

	if isZip(raw) {
		return ext.openZip(raw, report.ClassOther)
	}

	if looksBare(raw) {
		return ext.part(bytes.NewReader(raw), "", true)
	}

	if headerErr != nil && !message.IsUnknownCharset(headerErr) {
		ext.failed(fmt.Errorf("read MIME message: %w", headerErr))

		return nil
	}

	if entity != nil {
		ext.appendDoc(docOuterHeaders, outerHeaderBlock(&entity.Header), "")
	}

	hooks := &mimeHooks{
		fail:        ext.failed,
		seen:        ext.seen,
		leaf:        ext.leaf,
		failurePair: ext.failurePair,
	}

	return walkMIME(entity, 0, ext.reader, hooks)
}

func (ext *extraction) leaf(entity *message.Entity, mediaType string) error {
	switch {
	case strings.EqualFold(mediaType, typeFeedback):
		data, err := ext.reader.read(entity.Body)
		if err != nil {
			ext.appendDoc(docFeedback, nil, boundedText(err.Error(), report.MaxLong))

			return nil
		}

		ext.appendDoc(docFeedback, headerOnly(data), "")

		return nil
	case strings.EqualFold(mediaType, typeOriginal),
		strings.EqualFold(mediaType, typeOriginalHeaders):
		data, err := ext.reader.readOriginalHeaders(
			entity.Body, strings.EqualFold(mediaType, typeOriginal),
		)
		if err != nil {
			ext.appendDoc(docOriginalHeaders, nil, boundedText(err.Error(), report.MaxLong))

			return nil
		}

		ext.appendDoc(docOriginalHeaders, data, "")

		return nil
	default:
		return ext.part(entity.Body, mediaType, false)
	}
}

func (ext *extraction) failurePair(parts *failureParts) error {
	if parts.hasFeedback {
		if parts.feedbackErr != nil {
			ext.appendDoc(
				docFeedback, nil, boundedText(parts.feedbackErr.Error(), report.MaxLong),
			)
		} else {
			ext.appendDoc(docFeedback, headerOnly(parts.feedback), "")
		}
	}

	if parts.hasHeaders {
		if parts.headerErr != nil {
			ext.appendDoc(
				docOriginalHeaders, nil, boundedText(parts.headerErr.Error(), report.MaxLong),
			)
		} else {
			ext.appendDoc(docOriginalHeaders, parts.headers, "")
		}
	}

	return nil
}

func readHeader(raw []byte) (*message.Entity, error) {
	// IMAP and feedback parts may omit the blank line ending the header.
	entity, err := message.Read(io.MultiReader(bytes.NewReader(raw), strings.NewReader("\r\n\r\n")))
	if err != nil && entity == nil {
		return nil, fmt.Errorf("read header fields: %w", err)
	}

	return entity, nil
}

func looksBare(data []byte) bool {
	if isGzip(data) || isZip(data) {
		return true
	}

	trimmed := trimDocument(data)

	return len(trimmed) > 0 && (trimmed[0] == '<' || trimmed[0] == '{')
}

func trimDocument(data []byte) []byte {
	return bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
}

func isGzip(data []byte) bool { return bytes.HasPrefix(data, []byte("\x1f\x8b")) }

func isZip(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) ||
		bytes.HasPrefix(data, []byte(zipEndSignature)) ||
		bytes.HasPrefix(data, []byte("PK\x07\x08"))
}

func (ext *extraction) part(reader io.Reader, mediaType string, expected bool) error {
	buffered := bufio.NewReader(reader)

	magic, err := buffered.Peek(archiveMagicBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		ext.failed(fmt.Errorf("read report signature: %w", err))

		return nil
	}

	hint := report.ClassOther
	if strings.EqualFold(mediaType, typeTLSJSON) || strings.EqualFold(mediaType, typeTLSGzip) {
		hint = report.ClassTLS

		expected = true
	}

	switch {
	case isGzip(magic):
		return ext.gzip(buffered, hint)
	case isZip(magic):
		return ext.zip(buffered, hint)
	default:
		data, err := ext.reader.read(buffered)
		if err != nil {
			ext.failedHint(err, hint)

			return nil
		}

		return ext.deliver(data, hint, expected)
	}
}

func (ext *extraction) gzip(reader io.Reader, hint report.Class) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		ext.failedHint(fmt.Errorf("open gzip report: %w", err), hint)

		return nil
	}

	data, err := ext.reader.read(gz)

	err = errors.Join(err, gz.Close())
	if err != nil {
		ext.failedHint(err, hint)

		return nil
	}

	if isZip(data) || isGzip(data) {
		ext.failedHint(errNestedArchive, hint)

		return nil
	}

	return ext.deliver(data, hint, true)
}

func (ext *extraction) zip(reader io.Reader, hint report.Class) error {
	// ZIP requires random access to the compressed bytes. The input MIME bytes
	// are already bounded by MaxMessageBytes; each entry has a separate cap.
	data, err := io.ReadAll(io.LimitReader(reader, int64(MaxMessageBytes)+1))
	if err != nil {
		ext.failedHint(fmt.Errorf("read zip archive: %w", err), hint)

		return nil
	}

	if len(data) > MaxMessageBytes {
		ext.failedHint(errMessageLimit, hint)

		return nil
	}

	return ext.openZip(data, hint)
}

func (ext *extraction) openZip(data []byte, hint report.Class) error {
	err := checkZipDirectory(data)
	if err != nil {
		ext.failedHint(err, hint)

		return nil
	}

	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		ext.failedHint(fmt.Errorf("open zip archive: %w", err), hint)

		return nil
	}

	if len(archive.File) > MaxZipEntries {
		ext.failedHint(errZipEntries, hint)

		return nil
	}

	err = checkZipEntries(archive.File)
	if err != nil {
		ext.failedHint(err, hint)

		return nil
	}

	return ext.zipDocuments(archive.File, hint)
}

func checkZipEntries(files []*zip.File) error {
	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}

		reader, err := file.Open()
		if err != nil {
			continue // Recorded when reading the entry.
		}

		magic := make([]byte, archiveMagicBytes)

		length, readErr := io.ReadFull(reader, magic)

		err = reader.Close()
		if err != nil {
			return fmt.Errorf("close zip entry: %w", err)
		}

		if readErr != nil && length < 2 {
			continue // Recorded when reading the entry.
		}

		if isZip(magic[:length]) || isGzip(magic[:length]) {
			return errNestedArchive
		}
	}

	return nil
}

func (ext *extraction) zipDocuments(files []*zip.File, hint report.Class) error {
	var found bool

	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}

		reader, err := file.Open()
		if err != nil {
			ext.failedHint(fmt.Errorf("open zip entry: %w", err), hint)

			continue
		}

		data, err := ext.reader.read(reader)

		err = errors.Join(err, reader.Close())
		if err != nil {
			ext.failedHint(err, hint)

			if errors.Is(err, errMessageLimit) {
				return nil
			}

			found = true

			continue
		}

		class := documentClass(data, hint)
		if class != report.ClassOther {
			found = true

			err = ext.deliverClass(data, class)
			if err != nil {
				return err
			}
		}
	}

	if !found {
		ext.failedHint(errNotReport, hint)
	}

	return nil
}

func (ext *extraction) deliver(data []byte, hint report.Class, expected bool) error {
	class := documentClass(data, hint)
	if class != report.ClassOther {
		return ext.deliverClass(data, class)
	}

	if expected {
		ext.failedHint(errNotReport, hint)
	}

	return nil
}

func (ext *extraction) deliverClass(data []byte, class report.Class) error {
	switch class {
	case report.ClassAggregate:
		ext.appendDoc(docAggregate, data, "")
	case report.ClassTLS:
		ext.appendDoc(docTLS, data, "")
	case report.ClassFailure, report.ClassOther:
		ext.failed(errNotReport)
	default:
		ext.failed(errNotReport)
	}

	return nil
}

func documentClass(data []byte, hint report.Class) report.Class {
	if hint == report.ClassTLS {
		return hint
	}

	trimmed := trimDocument(data)
	if len(trimmed) == 0 {
		return report.ClassOther
	}

	switch trimmed[0] {
	case '<':
		if feedbackRoot(trimmed) {
			return report.ClassAggregate
		}
	case '{':
		var shape struct {
			Policies json.RawMessage `json:"policies"`
			Org      json.RawMessage `json:"organization-name"` //nolint:tagliatelle // RFC 8460 names the wire key.
		}
		if json.Unmarshal(trimmed, &shape) == nil && shape.Policies != nil && shape.Org != nil {
			return report.ClassTLS
		}
	}

	return report.ClassOther
}

func feedbackRoot(data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	decoder.CharsetReader = charset.Reader
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}

		if root, ok := token.(xml.StartElement); ok {
			return root.Name.Local == "feedback"
		}
	}
}

func headerText(header *message.Header, key string) string {
	value, err := header.Text(key)
	if err != nil {
		value = header.Get(key)
	}

	return boundedText(value, report.MaxMedium)
}

func boundedText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\ufffd")
	if len(value) <= limit {
		return value
	}

	for !utf8.RuneStart(value[limit]) {
		limit--
	}

	return value[:limit]
}
