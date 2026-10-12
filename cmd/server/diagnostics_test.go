package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/worker"
)

func TestCommandDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		err                     error
		phase, reason, sqlstate string
	}{
		{"configuration", fail("configuration", "DATABASE_URL is required", errors.New("secret")), "configuration", "DATABASE_URL is required", ""},
		{"pending migrations", fail("migrations", "cannot check migrations", fmt.Errorf("secret: %w", migrate.ErrPending)), "migrations", "database migrations are pending", ""},
		{"postgres", fail("leadership", "cannot acquire worker lock", &pgconn.PgError{Code: "42501", Message: "secret", Detail: "secret"}), "leadership", "cannot acquire worker lock", "42501"},
		{"refused", fail("leadership", "cannot acquire worker lock", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), "leadership", "connection refused", ""},
		{"timeout", fail("scheduler", "worker execution failed", context.DeadlineExceeded), "scheduler", "operation timed out", ""},
		{"bind", fail("probes", "cannot listen for worker probes", syscall.EADDRINUSE), "probes", "listen address already in use", ""},
		{"leadership lost", fail("scheduler", "worker execution failed", &worker.LeadershipError{Cause: errors.New("secret")}), "leadership", "worker leadership connection lost", ""},
		{"unknown", fmt.Errorf("postgres://user:secret@host/db: %w", errors.New("secret")), "command", "command execution failed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			defer slog.SetDefault(previous)
			logCommandFailure(tc.err)
			if strings.Contains(output.String(), "secret") {
				t.Fatal("diagnostic leaked a secret")
			}
			var row map[string]any
			if err := json.Unmarshal(output.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if row["phase"] != tc.phase || row["reason"] != tc.reason || row["error_type"] == nil {
				t.Fatal(row)
			}
			if tc.sqlstate != "" && row["sqlstate"] != tc.sqlstate {
				t.Fatal(row)
			}
		})
	}
}

func TestWorkerConfigurationDiagnostic(t *testing.T) {
	t.Setenv("ORPHEUS_BASE_URL", "http://core")
	t.Setenv("ORPHEUS_API_KEY", "")
	err := run(t.Context(), []string{"worker"})
	failure, ok := errors.AsType[*commandFailure](err)
	if !ok || failure.phase != "configuration" || !strings.Contains(failure.reason, "ORPHEUS_API_KEY") {
		t.Fatalf("missing configuration diagnostic: %v", err)
	}
}

func TestWorkerInvalidTOMLDoesNotLeakValues(t *testing.T) {
	t.Setenv("ORPHEUS_BASE_URL", "http://core")
	t.Setenv("ORPHEUS_API_KEY", "secret")
	t.Setenv("DATABASE_URL", "postgres://user:secret@localhost/db")
	t.Setenv("PUBLIC_API_KEYS", "[]")
	t.Setenv("ORPHEUS_BROWSER_AUTH", "api_only")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("secret = [invalid secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORPHEUS_CONFIG_FILE", path)
	err := run(t.Context(), []string{"worker"})
	failure, ok := errors.AsType[*commandFailure](err)
	if !ok || failure.phase != "configuration" || !strings.Contains(failure.reason, "TOML") || strings.Contains(failure.reason, "secret") {
		t.Fatalf("unsafe or missing TOML diagnostic: %v", err)
	}
}
