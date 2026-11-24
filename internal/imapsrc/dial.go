package imapsrc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	tlsImplicit = "tls"
	tlsStart    = "starttls"
	tlsNone     = "none"

	dialTimeout = 30 * time.Second
)

var (
	errTLSMode       = errors.New("tls mode must be tls, starttls or none")
	errPlainLoopback = errors.New("IMAP without TLS is only allowed for a loopback address")
	errCAParse       = errors.New("parse IMAP CA file")
)

func (fetcher *Fetcher) connect(ctx context.Context) (*imapSession, error) {
	var last error

	attempts := len(connectBackoff) + 1
	for attempt := range attempts {
		if attempt > 0 {
			err := fetcher.sleep(ctx, connectBackoff[attempt-1])
			if err != nil {
				return nil, err
			}
		}

		session, err := dialIMAP(ctx, &fetcher.cfg)
		if err == nil {
			return session, nil
		}

		if isCancel(err) {
			return nil, err
		}

		last = scrub(err, fetcher.cfg.Password)
		slog.WarnContext(ctx, "IMAP connect failed",
			slog.Int("attempt", attempt+1),
			slog.Any("error", last),
		)
	}

	return nil, fmt.Errorf("connect IMAP: %w", last)
}

func dialIMAP(ctx context.Context, cfg *Config) (*imapSession, error) {
	switch cfg.TLSMode {
	case tlsImplicit, tlsStart, tlsNone:
	default:
		return nil, fmt.Errorf("%w: %q", errTLSMode, cfg.TLSMode)
	}

	if cfg.TLSMode == tlsNone && !loopbackHost(cfg.Addr) {
		return nil, errPlainLoopback
	}

	conn, err := dialTCP(ctx, cfg.Addr)
	if err != nil {
		return nil, err
	}

	client, err := openClient(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}

	err = login(client, cfg)
	if err != nil {
		return nil, err
	}

	return &imapSession{raw: client}, nil
}

func dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	dialer := new(net.Dialer)
	dialer.Timeout = dialTimeout

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial IMAP: %w", err)
	}

	return conn, nil
}

func openClient(ctx context.Context, conn net.Conn, cfg *Config) (*imapclient.Client, error) {
	switch cfg.TLSMode {
	case tlsNone:
		return greet(imapclient.New(conn, nil))
	case tlsImplicit:
		return openImplicitTLS(ctx, conn, cfg)
	case tlsStart:
		return openStartTLS(conn, cfg)
	default:
		return nil, fmt.Errorf("%w: %q", errTLSMode, cfg.TLSMode)
	}
}

func openImplicitTLS(ctx context.Context, conn net.Conn, cfg *Config) (*imapclient.Client, error) {
	tlsConfig, err := connectionTLS(cfg)
	if err != nil {
		return nil, closeConn(conn, err)
	}

	tlsConn := tls.Client(conn, tlsConfig)

	err = tlsConn.HandshakeContext(ctx)
	if err != nil {
		return nil, closeConn(conn, fmt.Errorf("IMAP TLS handshake: %w", err))
	}

	options := new(imapclient.Options)
	options.TLSConfig = tlsConfig

	return greet(imapclient.New(tlsConn, options))
}

func openStartTLS(conn net.Conn, cfg *Config) (*imapclient.Client, error) {
	tlsConfig, err := connectionTLS(cfg)
	if err != nil {
		return nil, closeConn(conn, err)
	}

	options := new(imapclient.Options)
	options.TLSConfig = tlsConfig

	client, err := imapclient.NewStartTLS(conn, options)
	if err != nil {
		return nil, fmt.Errorf("IMAP STARTTLS: %w", err)
	}

	return greet(client)
}

func greet(client *imapclient.Client) (*imapclient.Client, error) {
	err := client.WaitGreeting()
	if err != nil {
		return nil, closeClient(client, fmt.Errorf("IMAP greeting: %w", err))
	}

	return client, nil
}

func login(client *imapclient.Client, cfg *Config) error {
	err := client.Login(cfg.Username, cfg.Password).Wait()
	if err != nil {
		return closeClient(client, fmt.Errorf("IMAP login: %w", scrub(err, cfg.Password)))
	}

	return nil
}

func closeConn(conn net.Conn, err error) error {
	closeErr := conn.Close()
	if closeErr != nil {
		return errors.Join(err, fmt.Errorf("close IMAP connection: %w", closeErr))
	}

	return err
}

func closeClient(client *imapclient.Client, err error) error {
	closeErr := client.Close()
	if closeErr != nil && !benignClose(closeErr) {
		return errors.Join(err, fmt.Errorf("close IMAP connection: %w", closeErr))
	}

	return err
}

// connectionTLS returns a TLS config whose root pool is a copy of the system
// pool, plus CAFile when set. The system pool itself is left unchanged.
func connectionTLS(cfg *Config) (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system CA pool: %w", err)
	}

	if pool == nil {
		pool = x509.NewCertPool()
	}

	pool = pool.Clone()

	if cfg.CAFile != "" {
		pemBytes, readErr := os.ReadFile(cfg.CAFile)
		if readErr != nil {
			return nil, fmt.Errorf("read IMAP CA file: %w", readErr)
		}

		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, errCAParse
		}
	}

	host, _, splitErr := net.SplitHostPort(cfg.Addr)
	if splitErr != nil {
		host = cfg.Addr
	}

	tlsConfig := new(tls.Config)
	tlsConfig.MinVersion = tls.VersionTLS12
	tlsConfig.RootCAs = pool
	tlsConfig.ServerName = host

	return tlsConfig, nil
}

func loopbackHost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}
