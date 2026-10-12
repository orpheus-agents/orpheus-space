package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/worker"
)

// Only reason is safe to log. The wrapped error may contain credentials,
// database values or upstream response text.
type commandFailure struct {
	phase, reason string
	cause         error
}

func (e *commandFailure) Error() string { return e.reason }
func (e *commandFailure) Unwrap() error { return e.cause }
func fail(phase, reason string, err error) error {
	return &commandFailure{phase: phase, reason: reason, cause: err}
}

func logCommandFailure(err error) {
	phase, reason := "command", "command execution failed"
	if failure, ok := errors.AsType[*commandFailure](err); ok {
		phase, reason = failure.phase, failure.reason
	}
	switch {
	case errors.Is(err, migrate.ErrPending):
		reason = "database migrations are pending"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "operation timed out"
	case errors.Is(err, context.Canceled):
		reason = "operation canceled"
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = "connection refused"
	case errors.Is(err, syscall.EADDRINUSE):
		reason = "listen address already in use"
	default:
		if network, ok := errors.AsType[net.Error](err); ok && network.Timeout() {
			reason = "network operation timed out"
		}
	}
	if leadership, ok := errors.AsType[*worker.LeadershipError](err); ok {
		phase, reason = "leadership", leadership.Error()
	}
	cause := err
	for errors.Unwrap(cause) != nil {
		cause = errors.Unwrap(cause)
	}
	attrs := []any{"phase", phase, "reason", reason, "error_type", fmt.Sprintf("%T", cause)}
	if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
		attrs = append(attrs, "sqlstate", pg.Code)
	}
	slog.Error("Command failed", attrs...)
}
