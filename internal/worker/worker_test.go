//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/core"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type submitted struct {
	path, key string
	body      []byte
}
type coreFixture struct {
	mu         sync.Mutex
	requests   []submitted
	accepted   map[string]coreapi.Accepted
	runs       map[uuid.UUID]coreapi.Run
	lose       bool
	postError  string
	postStatus int
	offline    bool
}

func (f *coreFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer core-key" {
		w.WriteHeader(401)
		return
	}
	if r.Method == "POST" {
		raw, _ := io.ReadAll(r.Body)
		key := r.Header.Get("Idempotency-Key")
		f.requests = append(f.requests, submitted{r.URL.Path, key, raw})
		if f.postError != "" {
			w.WriteHeader(f.postStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": f.postError}})
			return
		}
		accepted, exists := f.accepted[key]
		if !exists {
			sid := uuid.New()
			if strings.HasSuffix(r.URL.Path, "/runs") {
				sid, _ = uuid.Parse(strings.Split(r.URL.Path, "/")[4])
			}
			accepted = coreapi.Accepted{SessionID: sid, RunID: uuid.New(), MessageID: uuid.New()}
			f.accepted[key] = accepted
			f.runs[accepted.RunID] = coreapi.Run{ID: accepted.RunID, SessionID: sid, Status: coreapi.RunStatusRunning}
		}
		if f.lose {
			f.lose = false
			c, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = c.Close()
			}
			return
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(accepted)
		return
	}
	if f.offline {
		w.WriteHeader(503)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) == 5 {
		sid, _ := uuid.Parse(parts[4])
		session := coreapi.Session{ID: sid}
		for id, run := range f.runs {
			if run.SessionID == sid && !core.Terminal(run.Status) {
				session.ActiveRunID = new(id)
			}
		}
		_ = json.NewEncoder(w).Encode(session)
		return
	}
	if len(parts) == 7 {
		id, _ := uuid.Parse(parts[6])
		run, ok := f.runs[id]
		if !ok {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "run_not_found"}})
			return
		}
		_ = json.NewEncoder(w).Encode(run)
		return
	}
	w.WriteHeader(404)
}
func fixture(t *testing.T) (*Worker, *coreFixture, *time.Time, schedule.Schedule) {
	t.Helper()
	now := new(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	cfg := config.Config{AllowedEnv: []string{"A", "B"}}
	cfg.Execution.Agent.Profile = "default"
	cfg.Execution.Sandbox.Template = "sandbox"
	cfg.Execution.Sandbox.EnvFrom = []string{"A"}
	cfg.Execution.Limits.RunTimeoutSeconds = 3600
	storage := &store.Store{Pool: testutil.Database(t), AllowedEnv: cfg.AllowedEnv, Now: func() time.Time { return *now }}
	f := &coreFixture{accepted: map[string]coreapi.Accepted{}, runs: map[uuid.UUID]coreapi.Run{}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	client, err := core.New(server.URL, "core-key")
	if err != nil {
		t.Fatal(err)
	}
	in := schedule.Defaults()
	in.Name = "Task"
	in.Prompt = "Do work"
	in.Cron = "* * * * *"
	in.Timezone = "UTC"
	in.SessionMode = "reuse"
	task, err := storage.Create(t.Context(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{Store: storage, Core: client, Build: Builder(cfg), Now: func() time.Time { return *now }}
	f.mu.Lock()
	t.Cleanup(f.mu.Unlock)
	return worker, f, now, task
}

// Tests own the fixture lock between ticks; HTTP handlers own it during a tick.
func tick(t *testing.T, w *Worker, f *coreFixture) {
	t.Helper()
	f.mu.Unlock()
	defer f.mu.Lock()
	if err := w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func history(t *testing.T, w *Worker, id uuid.UUID) []schedule.Occurrence {
	t.Helper()
	page, err := w.Store.History(t.Context(), id, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}
func complete(f *coreFixture, at time.Time) {
	for id, run := range f.runs {
		run.Status = coreapi.RunStatusCompleted
		run.FinishedAt = new(at)
		f.runs[id] = run
	}
}
func TestReuseResetAndConfigurationChange(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = now.Add(time.Minute)
	tick(t, w, f)
	if len(f.requests) != 1 || f.requests[0].path != "/api/v1/sessions" {
		t.Fatal(f.requests)
	}
	var request coreapi.CreateSession
	if err := json.Unmarshal(f.requests[0].body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Configuration.Agent.Instructions != nil {
		t.Fatal("omitted instructions override profile")
	}
	if request.AllowMultipleRuns == nil || !*request.AllowMultipleRuns || *request.Namespace != "schedule" || *request.ExternalKey != task.ID.String() || !strings.Contains(request.Messages[0].Text, "scheduled_at:") {
		t.Fatal(request)
	}
	if _, err := w.Store.ResetSession(t.Context(), task.ID); err == nil {
		t.Fatal("reset active accepted run")
	}
	complete(f, now.Add(20*time.Second))
	*now = now.Add(time.Minute)
	tick(t, w, f)
	if len(f.requests) != 2 || !strings.HasSuffix(f.requests[1].path, "/runs") {
		t.Fatal(f.requests)
	}
	complete(f, now.Add(time.Second))
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	if _, err := w.Store.ResetSession(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(50 * time.Second)
	tick(t, w, f)
	if len(f.requests) != 3 || f.requests[2].path != "/api/v1/sessions" {
		t.Fatal(f.requests)
	}
	complete(f, now.Add(time.Second))
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	if _, err := w.Store.Update(t.Context(), task.ID, []byte(`{"model":"other","env_from":["B"]}`)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(50 * time.Second)
	tick(t, w, f)
	if len(f.requests) != 4 || f.requests[3].path != "/api/v1/sessions" {
		t.Fatal(f.requests)
	}
	if err := json.Unmarshal(f.requests[3].body, &request); err != nil || request.Configuration.Agent.Model == nil || *request.Configuration.Agent.Model != "other" || len(*request.Configuration.Sandbox.EnvFrom) != 2 {
		t.Fatal(request, err)
	}
	got, err := w.Store.Get(t.Context(), task.ID)
	if err != nil || got.LastOccurrence == nil || got.LastOccurrence.RunID == nil {
		t.Fatal(got, err)
	}
}
func TestLostResponseRestartAndPause(t *testing.T) {
	w, f, now, task := fixture(t)
	f.lose = true
	*now = now.Add(time.Minute)
	tick(t, w, f)
	if rows := history(t, w, task.ID); len(rows) != 1 || rows[0].State != "dispatching" {
		t.Fatal(rows)
	}
	if _, err := w.Store.Update(t.Context(), task.ID, []byte(`{"status":"paused","prompt":"new prompt","model":"new"}`)); err != nil {
		t.Fatal(err)
	}
	replacement := *w
	w = &replacement
	*now = now.Add(time.Minute)
	tick(t, w, f)
	if len(f.accepted) != 1 || len(f.requests) != 2 || f.requests[0].key != f.requests[1].key || string(f.requests[0].body) != string(f.requests[1].body) || f.requests[0].path != f.requests[1].path {
		t.Fatal(f.requests)
	}
	if rows := history(t, w, task.ID); len(rows) != 1 || rows[0].State != "accepted" {
		t.Fatal(rows)
	}
	complete(f, now.Add(time.Second))
	*now = now.Add(2 * time.Minute)
	tick(t, w, f)
	if len(history(t, w, task.ID)) != 1 {
		t.Fatal("paused periods were queued")
	}
}
func TestActiveSkipAndUnknownStateRecovery(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = now.Add(time.Minute)
	tick(t, w, f)
	*now = now.Add(2 * time.Minute)
	tick(t, w, f)
	rows := history(t, w, task.ID)
	if len(rows) != 2 || rows[0].State != "skipped" || *rows[0].ErrorCode != "previous_run_active" {
		t.Fatal(rows)
	}
	f.offline = true
	*now = now.Add(3 * time.Minute)
	tick(t, w, f)
	got, _ := w.Store.Get(t.Context(), task.ID)
	if !got.NextRunAt.Before(*now) || len(history(t, w, task.ID)) != 2 {
		t.Fatal("unknown state was treated as completion")
	}
	complete(f, now.Add(-90*time.Second))
	f.offline = false
	*now = now.Add(time.Minute)
	tick(t, w, f)
	if len(f.requests) != 2 {
		t.Fatal("did not catch up latest period after actual finished_at", len(f.requests))
	}
	rows = history(t, w, task.ID)
	if !rows[0].ScheduledAt.Equal(*now) {
		t.Fatal(rows)
	}
}
func TestPauseAndDeleteCancelPending(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "delete"}[remove], func(t *testing.T) {
			w, f, now, task := fixture(t)
			*now = now.Add(time.Minute)
			if err := w.Store.Plan(t.Context(), task.ID, *now); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Store.ResetSession(t.Context(), task.ID); err == nil {
				t.Fatal("reset pending")
			}
			var err error
			if remove {
				err = w.Store.Delete(t.Context(), task.ID)
			} else {
				_, err = w.Store.Update(t.Context(), task.ID, []byte(`{"status":"paused"}`))
			}
			if err != nil {
				t.Fatal(err)
			}
			tick(t, w, f)
			if len(f.requests) != 0 || history(t, w, task.ID)[0].State != "cancelled" {
				t.Fatal(f.requests)
			}
		})
	}
}
func TestDispatchFailures(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		lost   bool
		state  string
	}{{"capacity_exhausted", 503, false, "dispatching"}, {"validation_error", 422, false, "failed"}, {"token_limit_exceeded", 409, false, "failed"}, {"session_unavailable", 409, false, "failed"}, {"unauthorized", 401, true, "dispatching"}, {"idempotency_conflict", 409, false, "dispatching"}} {
		t.Run(tc.code, func(t *testing.T) {
			w, f, now, task := fixture(t)
			*now = now.Add(time.Minute)
			if tc.lost {
				f.lose = true
				tick(t, w, f)
				*now = now.Add(10 * time.Second)
			}
			f.postError, f.postStatus = tc.code, tc.status
			tick(t, w, f)
			rows := history(t, w, task.ID)
			if len(rows) != 1 || rows[0].State != tc.state || *rows[0].ErrorCode != tc.code {
				t.Fatal(rows)
			}
		})
	}
}
func TestLeadershipLossPreventsDispatch(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = now.Add(time.Minute)
	lease, err := Acquire(t.Context(), w.Store.Pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if other, err := Acquire(t.Context(), w.Store.Pool.Config().ConnConfig); err == nil {
		other.Close()
		t.Fatal("second worker acquired lock")
	}
	w.Check = lease.Check
	if _, err = w.Store.Pool.Exec(t.Context(), "SELECT pg_terminate_backend($1)", lease.conn.PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	if err = w.Tick(t.Context()); err == nil {
		t.Fatal("lost lock did not stop tick")
	}
	if len(f.requests) != 0 || len(history(t, w, task.ID)) != 0 {
		t.Fatal("worker dispatched without lock")
	}
}
func TestUncertainRequestPersistsBeforeNetwork(t *testing.T) {
	w, _, now, task := fixture(t)
	*now = now.Add(time.Minute)
	if err := w.Store.Plan(t.Context(), task.ID, *now); err != nil {
		t.Fatal(err)
	}
	occ := history(t, w, task.ID)[0]
	prepared, err := w.Store.Prepare(t.Context(), task.ID, occ.ID, *now, w.Build)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.New(w.Store.Pool).MarkAttempt(t.Context(), db.MarkAttemptParams{ID: prepared.ID, UpdatedAt: *now})
	if err != nil {
		t.Fatal(err)
	}
	w.Check = func(context.Context) error { return errors.New("lost lock") }
	if err = w.Tick(t.Context()); err == nil {
		t.Fatal("ignored lost lock")
	}
	persisted, err := db.New(w.Store.Pool).GetOccurrence(t.Context(), db.GetOccurrenceParams{ScheduleID: task.ID, ID: occ.ID})
	if err != nil || !persisted.Uncertain || len(persisted.RequestBody) == 0 {
		t.Fatal(persisted, err)
	}
}

func TestSnapshotFingerprintAndNewMode(t *testing.T) {
	cfg := config.Config{AllowedEnv: []string{"A", "B"}}
	cfg.Execution.Agent.Profile = "default"
	cfg.Execution.Sandbox.Template = "template"
	cfg.Execution.Sandbox.EnvFrom = []string{"A"}
	row := db.Schedule{ID: uuid.New(), Prompt: "first", Timezone: "UTC", SessionMode: "reuse", EnvFrom: []string{"A", "B"}}
	occ := db.ScheduleOccurrence{ID: uuid.New(), ScheduledAt: time.Now()}
	now := time.Now()
	first, err := Builder(cfg)(row, occ, now)
	if err != nil {
		t.Fatal(err)
	}
	row.ReusableSessionID = new(uuid.New())
	row.ReusableFingerprint = &first.Fingerprint
	row.Prompt = "changed"
	row.Name = "renamed"
	reused, err := Builder(cfg)(row, occ, now)
	if err != nil || reused.Path == "/api/v1/sessions" || reused.Fingerprint != first.Fingerprint {
		t.Fatal(reused, err)
	}
	cfg.Execution.Agent.Instructions = new("changed base instructions")
	fresh, err := Builder(cfg)(row, occ, now)
	if err != nil || fresh.Path != "/api/v1/sessions" || fresh.Fingerprint == first.Fingerprint {
		t.Fatal(fresh, err)
	}
	row.SessionMode = "new"
	fresh, err = Builder(cfg)(row, occ, now)
	if err != nil || fresh.Reusable {
		t.Fatal(fresh, err)
	}
	var body coreapi.CreateSession
	if err = json.Unmarshal(fresh.Body, &body); err != nil || body.AllowMultipleRuns == nil || *body.AllowMultipleRuns {
		t.Fatal(body, err)
	}
}
func TestHistoryCursorAndActualCompletionBoundary(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = now.Add(time.Minute)
	tick(t, w, f)
	complete(f, now.Add(2*time.Minute))
	*now = now.Add(2*time.Minute + 30*time.Second)
	tick(t, w, f)
	// 00:03 is before/at finished_at, so it must not be run after recovery at 00:03:30.
	rows := history(t, w, task.ID)
	if len(rows) != 2 || rows[0].State != "skipped" || len(f.requests) != 1 {
		t.Fatal(rows)
	}
	first, err := w.Store.History(t.Context(), task.ID, 1, "")
	if err != nil || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	*now = now.Add(time.Minute)
	tick(t, w, f)
	second, err := w.Store.History(t.Context(), task.ID, 1, *first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != rows[1].ID {
		t.Fatal(second, err)
	}
}

type blockingCore struct {
	Core
	started chan struct{}
}

func (c *blockingCore) Dispatch(ctx context.Context, _ string, _ []byte, _ uuid.UUID) (coreapi.Accepted, error) {
	close(c.started)
	<-ctx.Done()
	return coreapi.Accepted{}, ctx.Err()
}
func TestLostLeaseCancelsInFlightRequest(t *testing.T) {
	w, _, now, task := fixture(t)
	w.Poll = time.Hour
	*now = now.Add(time.Minute)
	lease, err := Acquire(t.Context(), w.Store.Pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	blocked := &blockingCore{Core: w.Core, started: make(chan struct{})}
	w.Core = blocked
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done <- w.Run(ctx, lease) }()
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not dispatch")
	}
	if _, err = w.Store.Pool.Exec(t.Context(), "SELECT pg_terminate_backend($1)", lease.conn.PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost lease succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request was not cancelled")
	}
	rows := history(t, w, task.ID)
	if len(rows) != 1 || rows[0].State != "dispatching" {
		t.Fatal(rows)
	}
}

func TestBusySessionWaitsWithoutNewDispatch(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = now.Add(time.Minute)
	tick(t, w, f)
	complete(f, now.Add(time.Second))
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	original := history(t, w, task.ID)[0]
	external := uuid.New()
	f.runs[external] = coreapi.Run{ID: external, SessionID: *original.SessionID, Status: coreapi.RunStatusRunning}
	f.postError, f.postStatus = "session_busy", 409
	*now = now.Add(50 * time.Second)
	tick(t, w, f)
	count := len(f.requests)
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	if len(f.requests) != count {
		t.Fatal("posted again while external run active")
	}
	complete(f, *now)
	f.postError = ""
	*now = now.Add(10 * time.Second)
	tick(t, w, f)
	if len(f.requests) != count+1 || f.requests[count].path != f.requests[count-1].path || f.requests[count].key != f.requests[count-1].key {
		t.Fatal(f.requests)
	}
}

func TestMissingRunBlocksUntilRecovered(t *testing.T) {
	w, f, now, task := fixture(t)
	*now = now.Add(time.Minute)
	tick(t, w, f)
	tick(t, w, f)
	before := history(t, w, task.ID)[0]
	run := f.runs[*before.RunID]
	delete(f.runs, run.ID)
	*now = now.Add(3 * time.Minute)
	tick(t, w, f)
	rows := history(t, w, task.ID)
	if len(rows) != 1 || len(f.requests) != 1 {
		t.Fatal("missing run allowed further execution", rows)
	}
	got := rows[0]
	if got.State != "accepted" || got.SyncErrorCode == nil || *got.SyncErrorCode != "run_not_found" || *got.RunStatus != *before.RunStatus || !got.ObservedAt.Equal(*before.ObservedAt) {
		t.Fatal("missing run lost cached observation", got)
	}
	_, err := w.Store.ResetSession(t.Context(), task.ID)
	if e, ok := errors.AsType[*schedule.Error](err); !ok || e.Status != 409 {
		t.Fatal("reset allowed unresolved run", err)
	}
	run.Status = coreapi.RunStatusCompleted
	run.FinishedAt = new(now.Add(-90 * time.Second))
	f.runs[run.ID] = run
	*now = now.Add(time.Minute)
	tick(t, w, f)
	rows = history(t, w, task.ID)
	if len(f.requests) != 2 || !rows[0].ScheduledAt.Equal(*now) || rows[1].SyncErrorCode != nil || rows[1].FinishedAt == nil || !rows[1].FinishedAt.Equal(*run.FinishedAt) {
		t.Fatal("did not recover using actual completion boundary", rows)
	}
}
