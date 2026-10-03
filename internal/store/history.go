package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

func occurrence(row db.ScheduleOccurrence) schedule.Occurrence {
	return schedule.Occurrence{ID: row.ID, ScheduleID: row.ScheduleID, ScheduledAt: row.ScheduledAt, State: row.State, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, SessionID: row.SessionID, RunID: row.RunID, RunStatus: row.RunStatus, ObservedAt: row.ObservedAt, ExecutionStartedAt: row.ExecutionStartedAt, FinishedAt: row.FinishedAt, RunErrorCode: row.RunErrorCode, SyncErrorCode: row.SyncErrorCode, ErrorCode: row.ErrorCode, Attempts: row.Attempts, NextAttemptAt: row.NextAttemptAt}
}
func summary(ctx context.Context, q *db.Queries, s *schedule.Schedule) error {
	row, err := q.LastOccurrence(ctx, s.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.LastOccurrence = new(occurrence(row))
	return nil
}
func (s *Store) Occurrence(ctx context.Context, sid, id uuid.UUID) (schedule.Occurrence, error) {
	row, err := db.New(s.Pool).GetOccurrence(ctx, db.GetOccurrenceParams{ScheduleID: sid, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return schedule.Occurrence{}, schedule.Fail(404, "occurrence_not_found", "Occurrence not found.")
	}
	return occurrence(row), err
}
func (s *Store) History(ctx context.Context, sid uuid.UUID, limit int, token string) (schedule.OccurrencePage, error) {
	page := schedule.OccurrencePage{Items: []schedule.Occurrence{}}
	if limit < 1 || limit > 200 {
		return page, schedule.InvalidAt("query", "limit")
	}
	q := db.New(s.Pool)
	if _, err := q.GetSchedule(ctx, sid); err != nil {
		return page, missing(err)
	}
	c := cursor{Version: 1, Filter: sid.String()}
	if token != "" {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if len(token) > 8192 || err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Filter != sid.String() || c.Upper < 0 || c.ID == uuid.Nil || c.Position.IsZero() {
			return page, invalidCursor()
		}
	} else {
		var err error
		c.Upper, err = q.OccurrenceUpperBound(ctx, sid)
		if err != nil {
			return page, err
		}
	}
	rows, err := q.ListOccurrences(ctx, db.ListOccurrencesParams{ScheduleID: sid, UpperSequence: c.Upper, HasPosition: token != "", PositionTime: c.Position, PositionID: c.ID, PageLimit: limit + 1})
	if err != nil {
		return page, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, occurrence(row))
	}
	if more {
		last := rows[len(rows)-1]
		c.Position, c.ID = last.ScheduledAt, last.ID
		raw, _ := json.Marshal(c)
		page.NextCursor = new(base64.RawURLEncoding.EncodeToString(raw))
	}
	return page, nil
}
func (s *Store) ResetSession(ctx context.Context, actor access.Principal, id uuid.UUID) (schedule.Schedule, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return schedule.Schedule{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	row, err := q.LockSchedule(ctx, id)
	if err != nil {
		return schedule.Schedule{}, missing(err)
	}
	if err := actor.RequireManage(row.OwnerEmail); err != nil {
		return schedule.Schedule{}, err
	}
	if row.DeletedAt != nil {
		return schedule.Schedule{}, schedule.Fail(409, "schedule_deleted", "Deleted schedules cannot be edited.")
	}
	if _, err = q.ActiveOccurrence(ctx, id); err == nil {
		return schedule.Schedule{}, schedule.Fail(409, "schedule_busy", "An occurrence is still active.")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return schedule.Schedule{}, err
	}
	if err = q.ClearReusableSession(ctx, id); err != nil {
		return schedule.Schedule{}, err
	}
	out := record(row)
	if err = summary(ctx, q, &out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
