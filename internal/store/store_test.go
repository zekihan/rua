package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
)

//nolint:gocyclo // One linear scenario.
func TestOpenMigratesAndRoundTripsBlobs(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rua.db")

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	data := bytes.Repeat([]byte("<feedback/>"), 1000)

	var digest []byte

	err = st.Write(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		var putErr error

		digest, putErr = PutBlob(ctx, tx, data)
		if putErr != nil {
			return putErr
		}

		_, putErr = PutBlob(ctx, tx, data)
		if putErr != nil {
			return putErr
		}

		return SetMeta(ctx, tx, MetaParserVersion, "1")
	})
	if err != nil {
		t.Fatal(err)
	}

	err = st.Close()
	if err != nil {
		t.Fatal(err)
	}

	st, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	err = st.Read(t.Context(), func(ctx context.Context, tx *sqlx.Tx) error {
		got, getErr := GetBlob(ctx, tx, digest)
		if getErr != nil {
			return getErr
		}

		if !bytes.Equal(got, data) {
			t.Error("blob changed in the round trip")
		}

		_, getErr = GetBlob(ctx, tx, Digest([]byte("missing")))
		if !errors.Is(getErr, ErrNotFound) {
			t.Errorf("missing blob: %v", getErr)
		}

		value, ok, getErr := Meta(ctx, tx, MetaParserVersion)
		if getErr != nil || !ok || value != "1" {
			t.Errorf("meta = %q, %v, %v", value, ok, getErr)
		}

		var version int

		getErr = tx.GetContext(ctx, &version, "PRAGMA user_version")
		if getErr != nil || version != len(Migrations) {
			t.Errorf("user_version = %d, %v", version, getErr)
		}

		_, getErr = tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('x', 'y')")
		if getErr == nil {
			t.Error("reader transaction accepted a write")
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMemory(t *testing.T) {
	t.Parallel()

	st, err := Open(t.Context(), Memory)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	err = st.Ping(t.Context())
	if err != nil {
		t.Fatal(err)
	}
}
