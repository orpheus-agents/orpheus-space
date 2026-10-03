//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

func TestOwnerChangesDuringCatalogValidation(t *testing.T) {
	s, in := fixture(t)
	in.OwnerEmail = new("alice@example.com")
	actor := access.Principal{Email: in.OwnerEmail}
	task, err := s.Create(t.Context(), actor, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &checkedCatalog{before: func() {
		if _, err := s.Pool.Exec(t.Context(), "UPDATE schedules SET owner_email='bob@example.com' WHERE id=$1", task.ID); err != nil {
			t.Fatal(err)
		}
	}}
	s.Catalog = catalog
	_, err = s.Update(t.Context(), actor, task.ID, []byte(`{"profile":"other"}`))
	requireStatus(t, err, 403)
	got, err := s.Get(t.Context(), task.ID)
	if err != nil || got.Profile != task.Profile || *got.OwnerEmail != "bob@example.com" {
		t.Fatal(got, err)
	}
	catalog.calls = 0
	_, err = s.Update(t.Context(), actor, task.ID, []byte(`{"profile":"other","owner_email":"alice@example.com"}`))
	requireStatus(t, err, 403)
	if catalog.calls != 0 {
		t.Fatal("unauthorized request reached core")
	}
}

func TestMutationsRecheckOwnerAfterLock(t *testing.T) {
	for _, operation := range []string{"update", "delete", "reset"} {
		t.Run(operation, func(t *testing.T) {
			s, in := fixture(t)
			in.OwnerEmail = new("alice@example.com")
			actor := access.Principal{Email: in.OwnerEmail}
			task, err := s.Create(t.Context(), actor, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := uuid.New()
			if _, err := s.Pool.Exec(t.Context(), `UPDATE schedules SET reusable_session_id=$2,reusable_fingerprint='saved' WHERE id=$1`, task.ID, sessionID); err != nil {
				t.Fatal(err)
			}
			q := db.New(s.Pool)
			occ, err := q.InsertOccurrence(t.Context(), db.InsertOccurrenceParams{ID: uuid.New(), ScheduleID: task.ID, ScheduledAt: s.now(), State: "pending", CreatedAt: s.now(), RequestKey: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(t.Context(), "UPDATE schedules SET owner_email='bob@example.com' WHERE id=$1", task.ID); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "update":
					_, err = s.Update(ctx, actor, task.ID, []byte(`{"status":"paused","model":"changed"}`))
				case "delete":
					err = s.Delete(ctx, actor, task.ID)
				case "reset":
					_, err = s.ResetSession(ctx, actor, task.ID)
				}
				done <- err
			}()
			// Wait for a real PostgreSQL lock wait, not a timing assumption.
			for {
				var waiting bool
				err := s.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", tx.Conn().PgConn().PID()).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("did not wait for owner lock: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			requireStatus(t, <-done, 403)
			row, err := q.GetSchedule(t.Context(), task.ID)
			if err != nil || row.DeletedAt != nil || row.Status != "active" || row.Model != nil || row.ReusableSessionID == nil || *row.ReusableSessionID != sessionID || *row.OwnerEmail != "bob@example.com" {
				t.Fatal(row, err)
			}
			stored, err := q.GetOccurrence(t.Context(), db.GetOccurrenceParams{ScheduleID: task.ID, ID: occ.ID})
			if err != nil || stored.State != "pending" || stored.CompletedAt != nil {
				t.Fatal(stored, err)
			}
		})
	}
}

func TestCreateChecksPermissionsBeforeReplayAndCatalog(t *testing.T) {
	s, in := fixture(t)
	in.OwnerEmail = new("alice@example.com")
	key := uuid.New()
	if _, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key); err != nil {
		t.Fatal(err)
	}
	catalog := &checkedCatalog{}
	s.Catalog = catalog
	for _, actor := range []access.Principal{{}, {Email: new("bob@example.com")}} {
		for _, key := range []*uuid.UUID{nil, &key} {
			_, err := s.Create(t.Context(), actor, in, key)
			requireStatus(t, err, 403)
		}
	}
	if catalog.calls != 0 {
		t.Fatal("forbidden create reached core")
	}
}
