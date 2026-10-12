// Command server serves the Space API or runs database migrations.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/core"
	"github.com/orpheus-agents/orpheus-space/internal/httpserver"
	"github.com/orpheus-agents/orpheus-space/internal/migrate"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/worker"
)

func main() { os.Exit(mainCode()) }

func mainCode() int {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		logCommandFailure(err)
		return 1
	}
	return 0
}
func run(ctx context.Context, args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "healthcheck":
		host := os.Getenv("ORPHEUS_SYSTEM_HOST")
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		port := os.Getenv("ORPHEUS_SYSTEM_PORT")
		if port == "" {
			port = "9100"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/ready", nil)
		if err != nil {
			return err
		}
		client := http.Client{Timeout: 3 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != 200 {
			return errors.New("service is not ready")
		}
		return nil
	case "migrate":
		dir := os.Getenv("ORPHEUS_MIGRATIONS_DIR")
		if dir == "" {
			dir = "migrations"
		}
		sub := "up"
		if len(args) > 1 {
			sub = args[1]
		}
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return errors.New("DATABASE_URL is required")
		}
		return migrate.Run(ctx, dsn, sub, dir)
	case "worker":
		cfg, err := config.LoadWorker()
		if err != nil {
			return fail("configuration", err.Error(), err)
		}
		return runWorker(ctx, cfg)
	case "serve":
		if len(args) > 1 {
			return errors.New("unexpected serve arguments")
		}
		cfg, err := config.Load()
		if err != nil {
			return fail("configuration", err.Error(), err)
		}
		return serve(ctx, cfg)
	default:
		return errors.New("use serve, worker, migrate or healthcheck")
	}
}
func serve(ctx context.Context, cfg config.Config) error {
	poolConfig, err := config.DatabasePoolConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	migrationDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = migrationDB.Close() }()
	provider, err := migrate.Provider(migrationDB, cfg.MigrationsDir)
	if err != nil {
		return err
	}
	var coreClient httpserver.CoreReader
	if cfg.CoreURL != "" {
		client, err := core.New(cfg.CoreURL, cfg.CoreAPIKey)
		if err != nil {
			return err
		}
		coreClient = client
	}
	storage := &store.Store{Pool: pool, DefaultProfile: cfg.Execution.Agent.Profile, DefaultTemplate: cfg.Execution.Sandbox.Template, Catalog: coreClient}
	handler, err := httpserver.Handler(&httpserver.Server{Store: storage, Config: cfg, Core: coreClient})
	if err != nil {
		return err
	}
	probes := http.NewServeMux()
	probes.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	probes.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		bounded, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if ctx.Err() != nil || migrate.Ready(bounded, provider) != nil {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"status":"unavailable"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	apiListener, err := net.Listen("tcp", cfg.APIAddress)
	if err != nil {
		return err
	}
	defer func() { _ = apiListener.Close() }()
	systemListener, err := net.Listen("tcp", cfg.SystemAddress)
	if err != nil {
		return err
	}
	defer func() { _ = systemListener.Close() }()
	servers := []*http.Server{{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}, {Handler: probes, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}}
	failures := make(chan error, 2)
	go func() { failures <- servers[0].Serve(apiListener) }()
	go func() { failures <- servers[1].Serve(systemListener) }()
	slog.Info("Space API started", "address", cfg.APIAddress, "auth_mode", cfg.Auth.Mode)
	var cause error
	select {
	case <-ctx.Done():
	case cause = <-failures:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			if cause == nil {
				cause = err
			}
		}
	}
	if errors.Is(cause, http.ErrServerClosed) {
		return nil
	}
	return cause
}

func runWorker(ctx context.Context, cfg config.Config) (result error) {
	defer func() {
		if ctx.Err() != nil && (errors.Is(result, ctx.Err()) || errors.Is(result, context.Cause(ctx))) {
			result = nil
		}
	}()
	pc, err := config.DatabasePoolConfig(cfg.DatabaseURL)
	if err != nil {
		return fail("database", "invalid database configuration", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return fail("database", "cannot initialize database pool", err)
	}
	defer pool.Close()
	database := stdlib.OpenDBFromPool(pool)
	defer func() { _ = database.Close() }()
	provider, err := migrate.Provider(database, cfg.MigrationsDir)
	if err != nil {
		return fail("migrations", "cannot initialize migrations", err)
	}
	if err = migrate.Ready(ctx, provider); err != nil {
		return fail("migrations", "cannot check database migrations", err)
	}
	client, err := core.New(cfg.CoreURL, cfg.CoreAPIKey)
	if err != nil {
		return fail("core_client", "cannot initialize core client", err)
	}
	w := &worker.Worker{Store: &store.Store{Pool: pool}, Core: client, Build: worker.Builder(cfg), Poll: cfg.WorkerPoll}
	workerCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	listener, err := net.Listen("tcp", cfg.SystemAddress)
	if err != nil {
		return fail("probes", "cannot listen for worker probes", err)
	}
	// Publish leadership only after acquisition; probes also check the physical
	// connection so readiness drops immediately on leadership loss.
	var active atomic.Pointer[worker.Lease]
	probes := http.NewServeMux()
	probes.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	probes.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		bounded, stop := context.WithTimeout(r.Context(), 2*time.Second)
		defer stop()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		lease := active.Load()
		if workerCtx.Err() != nil || lease == nil || lease.Check(bounded) != nil || migrate.Ready(bounded, provider) != nil {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"status":"unavailable"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	system := &http.Server{Handler: probes, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		cancel(fail("probes", "worker probe server stopped", system.Serve(listener)))
	}()
	defer func() {
		cancel(nil)
		_ = system.Close()
		<-serverDone
	}()
	lease, err := worker.Acquire(workerCtx, pc.ConnConfig)
	if err != nil {
		if workerCtx.Err() != nil {
			return context.Cause(workerCtx)
		}
		return fail("leadership", "cannot acquire worker lock", err)
	}
	defer lease.Close()
	active.Store(lease)
	defer active.Store(nil)
	err = w.Run(workerCtx, lease)
	if workerCtx.Err() != nil {
		return context.Cause(workerCtx)
	}
	if err != nil {
		return fail("scheduler", "worker execution failed", err)
	}
	return nil
}
