// Package worker schedules and reconciles occurrences with the Orpheus core.
package worker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/core"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	coreapi "github.com/orpheus-agents/orpheus/client"
	"golang.org/x/sync/errgroup"
)

type Core interface {
	Dispatch(context.Context, string, []byte, uuid.UUID) (coreapi.Accepted, error)
	Run(context.Context, uuid.UUID, uuid.UUID) (coreapi.Run, error)
	ActiveRun(context.Context, uuid.UUID) (*uuid.UUID, error)
}
type Worker struct {
	Poll  time.Duration
	Store *store.Store
	Core  Core
	Build store.BuildSnapshot
	Now   func() time.Time
	Check func(context.Context) error
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (w *Worker) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.Check != nil {
		return w.Check(ctx)
	}
	return nil
}
func (w *Worker) Tick(ctx context.Context) error {
	if err := w.check(ctx); err != nil {
		return err
	}
	// Reconcile before planning: actual core finished_at determines catch-up.
	if err := w.work(ctx); err != nil {
		return err
	}
	ids, err := db.New(w.Store.Pool).DueSchedules(ctx, new(w.now()))
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = w.check(ctx); err != nil {
			return err
		}
		if err = w.Store.Plan(ctx, id, w.now()); err != nil {
			return err
		}
	}
	return w.work(ctx)
}
func (w *Worker) work(ctx context.Context) error {
	rows, err := db.New(w.Store.Pool).WorkOccurrences(ctx, new(w.now()))
	if err != nil {
		return err
	}
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for _, row := range rows {
		group.Go(func() error { return w.reconcile(ctx, row) })
	}
	return group.Wait()
}
func (w *Worker) reconcile(ctx context.Context, occ db.ScheduleOccurrence) error {
	if err := w.check(ctx); err != nil {
		return err
	}
	var err error
	if occ.State == "pending" {
		occ, err = w.Store.Prepare(ctx, occ.ScheduleID, occ.ID, w.now(), w.Build)
		if err != nil {
			return err
		}
	}
	if occ.State == "accepted" {
		return w.observe(ctx, occ)
	}
	if occ.State != "dispatching" {
		return nil
	}
	q := db.New(w.Store.Pool)
	if occ.ErrorCode != nil && *occ.ErrorCode == "session_busy" {
		sid, err := uuid.Parse(strings.TrimSuffix(strings.TrimPrefix(*occ.RequestPath, "/api/v1/sessions/"), "/runs"))
		if err == nil {
			active, readErr := w.Core.ActiveRun(ctx, sid)
			wait := readErr != nil
			if readErr == nil && active != nil {
				run, e := w.Core.Run(ctx, sid, *active)
				wait = e != nil || !core.Terminal(run.Status)
			}
			if wait {
				return q.RetryOccurrence(ctx, db.RetryOccurrenceParams{ID: occ.ID, ErrorCode: occ.ErrorCode, Uncertain: occ.Uncertain, NextAttemptAt: new(w.now().Add(5 * time.Second)), UpdatedAt: w.now()})
			}
		}
	}
	previousUncertainty := occ.Uncertain
	occ, err = q.MarkAttempt(ctx, db.MarkAttemptParams{ID: occ.ID, UpdatedAt: w.now()})
	if err != nil {
		return err
	}
	if err = w.check(ctx); err != nil {
		return err
	}
	accepted, err := w.Core.Dispatch(ctx, *occ.RequestPath, occ.RequestBody, occ.RequestKey)
	if err == nil {
		return w.Store.Accept(ctx, occ, accepted.SessionID, accepted.RunID, w.now())
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	failure, known := errors.AsType[*core.Failure](err)
	code := core.ErrorCode(err)
	uncertain := previousUncertainty || !known || failure.Uncertain

	retry := uncertain || code == "capacity_exhausted" || code == "session_busy" || code == "idempotency_conflict" || known && (failure.Status == 429 || failure.Status >= 500)
	if !retry {
		return q.FinishDispatch(ctx, db.FinishDispatchParams{ID: occ.ID, State: "failed", ErrorCode: &code, CompletedAt: new(w.now())})
	}
	delay := time.Second * time.Duration(1<<min(occ.Attempts, 6))
	return q.RetryOccurrence(ctx, db.RetryOccurrenceParams{ID: occ.ID, ErrorCode: &code, Uncertain: uncertain, NextAttemptAt: new(w.now().Add(delay)), UpdatedAt: w.now()})
}
func (w *Worker) observe(ctx context.Context, occ db.ScheduleOccurrence) error {
	q := db.New(w.Store.Pool)
	run, err := w.Core.Run(ctx, *occ.SessionID, *occ.RunID)
	if err == nil && core.Terminal(run.Status) && run.FinishedAt == nil {
		err = &core.Failure{Code: "core_invalid_response", Uncertain: true}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return q.ObserveFailure(ctx, db.ObserveFailureParams{ID: occ.ID, SyncErrorCode: new(core.ErrorCode(err)), NextAttemptAt: new(w.now().Add(15 * time.Second)), UpdatedAt: w.now()})
	}
	var code *string
	if run.Error != nil {
		code = &run.Error.Code
	}
	var completed *time.Time
	next := new(w.now().Add(5 * time.Second))
	if core.Terminal(run.Status) {
		completed = new(w.now())
		next = nil
	}
	return q.ObserveRun(ctx, db.ObserveRunParams{ID: occ.ID, RunStatus: new(string(run.Status)), ObservedAt: new(w.now()), ExecutionStartedAt: run.ExecutionStartedAt, FinishedAt: run.FinishedAt, RunErrorCode: code, CompletedAt: completed, NextAttemptAt: next})
}
