//go:build integration

package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
	"github.com/orpheus-agents/orpheus-space/internal/worker"
)

func workerFixture(t *testing.T) (config.Config, *pgxpool.Pool, *atomic.Int32) {
	t.Helper()
	pool := workerDatabase(t)
	requests := new(atomic.Int32)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(503)
	}))
	t.Cleanup(core.Close)
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	u.Path = "/" + pool.Config().ConnConfig.Database
	q.Del("search_path")
	q.Set("application_name", "worker-test-"+uuid.NewString())
	u.RawQuery = q.Encode()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cfg := config.Config{DatabaseURL: u.String(), CoreURL: core.URL, CoreAPIKey: "test-key", SystemAddress: address, MigrationsDir: "../../migrations", WorkerPoll: 20 * time.Millisecond}
	cfg.Execution.Agent.Profile = "default"
	cfg.Execution.Sandbox.Template = "sandbox"
	cfg.Execution.Limits.RunTimeoutSeconds = 3600
	s := &store.Store{Pool: pool, DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}, Now: func() time.Time { return time.Now().Add(-2 * time.Minute) }}
	input := schedule.Defaults()
	input.Name, input.Prompt, input.Cron, input.Timezone = "due task", "work", "* * * * *", "UTC"
	if _, err := s.Create(t.Context(), access.Principal{ManageAll: true}, input, nil); err != nil {
		t.Fatal(err)
	}
	return cfg, pool, requests
}

func startWorker(t *testing.T, cfg config.Config) (context.CancelCauseFunc, <-chan error, <-chan struct{}) {
	t.Helper()
	previous := slog.Default()
	handler := &lockWaitHandler{Handler: slog.DiscardHandler, busy: make(chan struct{})}
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { defer close(exited); done <- runWorker(ctx, cfg) }()
	t.Cleanup(func() {
		cancel(nil)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	return cancel, done, handler.busy
}

func probe(t *testing.T, address, path string, status int) {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get("http://" + address + path)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == status {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not return %d", path, status)
}

func TestWorkerWaitsForLockBeforeDispatch(t *testing.T) {
	cfg, pool, requests := workerFixture(t)
	owner, err := worker.Acquire(t.Context(), pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	cancel, done, busy := startWorker(t, cfg)
	probe(t, cfg.SystemAddress, "/health", 200)
	probe(t, cfg.SystemAddress, "/ready", 503)
	waitingBackend(t, cfg, pool, busy)
	// Cover more than one acquisition attempt while a due schedule is present.
	time.Sleep(1200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("worker exited while waiting: %v", err)
	default:
	}
	probe(t, cfg.SystemAddress, "/health", 200)
	probe(t, cfg.SystemAddress, "/ready", 503)
	var planned int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM schedule_occurrences").Scan(&planned); err != nil {
		t.Fatal(err)
	}
	if planned != 0 || requests.Load() != 0 {
		t.Fatalf("work without leadership: planned=%d requests=%d", planned, requests.Load())
	}
	owner.Close()
	probe(t, cfg.SystemAddress, "/ready", 200)
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() == 0 {
		t.Fatal("worker did not dispatch after leadership transfer")
	}
	cancel(errors.New("received SIGTERM"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	// Successful shutdown must release leadership for a subsequent worker.
	ctx, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	next, err := worker.Acquire(ctx, pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestWorkerCancellationWhileWaiting(t *testing.T) {
	cfg, pool, requests := workerFixture(t)
	owner, err := worker.Acquire(t.Context(), pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	cancel, done, busy := startWorker(t, cfg)
	probe(t, cfg.SystemAddress, "/health", 200)
	probe(t, cfg.SystemAddress, "/ready", 503)
	waitingBackend(t, cfg, pool, busy)
	cancel(errors.New("received SIGTERM"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock wait ignored cancellation")
	}
	if requests.Load() != 0 {
		t.Fatal("dispatched while waiting")
	}
	if err := owner.Check(t.Context()); err != nil {
		t.Fatal("waiting worker disturbed leader", err)
	}
	u, _ := url.Parse(cfg.DatabaseURL)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var connections int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1", u.Query().Get("application_name")).Scan(&connections); err != nil {
			t.Fatal(err)
		}
		if connections == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaked %d worker connections", connections)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkerMigrationDiagnostic(t *testing.T) {
	cfg, pool, _ := workerFixture(t)
	if _, err := pool.Exec(t.Context(), "DELETE FROM goose_db_version WHERE version_id=(SELECT max(version_id) FROM goose_db_version)"); err != nil {
		t.Fatal(err)
	}
	err := runWorker(t.Context(), cfg)
	failure, ok := errors.AsType[*commandFailure](err)
	if !ok || failure.phase != "migrations" {
		t.Fatalf("missing migration diagnostic: %v", err)
	}
}

func TestWorkerDatabaseFailureIsNotRetried(t *testing.T) {
	cfg, _, _ := workerFixture(t)
	u, _ := url.Parse(cfg.DatabaseURL)
	u.Path = "/missing_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cfg.DatabaseURL = u.String()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := runWorker(ctx, cfg)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("database failure was retried or ignored: %v", err)
	}
}

// Leadership locks are database-wide. Give these process tests their own
// databases, rather than contending with the worker package's schema fixtures.
func workerDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(t.Context(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := "worker_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+quoted); err != nil {
			t.Error(err)
		}
	})
	cfg, err := config.DatabasePoolConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	database := stdlib.OpenDBFromPool(pool)
	defer func() { _ = database.Close() }()
	testutil.Migrate(t, database)
	return pool
}

// The log proves pg_try_advisory_lock returned false. Activity alone could
// observe an idle backend between preparing and executing the first query.
func waitingBackend(t *testing.T, cfg config.Config, pool *pgxpool.Pool, busy <-chan struct{}) int {
	t.Helper()
	select {
	case <-busy:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never entered the lock wait loop")
	}
	u, _ := url.Parse(cfg.DatabaseURL)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var pid int
		err := pool.QueryRow(t.Context(), "SELECT pid FROM pg_stat_activity WHERE application_name=$1 AND state='idle' AND query LIKE 'SELECT pg_try_advisory_lock%'", u.Query().Get("application_name")).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker never attempted the advisory lock")
	return 0
}

func TestWorkerLockConnectionFailureIsNotRetried(t *testing.T) {
	cfg, pool, requests := workerFixture(t)
	owner, err := worker.Acquire(t.Context(), pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	_, done, busy := startWorker(t, cfg)
	probe(t, cfg.SystemAddress, "/health", 200)
	pid := waitingBackend(t, cfg, pool, busy)
	if _, err := pool.Exec(t.Context(), "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		failure, ok := errors.AsType[*commandFailure](err)
		if !ok || failure.phase != "leadership" {
			t.Fatalf("lost lock connection was not diagnosed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("database failure during acquisition was retried")
	}
	if requests.Load() != 0 {
		t.Fatal("dispatched without leadership")
	}
	if err := owner.Check(t.Context()); err != nil {
		t.Fatal("waiting worker disturbed leader", err)
	}
}

// Tests are serial within this package. Restore the logger after worker cleanup.
type lockWaitHandler struct {
	slog.Handler
	busy chan struct{}
	once sync.Once
}

func (*lockWaitHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *lockWaitHandler) Handle(ctx context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "reason" && attr.Value.String() == "lock_busy" {
			h.once.Do(func() { close(h.busy) })
		}
		return true
	})
	return h.Handler.Handle(ctx, record)
}
