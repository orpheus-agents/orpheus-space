package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"

	"github.com/jackc/pgx/v5"
)

const lockID int64 = 4857668146153

// Lease uses a dedicated physical connection. Never return a connection still
// holding a session advisory lock to the pool.
type Lease struct {
	conn *pgx.Conn
	mu   sync.Mutex
}

// Acquire waits for the previous worker to release leadership. Only contention is
// retried; connection/query failures return immediately. Cancellation closes the
// dedicated connection, including when it races with acquiring the lock.
func Acquire(ctx context.Context, cfg *pgx.ConnConfig) (*Lease, error) {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	waiting := false
	for {
		var locked bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked); err != nil {
			_ = conn.Close(context.Background())
			return nil, err
		}
		if locked {
			if err := ctx.Err(); err != nil {
				_ = conn.Close(context.Background())
				return nil, err
			}
			slog.InfoContext(ctx, "Space worker lock acquired", "phase", "leadership", "reason", "lock_acquired")
			return &Lease{conn: conn}, nil
		}
		if !waiting {
			slog.InfoContext(ctx, "Waiting for Space worker lock", "phase", "leadership", "reason", "lock_busy")
			waiting = true
		}
		select {
		case <-ctx.Done():
			_ = conn.Close(context.Background())
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (l *Lease) Close() { l.mu.Lock(); defer l.mu.Unlock(); _ = l.conn.Close(context.Background()) }

// LeadershipError identifies failure of the physical connection holding the lock.
// Its message is safe for logs; the wrapped database/network error may not be.
type LeadershipError struct{ Cause error }

func (e *LeadershipError) Error() string { return "worker leadership connection lost" }
func (e *LeadershipError) Unwrap() error { return e.Cause }

func (l *Lease) Check(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := l.conn.Ping(bounded); err != nil {
		return &LeadershipError{Cause: err}
	}
	return nil
}
func (w *Worker) Run(ctx context.Context, lease *Lease) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	w.Check = lease.Check
	cleanupDone := make(chan struct{})
	go func() { defer close(cleanupDone); store.CleanupBrowserAuth(ctx, db.New(w.Store.Pool)) }()
	defer func() { cancel(nil); <-cleanupDone }()
	poll := w.Poll
	if poll <= 0 {
		poll = time.Second
	}
	monitor := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				monitor <- context.Cause(ctx)
				return
			case <-time.After(time.Second):
			}
			if err := lease.Check(ctx); err != nil {
				cancel(err)
				monitor <- err
				return
			}
		}
	}()
	defer func() { cancel(nil); <-monitor }()
	for {
		if err := w.Tick(ctx); err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(poll):
		}
	}
}
