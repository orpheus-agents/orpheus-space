package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const lockID int64 = 4857668146153

// Lease uses a dedicated physical connection. Never return a connection still
// holding a session advisory lock to the pool.
type Lease struct {
	conn *pgx.Conn
	mu   sync.Mutex
}

func Acquire(ctx context.Context, cfg *pgx.ConnConfig) (*Lease, error) {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked)
	if err != nil || !locked {
		_ = conn.Close(context.Background())
		if err == nil {
			err = errors.New("another Space worker owns the lock")
		}
		return nil, err
	}
	return &Lease{conn: conn}, nil
}
func (l *Lease) Close() { l.mu.Lock(); defer l.mu.Unlock(); _ = l.conn.Close(context.Background()) }
func (l *Lease) Check(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return l.conn.Ping(bounded)
}
func (w *Worker) Run(ctx context.Context, lease *Lease) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.Check = lease.Check
	poll := w.Poll
	if poll <= 0 {
		poll = time.Second
	}
	monitor := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				monitor <- ctx.Err()
				return
			case <-time.After(time.Second):
			}
			if err := lease.Check(ctx); err != nil {
				cancel()
				monitor <- err
				return
			}
		}
	}()
	defer func() { cancel(); <-monitor }()
	for {
		if err := w.Tick(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
