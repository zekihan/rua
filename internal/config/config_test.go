package config

import (
	"errors"
	"io/fs"
	"maps"
	"testing"
	"time"

	"github.com/sethvargo/go-envconfig/v2"
)

func noFiles(string) ([]byte, error) { return nil, fs.ErrNotExist }

func TestDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := load(t.Context(), envconfig.MapLookuper(map[string]string{}), noFiles)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.IMAPEnabled() || cfg.FetchInterval != 30*time.Minute || cfg.StaleAfter != 3*time.Hour ||
		cfg.MaxMessageBytes != 26214400 || cfg.DBPath != "./rua.db" || !cfg.Lookups ||
		cfg.ListenAddr != ":8080" || cfg.MetricsAddr != ":9090" ||
		len(cfg.IMAPMailboxes) != 1 || cfg.IMAPMailboxes[0] != "INBOX" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestIMAP(t *testing.T) {
	t.Parallel()

	files := func(path string) ([]byte, error) {
		if path == "/run/secrets/password" {
			return []byte("s3cret\n"), nil
		}

		return nil, fs.ErrNotExist
	}

	//nolint:gosec // Test fixture, not a credential.
	cfg, err := load(t.Context(), envconfig.MapLookuper(map[string]string{
		"IMAP_ADDR":          "imap.example.com:993",
		"IMAP_USERNAME":      "reports@example.com",
		"IMAP_PASSWORD_FILE": "/run/secrets/password",
		"IMAP_MAILBOXES":     "Folders/dmarc, Folders/tls ,",
		"LOOKUPS":            "off",
	}), files)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.IMAPPassword != "s3cret" || cfg.Lookups || len(cfg.IMAPMailboxes) != 2 ||
		cfg.IMAPMailboxes[1] != "Folders/tls" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestInvalid(t *testing.T) {
	t.Parallel()

	base := map[string]string{
		"IMAP_ADDR": "imap.example.com:993", "IMAP_USERNAME": "u", "IMAP_PASSWORD": "p",
	}

	cases := map[string]map[string]string{
		"missing password": {"IMAP_ADDR": "imap.example.com:993", "IMAP_USERNAME": "u"},
		"bad tls":          {"IMAP_TLS": "ssl"},
		"none remote":      {"IMAP_TLS": "none"},
		"no mailboxes":     {"IMAP_MAILBOXES": " , "},
		"zero interval":    {"FETCH_INTERVAL": "0s"},
		"negative days":    {"RETENTION_DAYS": "-1"},
		"unreadable file":  {"IMAP_PASSWORD": "", "IMAP_PASSWORD_FILE": "/missing"},
	}

	for name, overrides := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := map[string]string{}

			if name != "missing password" {
				maps.Copy(env, base)
			}

			maps.Copy(env, overrides)

			if name == "unreadable file" {
				delete(env, "IMAP_PASSWORD")
			}

			_, err := load(t.Context(), envconfig.MapLookuper(env), noFiles)
			if err == nil {
				t.Fatalf("expected an error for %v", env)
			}
		})
	}

	_, err := load(t.Context(), envconfig.MapLookuper(map[string]string{
		"IMAP_ADDR":     "127.0.0.1:1143",
		"IMAP_TLS":      "none",
		"IMAP_USERNAME": "u",
		"IMAP_PASSWORD": "p",
	}), noFiles)
	if err != nil && !errors.Is(err, errNoneNotLoopback) {
		t.Fatal(err)
	}

	if err != nil {
		t.Fatal("loopback none must be allowed")
	}
}
