// Package config loads the configuration from environment variables. Every
// variable may instead be read from a file named by <NAME>_FILE.
package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig/v2"
)

// TLS modes for the IMAP connection.
const (
	TLSModeTLS      = "tls"
	TLSModeStartTLS = "starttls"
	TLSModeNone     = "none"
)

type Config struct {
	// SourcesFile is an optional YAML file of source rules.
	SourcesFile string `env:"SOURCES_FILE"`
	// IMAPTLS is one of tls, starttls or none; none is only allowed for a
	// loopback server.
	IMAPTLS string `env:"IMAP_TLS, default=tls"`
	// IMAPCAFile adds a trust root for the IMAP connection only.
	IMAPCAFile   string `env:"IMAP_CA_FILE"`
	IMAPUsername string `env:"IMAP_USERNAME"`
	IMAPPassword string `env:"IMAP_PASSWORD"`
	// MetricsAddr serves /metrics. Empty disables the listener.
	MetricsAddr string `env:"METRICS_ADDR, default=:9090"`
	// ListenAddr is the HTTP listen address.
	ListenAddr string `env:"LISTEN_ADDR, default=:8080"`
	// IMAPAddr is host:port of the IMAP server. IMAP is disabled when empty.
	IMAPAddr string `env:"IMAP_ADDR"`
	// DBPath is the SQLite database file.
	DBPath string `env:"DB_PATH, default=./rua.db"`
	// IMAPMailboxes are examined in order. A missing mailbox is an error.
	IMAPMailboxes []string `env:"IMAP_MAILBOXES, default=INBOX"`
	// MaxMessageBytes skips larger messages (headers only are kept).
	MaxMessageBytes int64 `env:"MAX_MESSAGE_BYTES, default=26214400"`
	// RetentionDays deletes reports whose window ended earlier. 0 keeps all.
	RetentionDays int `env:"RETENTION_DAYS, default=0"`
	// StaleAfter marks the ingest stale when the last successful fetch is
	// older.
	StaleAfter time.Duration `env:"STALE_AFTER, default=3h"`
	// FetchInterval is the base interval between fetches (±10% jitter).
	FetchInterval time.Duration `env:"FETCH_INTERVAL, default=30m"`
	LogLevel      slog.Level    `env:"LOG_LEVEL, default=INFO"`
	// Lookups enables PTR and ASN enrichment.
	Lookups bool `env:"LOOKUPS, default=on"`
}

// IMAPEnabled reports whether a mailbox is configured.
func (cfg *Config) IMAPEnabled() bool { return cfg.IMAPAddr != "" }

// Load reads the configuration from the environment.
func Load(ctx context.Context) (*Config, error) {
	return load(ctx, envconfig.OsLookuper(), os.ReadFile)
}

func load(
	ctx context.Context,
	env envconfig.Lookuper,
	readFile func(string) ([]byte, error),
) (*Config, error) {
	cfg := new(Config)
	lookuper := &fileLookuper{next: env, readFile: readFile, err: nil}

	err := envconfig.ProcessWith(ctx, &envconfig.Config{
		Target:             cfg,
		Lookuper:           lookuper,
		DefaultDelimiter:   ",",
		DefaultSeparator:   "",
		DefaultNoInit:      false,
		DefaultOverwrite:   false,
		DefaultDecodeUnset: false,
		DefaultRequired:    false,
		Mutators:           []envconfig.Mutator{envconfig.MutatorFunc(onOff)},
	})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if lookuper.err != nil {
		return nil, fmt.Errorf("load config: %w", lookuper.err)
	}

	err = cfg.validate()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return cfg, nil
}

var (
	errTLSMode         = errors.New("IMAP_TLS must be tls, starttls or none")
	errNoneNotLoopback = errors.New("IMAP_TLS=none is only allowed for a loopback IMAP_ADDR")
	errCredentials     = errors.New("IMAP_USERNAME and IMAP_PASSWORD are required with IMAP_ADDR")
	errMailboxes       = errors.New("IMAP_MAILBOXES must name at least one mailbox")
	errPositive        = errors.New("must be positive")
	errNegative        = errors.New("must not be negative")
)

func (cfg *Config) validate() error {
	mailboxes := make([]string, 0, len(cfg.IMAPMailboxes))

	for _, name := range cfg.IMAPMailboxes {
		name = strings.TrimSpace(name)
		if name != "" {
			mailboxes = append(mailboxes, name)
		}
	}

	cfg.IMAPMailboxes = mailboxes

	switch {
	case cfg.FetchInterval <= 0:
		return fmt.Errorf("FETCH_INTERVAL %w", errPositive)
	case cfg.StaleAfter <= 0:
		return fmt.Errorf("STALE_AFTER %w", errPositive)
	case cfg.MaxMessageBytes <= 0:
		return fmt.Errorf("MAX_MESSAGE_BYTES %w", errPositive)
	case cfg.RetentionDays < 0:
		return fmt.Errorf("RETENTION_DAYS %w", errNegative)
	}

	if !cfg.IMAPEnabled() {
		return nil
	}

	switch cfg.IMAPTLS {
	case TLSModeTLS, TLSModeStartTLS:
	case TLSModeNone:
		if !loopback(cfg.IMAPAddr) {
			return errNoneNotLoopback
		}
	default:
		return errTLSMode
	}

	if cfg.IMAPUsername == "" || cfg.IMAPPassword == "" {
		return errCredentials
	}

	if len(cfg.IMAPMailboxes) == 0 {
		return errMailboxes
	}

	return nil
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// onOff accepts on/off for LOOKUPS.
func onOff(_ context.Context, key, _, _, resolvedValue string) (string, bool, error) {
	if key != "LOOKUPS" {
		return resolvedValue, false, nil
	}

	switch strings.ToLower(strings.TrimSpace(resolvedValue)) {
	case "on":
		return "true", false, nil
	case "off":
		return "false", false, nil
	default:
		return resolvedValue, false, nil
	}
}

// fileLookuper resolves NAME from NAME_FILE when NAME itself is unset.
type fileLookuper struct {
	next     envconfig.Lookuper
	readFile func(string) ([]byte, error)
	err      error
}

func (lookuper *fileLookuper) Lookup(key string) (string, bool) {
	value, ok := lookuper.next.Lookup(key)
	if ok {
		return value, true
	}

	path, ok := lookuper.next.Lookup(key + "_FILE")
	if !ok || path == "" {
		return "", false
	}

	data, err := lookuper.readFile(path)
	if err != nil {
		lookuper.err = errors.Join(lookuper.err, fmt.Errorf("%s_FILE: %w", key, err))

		return "", false
	}

	return strings.TrimRight(string(data), "\r\n"), true
}
