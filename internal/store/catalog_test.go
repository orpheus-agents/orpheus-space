//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type checkedCatalog struct {
	testutil.Catalog
	calls  int
	err    error
	before func()
}

func (c *checkedCatalog) Profiles(ctx context.Context) (coreapi.Profiles, error) {
	c.calls++
	if c.before != nil {
		c.before()
	}
	if c.err != nil {
		return coreapi.Profiles{}, c.err
	}
	return c.Catalog.Profiles(ctx)
}
func (c *checkedCatalog) Templates(ctx context.Context) (coreapi.Templates, error) {
	c.calls++
	if c.err != nil {
		return coreapi.Templates{}, c.err
	}
	return c.Catalog.Templates(ctx)
}

func TestSelectionDefaultsReplayAndOfflineEdits(t *testing.T) {
	s, in := fixture(t)
	catalog := &checkedCatalog{}
	s.Catalog = catalog
	key := uuid.New()
	first, err := s.Create(t.Context(), in, &key)
	if err != nil || first.Profile != "default" || first.Template != "sandbox" || catalog.calls != 2 {
		t.Fatal(first, err, catalog.calls)
	}
	s.DefaultProfile, s.DefaultTemplate = "removed", "removed"
	catalog.err = errors.New("offline")
	replay, err := s.Create(t.Context(), in, &key)
	if err != nil || replay.ID != first.ID || replay.Profile != first.Profile || catalog.calls != 2 {
		t.Fatal(replay, err, catalog.calls)
	}
	in.Profile = "default"
	_, err = s.Create(t.Context(), in, &key)
	requireStatus(t, err, 409)
	for _, patch := range []string{`{"name":"renamed"}`, `{"status":"paused"}`, `{"profile":"default","template":"sandbox"}`, `{"prompt":"changed"}`} {
		if _, err := s.Update(t.Context(), first.ID, []byte(patch)); err != nil {
			t.Fatal(patch, err)
		}
	}
	if catalog.calls != 2 {
		t.Fatal("unchanged selections queried Orpheus", catalog.calls)
	}
	_, err = s.Update(t.Context(), first.ID, []byte(`{"profile":"other"}`))
	requireStatus(t, err, 503)
	if err = s.Delete(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionValidationAndReuseReset(t *testing.T) {
	s, in := fixture(t)
	for _, field := range []string{"profile", "template"} {
		bad := in
		if field == "profile" {
			bad.Profile = "missing"
		} else {
			bad.Template = "missing"
		}
		_, err := s.Create(t.Context(), bad, nil)
		requireStatus(t, err, 422)
	}
	in.Profile, in.Template, in.SessionMode = "other", "other", "reuse"
	task, err := s.Create(t.Context(), in, nil)
	if err != nil || task.Profile != "other" || task.Template != "other" {
		t.Fatal(task, err)
	}
	_, err = s.Pool.Exec(t.Context(), `UPDATE schedules SET reusable_session_id=$2,reusable_fingerprint='saved' WHERE id=$1`, task.ID, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []string{`{"profile":null}`, `{"template":null}`, `{"profile":""}`, `{"template":" "}`, `{"profile":"missing"}`} {
		_, err = s.Update(t.Context(), task.ID, []byte(patch))
		requireStatus(t, err, 422)
	}
	task, err = s.Update(t.Context(), task.ID, []byte(`{"profile":"default"}`))
	if err != nil || task.Template != "other" {
		t.Fatal(task, err)
	}
	var cleared bool
	if err = s.Pool.QueryRow(t.Context(), `SELECT reusable_session_id IS NULL AND reusable_fingerprint IS NULL FROM schedules WHERE id=$1`, task.ID).Scan(&cleared); err != nil || !cleared {
		t.Fatal(cleared, err)
	}
}

func TestSelectionValidationDoesNotHoldLocksAndRechecksRaces(t *testing.T) {
	s, in := fixture(t)
	task, err := s.Create(t.Context(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &checkedCatalog{}
	s.Catalog = catalog
	catalog.before = func() {
		catalog.before = nil
		// Would time out if the update held a row lock across the catalog request.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := s.Pool.Exec(ctx, `UPDATE schedules SET template='other',prompt='concurrent edit' WHERE id=$1`, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.Update(t.Context(), task.ID, []byte(`{"profile":"other"}`))
	if err != nil || out.Profile != "other" || out.Template != "other" || out.Prompt != "concurrent edit" || catalog.calls != 2 {
		t.Fatal(out, err, catalog.calls)
	}
}
