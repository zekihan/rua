// Package store owns the SQLite database: opening, migrations, transactions
// and compressed blobs. Each other package keeps the SQL for the tables it
// owns; see schema.go.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/klauspost/compress/zstd"

	// SQLite driver for database/sql.
	_ "modernc.org/sqlite"
)

// Store holds one writer connection and a pool of read-only connections.
// WAL lets readers see the last committed state while a write runs.
type Store struct {
	writer *sqlx.DB
	reader *sqlx.DB
}

var (
	errSchemaVersion = errors.New("unsupported schema version")
	// ErrNotFound is returned by lookups that match no row.
	ErrNotFound = errors.New("not found")
)

// Memory is the path that opens a private in-memory database. Reads and
// writes then share the single connection.
const Memory = ":memory:"

const minReaders = 2

// Open opens (creating if needed) and migrates the database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	writer, err := openDB(path, false)
	if err != nil {
		return nil, err
	}

	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	writer.SetConnMaxIdleTime(0)

	st := &Store{writer: writer, reader: writer}

	err = st.Write(ctx, migrate)
	if err != nil {
		return nil, errors.Join(err, writer.Close())
	}

	if path == Memory {
		return st, nil
	}

	reader, err := openDB(path, true)
	if err != nil {
		return nil, errors.Join(err, writer.Close())
	}

	reader.SetMaxOpenConns(max(minReaders, runtime.GOMAXPROCS(0)))
	st.reader = reader

	return st, nil
}

func openDB(path string, readOnly bool) (*sqlx.DB, error) {
	pragmas := []string{"busy_timeout(5000)", "foreign_keys(1)"}
	if readOnly {
		pragmas = append(pragmas, "query_only(1)")
	} else {
		pragmas = append(pragmas, "journal_mode(WAL)", "synchronous(NORMAL)")
	}

	options := url.Values{"_pragma": pragmas}
	if !readOnly {
		options.Set("_txlock", "immediate")
	}

	dsn := new(url.URL)
	dsn.Scheme = "file"
	dsn.RawQuery = options.Encode()

	if path == Memory {
		dsn.Opaque = path
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("database path: %w", err)
		}

		dsn.Path = absolute
	}

	db, err := sqlx.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	return db, nil
}

func migrate(ctx context.Context, tx *sqlx.Tx) error {
	var version int

	err := tx.GetContext(ctx, &version, "PRAGMA user_version")
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if version < 0 || version > len(Migrations) {
		return fmt.Errorf("%w %d", errSchemaVersion, version)
	}

	for index := version; index < len(Migrations); index++ {
		_, err = tx.ExecContext(ctx, Migrations[index])
		if err != nil {
			return fmt.Errorf("migration %d: %w", index+1, err)
		}

		_, err = tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(index+1))
		if err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
	}

	return nil
}

// Close closes both pools.
func (s *Store) Close() error {
	err := s.writer.Close()
	if s.reader != s.writer {
		err = errors.Join(err, s.reader.Close())
	}

	if err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}

	return nil
}

// Ping checks database liveness.
func (s *Store) Ping(ctx context.Context) error {
	err := s.reader.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}

	return nil
}

// Read runs fn in a read-only transaction on the reader pool.
func (s *Store) Read(ctx context.Context, fn func(context.Context, *sqlx.Tx) error) error {
	return transaction(
		ctx,
		s.reader,
		&sql.TxOptions{Isolation: sql.LevelDefault, ReadOnly: true},
		fn,
	)
}

// Write runs fn in an immediate write transaction on the writer connection.
// Writes are serialized; keep them short.
func (s *Store) Write(ctx context.Context, fn func(context.Context, *sqlx.Tx) error) error {
	return transaction(ctx, s.writer, nil, fn)
}

func transaction(
	ctx context.Context,
	db *sqlx.DB,
	options *sql.TxOptions,
	fn func(context.Context, *sqlx.Tx) error,
) error {
	tx, err := db.BeginTxx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin sqlite transaction: %w", err)
	}

	err = fn(ctx, tx)
	if err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rollbackErr))
		}

		return err
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit sqlite transaction: %w", err)
	}

	return nil
}

var (
	encoder = must(zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression)))
	decoder = must(zstd.NewReader(nil, zstd.WithDecoderConcurrency(0)))
)

// must panics on an error from a constructor whose options are constant.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}

	return value
}

// Digest returns the SHA-256 of data, the key used for blobs and messages.
func Digest(data []byte) []byte {
	sum := sha256.Sum256(data)

	return sum[:]
}

// PutBlob stores data compressed under its digest, if not already present,
// and returns the digest.
func PutBlob(ctx context.Context, tx *sqlx.Tx, data []byte) ([]byte, error) {
	digest := Digest(data)

	_, err := tx.ExecContext(ctx,
		`INSERT INTO blobs (sha256, size, data) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		digest, len(data), encoder.EncodeAll(data, nil))
	if err != nil {
		return nil, fmt.Errorf("store blob: %w", err)
	}

	return digest, nil
}

// GetBlob returns the uncompressed blob with the given digest.
func GetBlob(ctx context.Context, q sqlx.QueryerContext, digest []byte) ([]byte, error) {
	var compressed []byte

	err := sqlx.GetContext(ctx, q, &compressed, `SELECT data FROM blobs WHERE sha256 = ?`, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("read blob: %w", err)
	}

	data, err := decoder.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("decompress blob: %w", err)
	}

	return data, nil
}

// Meta reads a meta value; ok is false when unset.
func Meta(ctx context.Context, q sqlx.QueryerContext, key string) (string, bool, error) {
	var value string

	err := sqlx.GetContext(ctx, q, &value, `SELECT value FROM meta WHERE key = ?`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("read meta %s: %w", key, err)
	}

	return value, true, nil
}

// SetMeta writes a meta value.
func SetMeta(ctx context.Context, tx *sqlx.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write meta %s: %w", key, err)
	}

	return nil
}

// MetaParserVersion is the meta key holding the parser version the derived
// rows were produced with.
const MetaParserVersion = "parser_version"
