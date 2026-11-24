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

		session, err := dialIMAP(ctx, &fetcher.cfg, fetcher.limits)
		if err == nil {
			return session, nil
		}

		if isCancel(err) {
			return nil, err
		}

		last = fetcher.safe(err)
		fetcher.log().WarnContext(ctx, "IMAP connect failed",
			slog.Int("attempt", attempt+1),
			slog.String("error", clipError(last, fetcher.secrets()...)),
		)
	}

	return nil, fmt.Errorf("connect failed: %w", last)
}

func dialIMAP(ctx context.Context, cfg *Config, limits timeLimits) (*imapSession, error) {
	switch cfg.TLSMode {
	case tlsImplicit, tlsStart, tlsNone:
	default:
		return nil, fmt.Errorf("%w: %q", errTLSMode, cfg.TLSMode)
	}

	if cfg.TLSMode == tlsNone && !loopbackHost(cfg.Addr) {
		return nil, errPlainLoopback
	}

	limit := limits.commandFor()
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, limit)

	conn, err := dialTCP(attemptCtx, cfg.Addr, limit)
	if err != nil {
		cancelAttempt()

		return nil, finishStage(ctx, attemptCtx, stageConnect, err)
	}

	// Parent cancellation and the attempt deadline both close the socket.
	// The library's read timeout is cleared while it is idle, so a stall
	// after accept would otherwise block until that timer is set again.
	stopParent := context.AfterFunc(ctx, func() { closeNet(ctx, conn) })
	stopAttempt := context.AfterFunc(attemptCtx, func() { closeNet(attemptCtx, conn) })
	fail := func(stageErr error) (*imapSession, error) {
		stopAttempt()
		stopParent()
		cancelAttempt()
		closeNet(ctx, conn)

		return nil, stageErr
	}

	client, err := openClient(ctx, attemptCtx, conn, cfg)
	if err != nil {
		return fail(err)
	}

	err = loginClient(ctx, attemptCtx, client, cfg)
	if err != nil {
		return fail(err)
	}

	if ctx.Err() != nil {
		return fail(contextError(ctx.Err()))
	}

	if !stopAttempt() {
		return fail(redactedError("timeout during " + stageConnect))
	}

	cancelAttempt()

	session := &imapSession{
		raw:      client,
		conn:     conn,
		stop:     stopParent,
		limits:   limits,
		maxBytes: cfg.MaxMessageBytes,
	}

	return session, nil
}

func dialTCP(ctx context.Context, addr string, limit time.Duration) (net.Conn, error) {
	dialer := new(net.Dialer)
	dialer.Timeout = limit

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial IMAP: %w", err)
	}

	return conn, nil
}

func openClient(
	parent, attempt context.Context,
	conn net.Conn,
	cfg *Config,
) (*imapclient.Client, error) {
	switch cfg.TLSMode {
	case tlsNone:
		return greet(parent, attempt, imapclient.New(conn, nil))
	case tlsImplicit:
		return openImplicitTLS(parent, attempt, conn, cfg)
	case tlsStart:
		return openStartTLS(parent, attempt, conn, cfg)
	default:
		return nil, fmt.Errorf("%w: %q", errTLSMode, cfg.TLSMode)
	}
}

func openImplicitTLS(
	parent, attempt context.Context,
	conn net.Conn,
	cfg *Config,
) (*imapclient.Client, error) {
	tlsConfig, err := connectionTLS(cfg)
	if err != nil {
		return nil, closeConn(conn, err)
	}

	tlsConn := tls.Client(conn, tlsConfig)

	err = tlsConn.HandshakeContext(attempt)
	if err != nil {
		return nil, closeConn(conn, finishStage(parent, attempt, stageTLS, err))
	}

	options := new(imapclient.Options)
	options.TLSConfig = tlsConfig

	return greet(parent, attempt, imapclient.New(tlsConn, options))
}

func openStartTLS(
	parent, attempt context.Context,
	conn net.Conn,
	cfg *Config,
) (*imapclient.Client, error) {
	tlsConfig, err := connectionTLS(cfg)
	if err != nil {
		return nil, closeConn(conn, err)
	}

	options := new(imapclient.Options)
	options.TLSConfig = tlsConfig

	client, err := imapclient.NewStartTLS(conn, options)
	if err != nil {
		return nil, finishStage(parent, attempt, stageStartTLS, err)
	}

	return client, nil
}

func greet(parent, attempt context.Context, client *imapclient.Client) (*imapclient.Client, error) {
	err := finishStage(parent, attempt, stageGreeting, client.WaitGreeting())
	if err != nil {
		return nil, closeClient(client, err)
	}

	return client, nil
}

func loginClient(parent, attempt context.Context, client *imapclient.Client, cfg *Config) error {
	err := client.Login(cfg.Username, cfg.Password).Wait()

	err = finishStage(parent, attempt, stageLogin, err)
	if err != nil {
		return closeClient(client, err)
	}

	return nil
}

func closeConn(conn net.Conn, err error) error {
	closeErr := conn.Close()

	if err != nil {
		return err
	}

	if closeErr != nil && !benignClose(closeErr) {
		return redactedError(textConnectionClosed)
	}

	return nil
}

func closeClient(client *imapclient.Client, err error) error {
	closeErr := client.Close()

	if err != nil {
		return err
	}

	if closeErr != nil && !benignClose(closeErr) {
		return redactedError(textConnectionClosed)
	}

	return nil
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
