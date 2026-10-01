//go:build integration

package store

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

type queryCounter struct{ count atomic.Int32 }

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.count.Add(1)
	return ctx
}
func (*queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestListBatchesLatestOccurrences(t *testing.T) {
	s, in := fixture(t)
	counter := &queryCounter{}
	cfg := s.Pool.Config()
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s.Pool = pool
	q := db.New(pool)
	expected := make(map[uuid.UUID]uuid.UUID)
	for i := range 200 {
		task, err := s.Create(t.Context(), in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			continue
		}
		// Insert newer first so selection cannot depend on insertion order.
		for j := 2; j >= 1; j-- {
			at := s.now().Add(time.Duration(j) * time.Minute)
			id := uuid.New()
			_, err := q.InsertOccurrence(t.Context(), db.InsertOccurrenceParams{ID: id, ScheduleID: task.ID, ScheduledAt: at, State: "skipped", CreatedAt: s.now(), CompletedAt: new(at), RequestKey: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			if j == 2 {
				expected[task.ID] = id
			}
		}
	}
	counter.count.Store(0)
	page, err := s.List(t.Context(), Filter{}, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	if count := counter.count.Load(); count > 3 {
		t.Fatalf("list made %d queries; expected at most 3", count)
	}
	if len(page.Items) != 200 || page.NextCursor != nil {
		t.Fatal("unexpected page size/cursor", len(page.Items), page.NextCursor)
	}
	for _, task := range page.Items {
		id, ok := expected[task.ID]
		if !ok {
			if task.LastOccurrence != nil {
				t.Fatal("unexpected occurrence", task.LastOccurrence)
			}
		} else if task.LastOccurrence == nil || task.LastOccurrence.ID != id {
			t.Fatal("wrong latest occurrence", task.ID, task.LastOccurrence)
		}
	}
}
