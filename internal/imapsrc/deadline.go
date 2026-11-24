package imapsrc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
)

const (
	commandTimeout = 30 * time.Second
	minBodyTimeout = 2 * time.Minute
	maxBodyTimeout = 15 * time.Minute
	// Throughput assumed when sizing a body-read deadline.
	bodyRate = 1 << 20

	maxHeaderBytes = 256 << 10

	stageConnect  = "connect"
	stageGreeting = "greeting"
	stageStartTLS = "STARTTLS"
	stageTLS      = "TLS handshake"
	stageLogin    = "login"
	stageExamine  = "EXAMINE"
	stageSearch   = "UID SEARCH"
	stageFetch    = "UID FETCH"
	stageLogout   = "logout"

	textConnectionClosed = "connection closed"
)

// timeLimits overrides the production deadlines. Zero values use the defaults
// so tests can inject a short bound without changing the exported API.
type timeLimits struct {
	command time.Duration
	body    time.Duration
}

func (limits timeLimits) commandFor() time.Duration {
	if limits.command > 0 {
		return limits.command
	}

	return commandTimeout
}

func (limits timeLimits) bodyFor(maxBytes int64) time.Duration {
	if limits.body > 0 {
		return limits.body
	}

	return bodyFetchTimeout(maxBytes)
}

// bodyFetchTimeout is 2 minutes plus the time MaxMessageBytes would take at
// 1 MB/s, and never longer than 15 minutes.
func bodyFetchTimeout(maxBytes int64) time.Duration {
	if maxBytes < 0 {
		maxBytes = 0
	}

	seconds := maxBytes / bodyRate
	if seconds > int64(maxBodyTimeout/time.Second) {
		return maxBodyTimeout
	}

	timeout := minBodyTimeout + time.Duration(seconds)*time.Second
	if timeout > maxBodyTimeout || timeout < minBodyTimeout {
		return maxBodyTimeout
	}

	return timeout
}

func benignClose(err error) bool {
	return err == nil ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe)
}

func closeNet(ctx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}

	closeErr := conn.Close()
	if closeErr != nil && !benignClose(closeErr) {
		slog.DebugContext(ctx, "IMAP connection close failed")
	}
}

// runStage runs fn and bounds it. When ctx is canceled the connection is
// closed so a stalled read returns promptly. The stage deadline does the
// same, independently of the library's own read timers, which it clears
// between commands.
func runStage(
	ctx context.Context,
	conn net.Conn,
	limit time.Duration,
	stage, mailbox string,
	fn func() error,
) error {
	err := ctx.Err()
	if err != nil {
		return contextError(err)
	}

	if limit <= 0 {
		limit = commandTimeout
	}

	opCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	if conn != nil {
		deadlineErr := conn.SetDeadline(time.Now().Add(limit))
		if deadlineErr != nil && !benignClose(deadlineErr) {
			return classifyServer(stage, mailbox, deadlineErr)
		}

		stop := context.AfterFunc(opCtx, func() { closeNet(opCtx, conn) })
		defer stop()
	}

	err = fn()

	if ctx.Err() != nil {
		return contextError(ctx.Err())
	}

	if opCtx.Err() != nil || isTimeout(err) {
		return redactedError("timeout during " + stage)
	}

	if err == nil {
		return nil
	}

	if isLocal(err) {
		return err
	}

	return classifyServer(stage, mailbox, err)
}

// finishStage classifies the result of a dial step that is already covered
// by the attempt-wide deadline.
func finishStage(parent, attempt context.Context, stage string, err error) error {
	if parent.Err() != nil {
		return contextError(parent.Err())
	}

	if attempt.Err() != nil || isTimeout(err) {
		return redactedError("timeout during " + stage)
	}

	if err == nil {
		return nil
	}

	if isLocal(err) {
		return err
	}

	return classifyServer(stage, "", err)
}

func isLocal(err error) bool {
	return errors.Is(err, errBodyMissing) ||
		errors.Is(err, errUIDMissing) ||
		errors.Is(err, errFetchSection) ||
		errors.Is(err, errTLSMode) ||
		errors.Is(err, errPlainLoopback) ||
		errors.Is(err, errCAParse)
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return false
}

func isClosed(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

func classifyServer(stage, mailbox string, err error) error {
	if isClosed(err) {
		return redactedError(textConnectionClosed)
	}

	status, ok := errors.AsType[*imap.Error](err)
	if ok {
		return statusError(stage, mailbox, status)
	}

	switch stage {
	case stageConnect, stageGreeting:
		return redactedError("connect failed")
	case stageTLS, stageStartTLS:
		return redactedError("TLS handshake failed")
	case stageLogin:
		return redactedError("login rejected")
	default:
		return redactedError("command failed: " + stage)
	}
}

func statusError(stage, mailbox string, status *imap.Error) error {
	switch stage {
	case stageLogin:
		return redactedError("login rejected")
	case stageGreeting, stageConnect:
		return redactedError("connect failed")
	case stageExamine:
		if status.Code == imap.ResponseCodeNonExistent ||
			status.Code == imap.ResponseCodeTryCreate {
			return redactedError("mailbox not found: " + mailbox)
		}
	case stageTLS, stageStartTLS:
		if status.Code == "" {
			return redactedError("TLS handshake failed")
		}
	default:
	}

	if status.Code != "" {
		return redactedError("command failed: " + stage + " " + string(status.Code))
	}

	return redactedError("command failed: " + stage)
}
