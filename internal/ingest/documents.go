package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
	"github.com/jmoiron/sqlx"

	"github.com/zekihan/rua/internal/store"
)

// Document kinds stored in the documents table for failure-containing
// messages. A feedback document and its original-headers document are
// paired by consecutive sequence values, feedback first.
const (
	docOuterHeaders    = "outer-headers"
	docAggregate       = "aggregate"
	docTLS             = "tls"
	docFeedback        = "feedback"
	docOriginalHeaders = "original-headers"
	docExtractError    = "extract-error"
)

// storedDoc is one extraction output row, with nil data when the
// stored blob is NULL: extract errors and parts that could not be
// read, which carry errText instead.
type storedDoc struct { //nolint:govet // Kind, data, error: logical grouping.
	kind    string
	data    []byte
	errText string
}

func writeDocuments(
	ctx context.Context, tx *sqlx.Tx, messageID int64, docs []storedDoc,
) error {
	err := deleteDocuments(ctx, tx, messageID)
	if err != nil {
		return err
	}

	for seq, doc := range docs {
		var blob []byte
		if doc.data != nil {
			blob, err = store.PutBlob(ctx, tx, doc.data)
			if err != nil {
				return fmt.Errorf("save message document: %w", err)
			}
		}

		_, err = tx.ExecContext(ctx, `INSERT INTO documents (message, seq, kind, blob, error)
			VALUES (?, ?, ?, ?, ?)`, messageID, seq, doc.kind, blob, doc.errText)
		if err != nil {
			return fmt.Errorf("insert message document: %w", err)
		}
	}

	return nil
}

func deleteDocuments(ctx context.Context, tx *sqlx.Tx, messageID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE message = ?`, messageID)
	if err != nil {
		return fmt.Errorf("delete message documents: %w", err)
	}

	return nil
}

func loadDocuments(
	ctx context.Context, tx *sqlx.Tx, messageID int64,
) ([]storedDoc, error) {
	var rows []struct {
		Kind    string `db:"kind"`
		ErrText string `db:"error"`
		Blob    []byte `db:"blob"`
	}

	err := tx.SelectContext(ctx, &rows, `SELECT kind, blob, error FROM documents
		WHERE message = ? ORDER BY seq`, messageID)
	if err != nil {
		return nil, fmt.Errorf("load message documents: %w", err)
	}

	docs := make([]storedDoc, 0, len(rows))

	for index := range rows {
		doc := storedDoc{kind: rows[index].Kind, data: nil, errText: rows[index].ErrText}
		if len(rows[index].Blob) > 0 {
			doc.data, err = store.GetBlob(ctx, tx, rows[index].Blob)
			if err != nil {
				return nil, fmt.Errorf("read message document blob: %w", err)
			}
		}

		docs = append(docs, doc)
	}

	return docs, nil
}

var allowlistedOuter = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-Id", "Mime-Version",
}

func isAllowlistedOuter(key string) bool {
	for _, name := range allowlistedOuter {
		if strings.EqualFold(key, name) {
			return true
		}
	}

	return false
}

// outerHeaderBlock returns the allowlisted top-level header fields, in
// their original order and raw form. No other outer field is stored
// for a failure-containing message, including any X-Rua-* field.
func outerHeaderBlock(header *message.Header) []byte {
	var out bytes.Buffer

	fields := header.Fields()
	for fields.Next() {
		key := fields.Key()
		if !isAllowlistedOuter(key) {
			continue
		}

		raw, err := fields.Raw()
		if err != nil || len(raw) == 0 {
			fmt.Fprintf(&out, "%s: %s\r\n", key, fields.Value())

			continue
		}

		out.Write(raw)

		if !bytes.HasSuffix(raw, []byte("\r\n")) && !bytes.HasSuffix(raw, []byte("\n")) {
			out.WriteString("\r\n")
		}
	}

	return out.Bytes()
}

// headerOnly returns only the header field block of data. Any text
// after the blank line ending the fields, which is a message body, is
// dropped.
func headerOnly(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	block, err := readHeaderBlock(bytes.NewReader(data))
	if err != nil {
		return headerBlock(data)
	}

	return block
}

func readHeaderBlock(body io.Reader) ([]byte, error) {
	buffered := bufio.NewReader(io.LimitReader(body, MaxReportBytes))

	var data []byte

	lineStart := true

	for {
		line, err := buffered.ReadSlice('\n')
		data = append(data, line...)

		continuation := errors.Is(err, bufio.ErrBufferFull)
		if !continuation && err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read header block: %w", err)
		}

		if err != nil && !continuation ||
			lineStart && (bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n"))) {
			break
		}

		lineStart = !continuation
	}

	return data, nil
}

func headerBlock(raw []byte) []byte {
	for _, sep := range [][]byte{
		[]byte("\r\n\r\n"), []byte("\n\n"), []byte("\r\n\n"), []byte("\n\r\n"),
	} {
		if idx := bytes.Index(raw, sep); idx >= 0 {
			return raw[:idx+len(sep)]
		}
	}

	trimmed := bytes.TrimRight(raw, "\r\n")

	out := make([]byte, 0, len(trimmed)+len("\r\n\r\n"))
	out = append(out, trimmed...)
	out = append(out, "\r\n\r\n"...)

	return out
}
