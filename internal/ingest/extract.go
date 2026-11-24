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

type document struct {
	class           report.Class
	data            []byte
	originalHeaders []byte
}

type extraction struct {
	firstError   error
	accept       func(document) error
	reader       *bodyReader
	class        report.Class
	failureCount int
	hasFailure   bool
}

func newExtraction(accept func(document) error) *extraction {
	return &extraction{
		class:        report.ClassOther,
		reader:       newBodyReader(),
		accept:       accept,
		firstError:   nil,
		failureCount: 0,
		hasFailure:   false,
	}
}

func (ext *extraction) classify(class report.Class) {
	if ext.class == report.ClassOther {
		ext.class = class
	}
}

func (ext *extraction) failed(err error) {
	if ext.firstError == nil {
		ext.firstError = err
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

	hooks := &mimeHooks{
		fail:           ext.failed,
		leaf:           ext.leaf,
		multipartStart: nil,
		multipartEnd:   nil,
		failurePair:    ext.failurePair,
	}

	return walkMIME(entity, 0, ext.reader, hooks)
}

func (ext *extraction) leaf(entity *message.Entity, mediaType string) error {
	return ext.part(entity.Body, mediaType, false)
}

func (ext *extraction) failurePair(feedback, headers []byte, headerError bool) error {
	return ext.failure(failureParts{
		feedback: feedback, headers: headers, headerError: headerError,
	})
}

func (ext *extraction) failure(parts failureParts) error {
	if len(parts.feedback) == 0 {
		return nil
	}

	entity, err := readHeader(parts.feedback)
	if err != nil {
		ext.failed(fmt.Errorf("read feedback fields: %w", err))

		return nil
	}

	if !strings.EqualFold(entity.Header.Get("Feedback-Type"), "auth-failure") {
		return nil
	}

	ext.classify(report.ClassFailure)
	ext.hasFailure = true
	ext.failureCount++

	if parts.headerError {
		return nil
	}

	return ext.accept(document{
		class: report.ClassFailure, data: parts.feedback,
		originalHeaders: parts.headers,
	})
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
	if mediaType == typeTLSJSON || mediaType == typeTLSGzip {
		hint = report.ClassTLS
		ext.classify(hint)

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
			ext.failed(err)

			return nil
		}

		return ext.deliver(data, hint, expected)
	}
}

func (ext *extraction) gzip(reader io.Reader, hint report.Class) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		ext.failed(fmt.Errorf("open gzip report: %w", err))

		return nil
	}

	data, err := ext.reader.read(gz)

	err = errors.Join(err, gz.Close())
	if err != nil {
		ext.failed(err)

		return nil
	}

	if isZip(data) || isGzip(data) {
		ext.failed(errNestedArchive)

		return nil
	}

	return ext.deliver(data, hint, true)
}

func (ext *extraction) zip(reader io.Reader, hint report.Class) error {
	// ZIP requires random access to the compressed bytes. The input MIME bytes
	// are already bounded by MaxMessageBytes; each entry has a separate cap.
	data, err := io.ReadAll(io.LimitReader(reader, int64(MaxMessageBytes)+1))
	if err != nil {
		ext.failed(fmt.Errorf("read zip archive: %w", err))

		return nil
	}

	if len(data) > MaxMessageBytes {
		ext.failed(errMessageLimit)

		return nil
	}

	return ext.openZip(data, hint)
}

func (ext *extraction) openZip(data []byte, hint report.Class) error {
	err := checkZipDirectory(data)
	if err != nil {
		ext.failed(err)

		return nil
	}

	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		ext.failed(fmt.Errorf("open zip archive: %w", err))

		return nil
	}

	if len(archive.File) > MaxZipEntries {
		ext.failed(errZipEntries)

		return nil
	}

	err = checkZipEntries(archive.File)
	if err != nil {
		ext.failed(err)

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
			ext.failed(fmt.Errorf("open zip entry: %w", err))

			continue
		}

		data, err := ext.reader.read(reader)

		err = errors.Join(err, reader.Close())
		if err != nil {
			ext.failed(err)

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
		ext.failed(errNotReport)
	}

	return nil
}

func (ext *extraction) deliver(data []byte, hint report.Class, expected bool) error {
	class := documentClass(data, hint)
	if class != report.ClassOther {
		return ext.deliverClass(data, class)
	}

	if expected {
		ext.failed(errNotReport)
	}

	return nil
}

func (ext *extraction) deliverClass(data []byte, class report.Class) error {
	ext.classify(class)

	return ext.accept(document{class: class, data: data, originalHeaders: nil})
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
