package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const mboxSeparator = "From "

var (
	errImportType   = errors.New("unsupported import file type")
	errImportReport = errors.New("report processing failed; see stored message status")
)

// Import ingests report files, RFC 5322 messages and mbox mailboxes.
// Directories are walked recursively. Errors are joined after all paths run.
func (pipe *Pipeline) Import(ctx context.Context, paths []string) (ImportSummary, error) {
	var (
		summary  ImportSummary
		failures error
	)

	for _, path := range paths {
		err := filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
			err := ctx.Err()
			if err != nil {
				return fmt.Errorf("import canceled: %w", err)
			}

			switch {
			case walkErr != nil:
				summary.Failed++
			case entry.Type().IsRegular():
				walkErr = pipe.importFile(ctx, name, name == path, &summary)
			case name == path && !entry.IsDir():
				summary.Failed++
				walkErr = errImportType
			}

			if walkErr != nil {
				failures = errors.Join(failures, fmt.Errorf("%s: %w", name, walkErr))
			}

			return nil
		})
		if err != nil {
			return summary, errors.Join(failures, err)
		}
	}

	return summary, failures
}

func (pipe *Pipeline) importFile(
	ctx context.Context, path string, explicit bool, summary *ImportSummary,
) error {
	file, err := os.Open(path)
	if err != nil {
		summary.Failed++

		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	ext := strings.ToLower(filepath.Ext(path))

	prefix, err := reader.Peek(len(mboxSeparator))
	if err != nil && !errors.Is(err, io.EOF) {
		summary.Failed++

		return fmt.Errorf("read file signature: %w", err)
	}

	mbox := bytes.Equal(prefix, []byte(mboxSeparator))

	switch ext {
	case ".gz", ".zip", ".xml", ".json", ".eml", ".mbox":
		// These types are always attempted. Mbox is recognized by content.
	default:
		if !mbox {
			if explicit {
				summary.Failed++

				return errImportType
			}

			return nil
		}
	}

	summary.Files++

	info, err := file.Stat()
	if err != nil {
		summary.Failed++

		return fmt.Errorf("stat file: %w", err)
	}

	if mbox {
		return pipe.importMbox(ctx, reader, info.ModTime(), summary)
	}

	raw, err := io.ReadAll(io.LimitReader(reader, MaxMessageBytes+1))
	switch {
	case err != nil:
		summary.Failed++

		return fmt.Errorf("read file: %w", err)
	case len(raw) > MaxMessageBytes:
		summary.Failed++

		return errMessageLimit
	case ext == ".mbox":
		summary.Failed++

		return errImportType
	}

	return pipe.importMessage(ctx, raw, info.ModTime(), ext == ".eml", summary)
}

func (pipe *Pipeline) importMessage(
	ctx context.Context, raw []byte, date time.Time, email bool, summary *ImportSummary,
) error {
	if email {
		entity, headerErr := readHeader(raw)
		if headerErr == nil {
			parsed, err := mail.ParseDate(entity.Header.Get("Date"))
			if err == nil {
				date = parsed
			}
		}
	}

	summary.Messages++

	result, err := pipe.Ingest(ctx, raw, Source{
		InternalDate: date, Mailbox: "", UID: 0, UIDValidity: 0,
	})
	switch {
	case err != nil:
		summary.Failed++

		return err
	case result.Known:
		summary.Known++
	case result.Status == StatusError || result.Status == StatusPartial:
		summary.Failed++

		return errImportReport
	}

	return nil
}

type mboxImport struct {
	mtime     time.Time
	summary   *ImportSummary
	failures  error
	raw       []byte
	number    int
	oversized bool
}

func (box *mboxImport) flush(ctx context.Context, pipe *Pipeline) {
	if box.number == 0 {
		return
	}

	var err error

	if box.oversized {
		box.summary.Messages++
		box.summary.Failed++
		err = errMessageLimit
	} else {
		err = pipe.importMessage(ctx, box.raw, box.mtime, true, box.summary)
	}

	if err != nil {
		box.failures = errors.Join(box.failures, fmt.Errorf("mbox message %d: %w", box.number, err))
	}

	box.raw = box.raw[:0]
	box.oversized = false
}

func (pipe *Pipeline) importMbox(
	ctx context.Context, reader *bufio.Reader, mtime time.Time, summary *ImportSummary,
) error {
	box := mboxImport{
		mtime:     mtime,
		summary:   summary,
		failures:  nil,
		raw:       nil,
		number:    0,
		oversized: false,
	}
	lineStart := true

	for {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return errors.Join(box.failures, fmt.Errorf("read mbox: %w", ctxErr))
		}

		line, err := reader.ReadSlice('\n')
		if len(line) > 0 {
			if lineStart && bytes.HasPrefix(line, []byte(mboxSeparator)) {
				box.flush(ctx, pipe)
				box.number++
				// Discard all fragments of an unusually long separator line.
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = reader.ReadSlice('\n')
				}
			} else {
				box.append(line, lineStart)
			}
		}

		lineStart = !errors.Is(err, bufio.ErrBufferFull)
		if lineStart && err != nil {
			box.flush(ctx, pipe)

			if !errors.Is(err, io.EOF) {
				summary.Failed++
				box.failures = errors.Join(box.failures, fmt.Errorf("read mbox: %w", err))
			}

			return box.failures
		}
	}
}

func (box *mboxImport) append(fragment []byte, lineStart bool) {
	if box.oversized {
		return
	}

	if lineStart && fragment[0] == '>' &&
		bytes.HasPrefix(bytes.TrimLeft(fragment, ">"), []byte(mboxSeparator)) {
		fragment = fragment[1:]
	}

	if len(box.raw)+len(fragment) > MaxMessageBytes {
		box.oversized = true

		return
	}

	box.raw = append(box.raw, fragment...)
}
