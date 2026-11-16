//go:build integration

package store

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

func TestManualRunReplayAndSchedulePreservation(t *testing.T) {
	for _, status := range []string{"active", "paused"} {
		t.Run(status, func(t *testing.T) {
			s, in := fixture(t)
			in.Status = status
			actor := access.Principal{ManageAll: true}
			task, err := s.Create(t.Context(), actor, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			q := db.New(s.Pool)
			before, err := q.GetSchedule(t.Context(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			key := uuid.New()
			first, err := s.RunSchedule(t.Context(), actor, task.ID, key)
			if err != nil || first.State != "pending" || first.Trigger != "manual" || !first.ScheduledAt.Equal(s.now()) {
				t.Fatal(first, err)
			}
			after, err := q.GetSchedule(t.Context(), task.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("manual run changed schedule", after, err)
			}
			replay, err := s.RunSchedule(t.Context(), actor, task.ID, key)
			if err != nil || !reflect.DeepEqual(first, replay) {
				t.Fatal(replay, err)
			}
			_, err = s.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			requireStatus(t, err, 409)
			_, err = s.ResetSession(t.Context(), actor, task.ID)
			requireStatus(t, err, 409)
			stored, err := q.GetOccurrence(t.Context(), db.GetOccurrenceParams{ScheduleID: task.ID, ID: first.ID})
			if err != nil || stored.RequestKey == key || stored.ManualKey == nil || *stored.ManualKey != key {
				t.Fatal(stored, err)
			}
			if err = q.FinishDispatch(t.Context(), db.FinishDispatchParams{ID: first.ID, State: "failed", CompletedAt: new(s.now())}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Update(t.Context(), actor, task.ID, []byte(`{"prompt":"Updated prompt"}`)); err != nil {
				t.Fatal(err)
			}
			replay, err = s.RunSchedule(t.Context(), actor, task.ID, key)
			if err != nil || replay.ID != first.ID || replay.State != "failed" {
				t.Fatal(replay, err)
			}
			// Separate commands may share a timestamp. Cron uniqueness does not apply to them.
			second, err := s.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			if err != nil || second.ID == first.ID {
				t.Fatal(second, err)
			}
			if err = s.Delete(t.Context(), actor, task.ID); err != nil {
				t.Fatal(err)
			}
			replay, err = s.RunSchedule(t.Context(), actor, task.ID, key)
			if err != nil || replay.ID != first.ID {
				t.Fatal(replay, err)
			}
			_, err = s.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			requireStatus(t, err, 409)
			history, err := s.History(t.Context(), task.ID, 50, "")
			if err != nil || len(history.Items) != 2 {
				t.Fatal(history, err)
			}
		})
	}
}

func TestManualRunPermissionsAndKeyScope(t *testing.T) {
	s, in := fixture(t)
	admin := access.Principal{ManageAll: true}
	alice := access.Principal{Email: new("alice@example.com")}
	bob := access.Principal{Email: new("bob@example.com")}
	in.OwnerEmail = alice.Email
	task, err := s.Create(t.Context(), admin, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.New()
	for _, actor := range []access.Principal{{}, bob} {
		_, err := s.RunSchedule(t.Context(), actor, task.ID, key)
		requireStatus(t, err, 403)
	}
	first, err := s.RunSchedule(t.Context(), alice, task.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(t.Context(), admin, task.ID, []byte(`{"owner_email":"bob@example.com"}`)); err != nil {
		t.Fatal(err)
	}
	_, err = s.RunSchedule(t.Context(), alice, task.ID, key)
	requireStatus(t, err, 403)
	replayed, err := s.RunSchedule(t.Context(), bob, task.ID, key)
	if err != nil || replayed.ID != first.ID {
		t.Fatal(replayed, err)
	}
	in.OwnerEmail = nil
	other, err := s.Create(t.Context(), admin, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RunSchedule(t.Context(), alice, other.ID, key)
	requireStatus(t, err, 403)
	second, err := s.RunSchedule(t.Context(), admin, other.ID, key)
	if err != nil || second.ID == first.ID {
		t.Fatal(second, err)
	}
	q := db.New(s.Pool)
	a, _ := q.GetOccurrence(t.Context(), db.GetOccurrenceParams{ScheduleID: task.ID, ID: first.ID})
	b, _ := q.GetOccurrence(t.Context(), db.GetOccurrenceParams{ScheduleID: other.ID, ID: second.ID})
	if a.RequestKey == b.RequestKey {
		t.Fatal("core request keys crossed schedules")
	}
	_, err = s.RunSchedule(t.Context(), admin, task.ID, uuid.Nil)
	requireStatus(t, err, 422)
	_, err = s.RunSchedule(t.Context(), admin, uuid.New(), key)
	requireStatus(t, err, 404)
}

func TestManualRunConcurrentCommands(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{true: "same key", false: "different keys"}[sameKey], func(t *testing.T) {
			s, in := fixture(t)
			actor := access.Principal{ManageAll: true}
			task, err := s.Create(t.Context(), actor, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errorsOut := make(chan error, 12)
			ids := make(chan uuid.UUID, 12)
			key := uuid.New()
			for range 12 {
				wg.Go(func() {
					requestKey := key
					if !sameKey {
						requestKey = uuid.New()
					}
					occ, err := s.RunSchedule(t.Context(), actor, task.ID, requestKey)
					if err != nil {
						errorsOut <- err
					} else {
						ids <- occ.ID
					}
				})
			}
			wg.Wait()
			close(errorsOut)
			close(ids)
			for err := range errorsOut {
				requireStatus(t, err, 409)
			}
			want := 1
			if sameKey {
				want = 12
			}
			if len(ids) != want {
				t.Fatalf("got %d accepted responses, want %d", len(ids), want)
			}
			id := <-ids
			for other := range ids {
				if other != id {
					t.Fatal("replay created a different occurrence")
				}
			}
			page, err := s.History(t.Context(), task.ID, 50, "")
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
		})
	}
}

func TestManualRunRacesWithCron(t *testing.T) {
	s, in := fixture(t)
	actor := access.Principal{ManageAll: true}
	for range 12 {
		task, err := s.Create(t.Context(), actor, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Now = func() time.Time { return *task.NextRunAt }
		var wg sync.WaitGroup
		var planErr, runErr error
		wg.Go(func() { planErr = s.Plan(t.Context(), task.ID, s.now()) })
		wg.Go(func() { _, runErr = s.RunSchedule(t.Context(), actor, task.ID, uuid.New()) })
		wg.Wait()
		if planErr != nil {
			t.Fatal(planErr)
		}
		if runErr != nil {
			requireStatus(t, runErr, 409)
		}
		page, err := s.History(t.Context(), task.ID, 50, "")
		if err != nil || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
	}
}

func TestManualPendingPauseDeleteAndPrepare(t *testing.T) {
	for _, action := range []string{"pause", "delete", "edit"} {
		t.Run(action, func(t *testing.T) {
			s, in := fixture(t)
			actor := access.Principal{ManageAll: true}
			task, err := s.Create(t.Context(), actor, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			occ, err := s.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "delete":
				err = s.Delete(t.Context(), actor, task.ID)
			case "pause":
				_, err = s.Update(t.Context(), actor, task.ID, []byte(`{"status":"paused"}`))
			case "edit":
				_, err = s.Update(t.Context(), actor, task.ID, []byte(`{"prompt":"Edited before dispatch"}`))
			}
			if err != nil {
				t.Fatal(err)
			}
			built := false
			got, err := s.Prepare(t.Context(), task.ID, occ.ID, s.now(), func(row db.Schedule, _ db.ScheduleOccurrence, _ *db.ScheduleOccurrence, _ time.Time) (Snapshot, error) {
				built = true
				if action == "edit" && row.Prompt != "Edited before dispatch" {
					t.Fatal(row.Prompt)
				}
				return Snapshot{Path: "/api/v1/sessions", Body: []byte(`{}`)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "dispatching"
			if action == "delete" {
				want = "cancelled"
			}
			if got.State != want || built != (action != "delete") {
				t.Fatal(got.State, built)
			}
		})
	}
}

func TestManualReplayRechecksOwnerUnderLock(t *testing.T) {
	s, in := fixture(t)
	admin := access.Principal{ManageAll: true}
	alice := access.Principal{Email: new("alice@example.com")}
	in.OwnerEmail = alice.Email
	task, err := s.Create(t.Context(), admin, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.New()
	if _, err = s.RunSchedule(t.Context(), alice, task.ID, key); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "UPDATE schedules SET owner_email='bob@example.com' WHERE id=$1", task.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := s.RunSchedule(t.Context(), alice, task.ID, key); result <- err }()
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	err = <-result
	if problem, ok := errors.AsType[*schedule.Error](err); !ok || problem.Problem.Code != "schedule_forbidden" {
		t.Fatal(err)
	}
}
