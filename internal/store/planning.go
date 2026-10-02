package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

type Snapshot struct {
	Path        string
	Body        []byte
	Fingerprint string
	Reusable    bool
}
type BuildSnapshot func(db.Schedule, db.ScheduleOccurrence, *db.ScheduleOccurrence, time.Time) (Snapshot, error)

func (s *Store) Plan(ctx context.Context, id uuid.UUID, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	row, err := q.LockSchedule(ctx, id)
	if err != nil {
		return err
	}
	if row.DeletedAt != nil || row.Status != "active" || row.NextRunAt == nil || row.NextRunAt.After(now) {
		return nil
	}
	active, err := q.ActiveOccurrence(ctx, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	hasActive := err == nil
	// Uncertain outcomes must be reconciled before deciding whether a period was
	// missed. A cached active status is usable only after a successful sync.
	if hasActive && (active.State != "accepted" || active.SyncErrorCode != nil) {
		return nil
	}
	latest, err := schedule.Latest(row.Cron, row.Timezone, *row.NextRunAt, now)
	if err != nil {
		return err
	}
	next, err := schedule.Next(row.Cron, row.Timezone, now)
	if err != nil {
		return err
	}
	state := "pending"
	var completed *time.Time
	var reason *string
	if hasActive {
		state = "skipped"
		completed = new(now)
		reason = new("previous_run_active")
	}
	if !hasActive {
		boundary, err := q.LatestCompletion(ctx, id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && !latest.After(boundary) {
			state = "skipped"
			completed = new(now)
			reason = new("previous_run_active")
		}
	}
	if _, err = q.InsertOccurrence(ctx, db.InsertOccurrenceParams{ID: uuid.New(), ScheduleID: id, ScheduledAt: latest, State: state, CreatedAt: now, CompletedAt: completed, RequestKey: uuid.New(), ErrorCode: reason}); err != nil {
		return err
	}
	if err = q.AdvanceSchedule(ctx, db.AdvanceScheduleParams{ID: id, NextRunAt: &next}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Prepare(ctx context.Context, sid, id uuid.UUID, now time.Time, build BuildSnapshot) (db.ScheduleOccurrence, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return db.ScheduleOccurrence{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	row, err := q.LockSchedule(ctx, sid)
	if err != nil {
		return db.ScheduleOccurrence{}, err
	}
	occ, err := q.GetOccurrence(ctx, db.GetOccurrenceParams{ScheduleID: sid, ID: id})
	if err != nil {
		return occ, err
	}
	if occ.State != "pending" {
		return occ, nil
	}
	if row.DeletedAt != nil || row.Status != "active" {
		err = q.CancelPending(ctx, db.CancelPendingParams{ScheduleID: sid, CompletedAt: new(now)})
		if err != nil {
			return occ, err
		}
		occ.State = "cancelled"
		return occ, tx.Commit(ctx)
	}
	previous, err := q.LastSuccessfulOccurrence(ctx, db.LastSuccessfulOccurrenceParams{ScheduleID: sid, ScheduledAt: occ.ScheduledAt})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return occ, err
	}
	var lastSuccess *db.ScheduleOccurrence
	if err == nil {
		lastSuccess = &previous
	}
	snapshot, err := build(row, occ, lastSuccess, now)
	if err != nil {
		if problem, ok := errors.AsType[*schedule.Error](err); ok {
			err = q.FinishDispatch(ctx, db.FinishDispatchParams{ID: id, State: "failed", ErrorCode: new(problem.Problem.Code), CompletedAt: new(now)})
			if err != nil {
				return occ, err
			}
			occ.State = "failed"
			return occ, tx.Commit(ctx)
		}
		return occ, err
	}
	occ, err = q.PrepareOccurrence(ctx, db.PrepareOccurrenceParams{ID: id, RequestPath: &snapshot.Path, RequestBody: snapshot.Body, RequestKey: occ.RequestKey, Fingerprint: &snapshot.Fingerprint, Reusable: snapshot.Reusable, UpdatedAt: now})
	if err != nil {
		return occ, err
	}
	return occ, tx.Commit(ctx)
}
func (s *Store) Accept(ctx context.Context, occ db.ScheduleOccurrence, sid, rid uuid.UUID, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if _, err = q.LockSchedule(ctx, occ.ScheduleID); err != nil {
		return err
	}
	if err = q.AcceptOccurrence(ctx, db.AcceptOccurrenceParams{ID: occ.ID, SessionID: &sid, RunID: &rid, ObservedAt: &now}); err != nil {
		return err
	}
	if occ.Reusable {
		if err = q.SaveReusableSession(ctx, db.SaveReusableSessionParams{ID: occ.ScheduleID, ReusableSessionID: &sid, ReusableFingerprint: occ.Fingerprint}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
