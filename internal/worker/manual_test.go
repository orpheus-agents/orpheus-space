//go:build integration

package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

func TestManualRunsOnPauseUseCommonExecution(t *testing.T) {
	for _, mode := range []string{"new", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			w, f, now, task := fixture(t)
			actor := access.Principal{ManageAll: true}
			patch := `{"status":"paused","session_mode":"` + mode + `","profile":"other","template":"other","model":"custom","services":["a"],"prompt":"Manual report"}`
			if _, err := w.Store.Update(t.Context(), actor, task.ID, []byte(patch)); err != nil {
				t.Fatal(err)
			}
			first, err := w.Store.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			tick(t, w, f)
			if len(f.requests) != 1 {
				t.Fatal(f.requests)
			}
			var request coreapi.CreateSession
			if err = json.Unmarshal(f.requests[0].body, &request); err != nil {
				t.Fatal(err)
			}
			if request.Configuration.Agent.Profile != "other" || request.Configuration.Sandbox.Template != "other" || *request.Configuration.Agent.Model != "custom" || len(*request.Configuration.Sandbox.Services) != 1 || (*request.Configuration.Sandbox.Services)[0] != "a" || *request.AllowMultipleRuns != (mode == "reuse") || !strings.HasSuffix(request.Messages[0].Text, "Manual report") {
				t.Fatal(request)
			}
			if strings.Contains(request.Messages[0].Text, "trigger:") {
				t.Fatal("manual run changed message format")
			}
			complete(f, now.Add(time.Second))
			*now = now.Add(10 * time.Second)
			tick(t, w, f)
			_, err = w.Store.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			tick(t, w, f)
			if len(f.requests) != 2 || strings.HasSuffix(f.requests[1].path, "/runs") != (mode == "reuse") {
				t.Fatal(f.requests)
			}
			if !bytes.Contains(f.requests[1].body, []byte("last_successful_run")) {
				t.Fatal("manual success did not advance last_successful_run")
			}
			// A terminal agent error permits another explicit command and remains in history.
			for id, run := range f.runs {
				run.Status = coreapi.RunStatusFailed
				run.FinishedAt = new(now.Add(time.Second))
				f.runs[id] = run
			}
			*now = now.Add(10 * time.Second)
			tick(t, w, f)
			third, err := w.Store.RunSchedule(t.Context(), actor, task.ID, uuid.New())
			if err != nil || third.ID == first.ID {
				t.Fatal(third, err)
			}
			got, err := w.Store.Get(t.Context(), task.ID)
			if err != nil || got.Status != "paused" || got.NextRunAt != nil || got.Cron != task.Cron {
				t.Fatal(got, err)
			}
		})
	}
}

func TestManualRunSurvivesLostResponseAndBlocksAnotherCommand(t *testing.T) {
	w, f, now, task := fixture(t)
	actor := access.Principal{ManageAll: true}
	f.lose = true
	key := uuid.New()
	first, err := w.Store.RunSchedule(t.Context(), actor, task.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	tick(t, w, f)
	_, err = w.Store.RunSchedule(t.Context(), actor, task.ID, uuid.New())
	if problem, ok := errors.AsType[*schedule.Error](err); !ok || problem.Problem.Code != "schedule_busy" {
		t.Fatal(err)
	}
	replay, err := w.Store.RunSchedule(t.Context(), actor, task.ID, key)
	if err != nil || replay.ID != first.ID || replay.State != "dispatching" {
		t.Fatal(replay, err)
	}
	if _, err = w.Store.Update(t.Context(), actor, task.ID, []byte(`{"status":"paused","prompt":"New prompt"}`)); err != nil {
		t.Fatal(err)
	}
	replacement := *w
	w = &replacement
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	if len(f.accepted) != 1 || len(f.requests) != 2 || f.requests[0].key != f.requests[1].key || !bytes.Equal(f.requests[0].body, f.requests[1].body) {
		t.Fatal(f.requests)
	}
	f.offline = true
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	_, err = w.Store.RunSchedule(t.Context(), actor, task.ID, uuid.New())
	if problem, ok := errors.AsType[*schedule.Error](err); !ok || problem.Problem.Code != "schedule_busy" {
		t.Fatal(err)
	}
}

func TestManualRunSkipsOverlappingCronAtSameTimestamp(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = *task.NextRunAt
	if _, err := w.Store.RunSchedule(t.Context(), access.Principal{ManageAll: true}, task.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	tick(t, w, f)
	rows := history(t, w, task.ID)
	if len(rows) != 2 || len(f.requests) != 1 {
		t.Fatal(rows, f.requests)
	}
	for _, row := range rows {
		if !row.ScheduledAt.Equal(*now) || row.Trigger == "scheduled" && row.State != "skipped" || row.Trigger == "manual" && row.State != "accepted" {
			t.Fatal(row)
		}
	}
	_, err := w.Store.RunSchedule(t.Context(), access.Principal{ManageAll: true}, task.ID, uuid.New())
	if problem, ok := errors.AsType[*schedule.Error](err); !ok || problem.Problem.Code != "schedule_busy" {
		t.Fatal(err)
	}
	got, err := w.Store.Get(t.Context(), task.ID)
	if err != nil || !got.NextRunAt.Equal(now.Add(time.Minute)) {
		t.Fatal(got, err)
	}
}
