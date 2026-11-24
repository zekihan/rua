package demo

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/zekihan/rua/internal/ingest"
)

func (gen *generator) edges(ctx context.Context, pipe *ingest.Pipeline) error {
	err := gen.ingest(ctx, pipe, brokenMessage(), gen.now)
	if err != nil {
		return err
	}

	err = gen.ingest(ctx, pipe, otherMessage(), gen.now)
	if err != nil {
		return err
	}

	err = gen.failure(ctx, pipe, orgCedar, mailCedar, subjectFailCom, domainCom, ipSpoof4, "dmarc")
	if err != nil {
		return err
	}

	err = gen.failure(
		ctx,
		pipe,
		orgHarbor,
		mailHarbor,
		subjectFailNet,
		domainNet,
		ipForward4,
		methodDKIM,
	)
	if err != nil {
		return err
	}

	return gen.skip(ctx, pipe)
}

func (gen *generator) failure(
	ctx context.Context,
	pipe *ingest.Pipeline,
	org, from, subject, domain, ip, authFailure string,
) error {
	when := gen.now.Add(-2 * dayLength)
	if domain == domainNet {
		when = gen.now.Add(-40 * dayLength)
	}

	raw := failureMessage(
		from,
		subject,
		feedbackBody(org, domain, ip, authFailure, when),
		originalHeaders(domain),
	)

	err := gen.ingest(ctx, pipe, raw, when)
	if err != nil {
		return fmt.Errorf("ingest failure report: %w", err)
	}

	return nil
}

func (gen *generator) skip(ctx context.Context, pipe *ingest.Pipeline) error {
	header := []byte("From: huge@mailbox.example\r\nSubject: " + subjectOversized + "\r\n")
	gen.uid++

	_, err := pipe.Skip(ctx, header, oversizedBytes, ingest.Source{
		Mailbox: mailboxDemo, UID: gen.uid, InternalDate: gen.now,
	})
	if err != nil {
		return fmt.Errorf("record oversized demo message: %w", err)
	}

	return nil
}

func brokenMessage() []byte {
	body := []byte(
		`<?xml version="1.0" encoding="UTF-8"?><feedback><version>1.0</version></feedback>`,
	)

	return mimeAttachment(mailCedar, subjectBroken, mediaXML, fileXML, body)
}

func otherMessage() []byte {
	return []byte("From: notes@mailbox.example\r\nSubject: " + subjectOther +
		"\r\n\r\nThis message is not a report.\r\n")
}

func mimeAttachment(from, subject, mediaType, filename string, payload []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(payload)
	dst := make(
		[]byte,
		0,
		len(encoded)+len(from)+len(subject)+len(mediaType)+len(filename)+mimeSlack,
	)
	dst = append(dst, "From: "...)
	dst = append(dst, from...)
	dst = append(dst, "\r\nSubject: "...)
	dst = append(dst, subject...)
	dst = append(
		dst,
		"\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=rua-demo\r\n\r\n"...)
	dst = append(dst, "--rua-demo\r\nContent-Type: "...)
	dst = append(dst, mediaType...)
	dst = append(
		dst,
		"\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename="...)
	dst = append(dst, filename...)
	dst = append(dst, "\r\n\r\n"...)
	dst = append(dst, encoded...)
	dst = append(dst, "\r\n--rua-demo--\r\n"...)

	return dst
}

func gzipBytes(doc []byte) ([]byte, error) {
	var compressed bytes.Buffer

	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return nil, fmt.Errorf("create gzip report: %w", err)
	}

	writer.ModTime = time.Unix(0, 0).UTC()

	_, err = writer.Write(doc)
	if err != nil {
		return nil, fmt.Errorf("write gzip report: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("close gzip report: %w", err)
	}

	return compressed.Bytes(), nil
}

func zipBytes(doc []byte) ([]byte, error) {
	var compressed bytes.Buffer

	archive := zip.NewWriter(&compressed)
	header := &zip.FileHeader{ //nolint:exhaustruct_v5 // Deprecated zip fields stay unset.
		Name:     fileXML,
		Method:   zip.Deflate,
		Modified: time.Unix(0, 0).UTC(),
	}

	entry, err := archive.CreateHeader(header)
	if err != nil {
		return nil, fmt.Errorf("create zip entry: %w", err)
	}

	_, err = entry.Write(doc)
	if err != nil {
		return nil, fmt.Errorf("write zip entry: %w", err)
	}

	err = archive.Close()
	if err != nil {
		return nil, fmt.Errorf("close zip report: %w", err)
	}

	return compressed.Bytes(), nil
}

func failureMessage(from, subject, feedback, headers string) []byte {
	dst := make([]byte, 0, len(from)+len(subject)+len(feedback)+len(headers)+failureSlack)
	dst = append(dst, "From: "...)
	dst = append(dst, from...)
	dst = append(dst, "\r\nSubject: "...)
	dst = append(dst, subject...)
	dst = append(
		dst,
		"\r\nMIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=feedback-report; boundary=ruf\r\n\r\n"...)
	dst = append(dst, "--ruf\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"...)
	dst = append(dst, "DMARC authentication failure report.\r\n"...)
	dst = append(dst, "--ruf\r\nContent-Type: message/feedback-report\r\n\r\n"...)
	dst = append(dst, feedback...)
	dst = append(dst, "\r\n--ruf\r\nContent-Type: text/rfc822-headers\r\n\r\n"...)
	dst = append(dst, headers...)
	dst = append(dst, "\r\n--ruf--\r\n"...)

	return dst
}

func feedbackBody(org, domain, ip, authFailure string, when time.Time) string {
	return "Feedback-Type: auth-failure\r\nUser-Agent: " + org +
		" Reporter\r\nVersion: 1.0\r\nAuth-Failure: " + authFailure +
		"\r\nArrival-Date: " + when.Format(time.RFC1123Z) +
		"\r\nSource-IP: " + ip +
		"\r\nReported-Domain: " + domain +
		"\r\nOriginal-Mail-From: <bounce@" + domain + ">\r\n"
}

func originalHeaders(domain string) string {
	return "From: bounce@" + domain + "\r\nTo: person@mailbox.example\r\nSubject: notice for " + domain + "\r\n"
}
