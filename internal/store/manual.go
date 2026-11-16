package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

// RunSchedule enqueues one manual occurrence without changing the schedule.
func (s *Store) RunSchedule(ctx context.Context, actor access.Principal, id, key uuid.UUID) (schedule.Occurrence, error) {
	if key == uuid.Nil {
		return schedule.Occurrence{}, schedule.InvalidAt("header", "Idempotency-Key")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return schedule.Occurrence{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	row, err := q.LockSchedule(ctx, id)
	if err != nil {
		return schedule.Occurrence{}, missing(err)
	}
	if err = actor.RequireManage(row.OwnerEmail); err != nil {
		return schedule.Occurrence{}, err
	}
	previous, err := q.GetManualOccurrence(ctx, db.GetManualOccurrenceParams{ScheduleID: id, ManualKey: &key})
	if err == nil {
		return occurrence(previous), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return schedule.Occurrence{}, err
	}
	if row.DeletedAt != nil {
		return schedule.Occurrence{}, schedule.Fail(409, "schedule_deleted", "Deleted schedules cannot be run.")
	}
	if _, err = q.ActiveOccurrence(ctx, id); err == nil {
		return schedule.Occurrence{}, schedule.Fail(409, "schedule_busy", "An occurrence is still active.")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return schedule.Occurrence{}, err
	}
	created, err := q.InsertManualOccurrence(ctx, db.InsertManualOccurrenceParams{
		ID: uuid.New(), ScheduleID: id, ScheduledAt: s.now(),
		RequestKey: uuid.New(), ManualKey: &key,
	})
	if err != nil {
		return schedule.Occurrence{}, err
	}
	return occurrence(created), tx.Commit(ctx)
}
