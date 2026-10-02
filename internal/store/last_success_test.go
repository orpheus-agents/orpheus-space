//go:build integration

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

func TestLastSuccessfulOccurrenceSelection(t *testing.T) {
	s, in := fixture(t)
	task, err := s.Create(t.Context(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(t.Context(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := s.now()
	q := db.New(s.Pool)
	args := db.LastSuccessfulOccurrenceParams{ScheduleID: task.ID, ScheduledAt: at.Add(20 * time.Minute)}
	if _, err := q.LastSuccessfulOccurrence(t.Context(), args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	var expected uuid.UUID
	for i, tc := range []struct {
		state, status string
		other, future bool
	}{
		{state: "accepted", status: "completed"},
		{state: "accepted", status: "completed"},
		{state: "accepted", status: "failed"},
		{state: "accepted", status: "cancelled"},
		{state: "failed"}, {state: "skipped"}, {state: "cancelled"},
		{state: "accepted", status: "completed", other: true},
		{state: "accepted", status: "completed", future: true},
	} {
		id := uuid.New()
		sid := task.ID
		if tc.other {
			sid = other.ID
		}
		scheduled := at.Add(time.Duration(i) * time.Minute)
		if tc.future {
			scheduled = args.ScheduledAt
		}
		_, err := s.Pool.Exec(t.Context(), `INSERT INTO schedule_occurrences
  (id,schedule_id,scheduled_at,state,created_at,updated_at,completed_at,request_key,request_path,request_body,session_id,run_id,run_status,finished_at)
  VALUES ($1,$2,$3,$4,$3,$3,$3,$1,'/api/v1/sessions','{}',$1,$1,$5,$3)`, id, sid, scheduled, tc.state, tc.status)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			expected = id
		}
	}
	got, err := q.LastSuccessfulOccurrence(t.Context(), args)
	if err != nil || got.ID != expected {
		t.Fatal(got, err)
	}
}
