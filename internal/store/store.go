// Package store owns transactional schedule persistence.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
)

type Store struct {
	Pool       *pgxpool.Pool
	AllowedEnv []string
	Now        func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func record(row db.Schedule) schedule.Schedule {
	return schedule.Schedule{Name: row.Name, Prompt: row.Prompt, Cron: row.Cron, Timezone: row.Timezone, Status: row.Status, Model: row.Model, SessionMode: row.SessionMode, OwnerEmail: row.OwnerEmail, EnvFrom: row.EnvFrom, ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, NextRunAt: row.NextRunAt, DeletedAt: row.DeletedAt}
}
func missing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return schedule.Fail(404, "schedule_not_found", "Schedule not found.")
	}
	return err
}
func (s *Store) Get(ctx context.Context, id uuid.UUID) (schedule.Schedule, error) {
	row, err := db.New(s.Pool).GetSchedule(ctx, id)
	return record(row), missing(err)
}
func (s *Store) Create(ctx context.Context, in schedule.Input, key *uuid.UUID) (schedule.Schedule, error) {
	now := s.now()
	in, err := schedule.Normalize(in, s.AllowedEnv, now)
	if err != nil {
		return schedule.Schedule{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return schedule.Schedule{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if key != nil {
		normalized, _ := json.Marshal(in)
		hash := sha256.Sum256(normalized)
		fingerprint := hex.EncodeToString(hash[:])
		_, err = q.ReserveCreateKey(ctx, db.ReserveCreateKeyParams{Key: *key, Fingerprint: fingerprint})
		if errors.Is(err, pgx.ErrNoRows) {
			previous, err := q.GetCreateKey(ctx, *key)
			if err != nil {
				return schedule.Schedule{}, err
			}
			if previous.Fingerprint != fingerprint {
				return schedule.Schedule{}, schedule.Fail(409, "idempotency_conflict", "Key was used with a different request.")
			}
			var result schedule.Schedule
			err = json.Unmarshal(previous.Response, &result)
			return result, err
		}
		if err != nil {
			return schedule.Schedule{}, err
		}
	}
	var next *time.Time
	if in.Status == "active" {
		value, err := schedule.Next(in.Cron, in.Timezone, now)
		if err != nil {
			return schedule.Schedule{}, err
		}
		next = &value
	}
	row, err := q.CreateSchedule(ctx, db.CreateScheduleParams{ID: uuid.New(), Name: in.Name, Prompt: in.Prompt, Cron: in.Cron, Timezone: in.Timezone, Status: in.Status, Model: in.Model, SessionMode: in.SessionMode, OwnerEmail: in.OwnerEmail, EnvFrom: in.EnvFrom, CreatedAt: now, NextRunAt: next})
	if err != nil {
		return schedule.Schedule{}, err
	}
	result := record(row)
	if key != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return schedule.Schedule{}, err
		}
		if err = q.SaveCreateResponse(ctx, db.SaveCreateResponseParams{Key: *key, Response: raw}); err != nil {
			return schedule.Schedule{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return schedule.Schedule{}, err
	}
	return result, nil
}
func (s *Store) Update(ctx context.Context, id uuid.UUID, patch []byte) (schedule.Schedule, error) {
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
	if row.DeletedAt != nil {
		return schedule.Schedule{}, schedule.Fail(409, "schedule_deleted", "Deleted schedules cannot be edited.")
	}
	current := record(row)
	in, err := schedule.Patch(current.Input, patch)
	if err != nil {
		return schedule.Schedule{}, err
	}
	now := s.now()
	in, err = schedule.Normalize(in, s.AllowedEnv, now)
	if err != nil {
		return schedule.Schedule{}, err
	}
	next, start := row.NextRunAt, row.CronStartedAt
	if in.Cron != current.Cron || in.Timezone != current.Timezone || current.Status == "paused" && in.Status == "active" {
		start = now
		value, err := schedule.Next(in.Cron, in.Timezone, now)
		if err != nil {
			return schedule.Schedule{}, err
		}
		next = &value
	}
	if in.Status == "paused" {
		next = nil
	}
	row, err = q.UpdateSchedule(ctx, db.UpdateScheduleParams{ID: id, Name: in.Name, Prompt: in.Prompt, Cron: in.Cron, Timezone: in.Timezone, Status: in.Status, Model: in.Model, SessionMode: in.SessionMode, OwnerEmail: in.OwnerEmail, EnvFrom: in.EnvFrom, UpdatedAt: now, NextRunAt: next, CronStartedAt: start})
	if err != nil {
		return schedule.Schedule{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return schedule.Schedule{}, err
	}
	return record(row), nil
}
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if _, err = q.LockSchedule(ctx, id); err != nil {
		return missing(err)
	}
	if err = q.DeleteSchedule(ctx, db.DeleteScheduleParams{ID: id, DeletedAt: new(s.now())}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type Filter struct {
	Owners  []string `json:"owners"`
	Unowned bool     `json:"unowned"`
	Status  string   `json:"status"`
}
type cursor struct {
	Version  int       `json:"v"`
	Upper    int64     `json:"upper"`
	Position time.Time `json:"at"`
	ID       uuid.UUID `json:"id"`
	Filter   string    `json:"filter"`
}

func invalidCursor() error {
	err := schedule.InvalidAt("query", "cursor")
	err.Problem.Code = "invalid_cursor"
	err.Problem.Message = "Invalid cursor or changed filters."
	return err
}
func normalizeFilter(f Filter) (Filter, error) {
	if f.Unowned && len(f.Owners) > 0 {
		return f, schedule.InvalidAt("query", "unowned")
	}
	if f.Status != "" && f.Status != "active" && f.Status != "paused" {
		return f, schedule.InvalidAt("query", "status")
	}
	owners := []string{}
	for _, raw := range f.Owners {
		email, err := schedule.Email(raw)
		if err != nil {
			return f, schedule.InvalidAt("query", "owner_email")
		}
		owners = append(owners, email)
	}
	slices.Sort(owners)
	f.Owners = slices.Compact(owners)
	if len(f.Owners) > 100 {
		return f, schedule.InvalidAt("query", "owner_email")
	}
	return f, nil
}
func (s *Store) List(ctx context.Context, filter Filter, limit int, token string) (schedule.Page, error) {
	page := schedule.Page{Items: []schedule.Schedule{}}
	if limit < 1 || limit > 200 {
		return page, schedule.InvalidAt("query", "limit")
	}
	filter, err := normalizeFilter(filter)
	if err != nil {
		return page, err
	}
	q := db.New(s.Pool)
	rawFilter, _ := json.Marshal(filter)
	hash := sha256.Sum256(rawFilter)
	fingerprint := hex.EncodeToString(hash[:])
	c := cursor{Version: 1, Filter: fingerprint}
	if token != "" {
		if len(token) > 8192 {
			return page, invalidCursor()
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return page, invalidCursor()
		}
		if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Upper < 0 || c.ID == uuid.Nil || c.Position.IsZero() || c.Filter != fingerprint {
			return page, invalidCursor()
		}
	} else {
		c.Upper, err = q.ScheduleUpperBound(ctx)
		if err != nil {
			return page, err
		}
	}
	rows, err := q.ListSchedules(ctx, db.ListSchedulesParams{UpperSequence: c.Upper, Status: filter.Status, Unowned: filter.Unowned, Owners: filter.Owners, HasPosition: token != "", PositionTime: c.Position, PositionID: c.ID, PageLimit: limit + 1})
	if err != nil {
		return page, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, record(row))
	}
	if more {
		last := rows[len(rows)-1]
		c.Position, c.ID = last.CreatedAt, last.ID
		raw, _ := json.Marshal(c)
		page.NextCursor = new(base64.RawURLEncoding.EncodeToString(raw))
	}
	return page, nil
}
