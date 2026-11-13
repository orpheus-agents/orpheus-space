//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type checkedCatalog struct {
	testutil.Catalog
	calls    int
	err      error
	before   func()
	inactive bool
	missing  bool
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
func (c *checkedCatalog) Templates(ctx context.Context, params *coreapi.GetTemplatesParams) (coreapi.Templates, error) {
	c.calls++
	if params == nil || params.Name == nil || params.Active != nil || params.Cursor != nil {
		return coreapi.Templates{}, errors.New("selection must use an exact unfiltered lookup")
	}
	if c.err != nil {
		return coreapi.Templates{}, c.err
	}
	page, err := c.Catalog.Templates(ctx, params)
	if c.inactive {
		for i := range page.Items {
			page.Items[i].Active = false
		}
	}
	if c.missing {
		page.Items = []coreapi.Template{}
	}
	return page, err
}

func TestInactiveOrRemovedTemplateBlocksEditsButAllowsPauseAndDelete(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint("removed=", removed), func(t *testing.T) {
			s, in := fixture(t)
			catalog := &checkedCatalog{}
			s.Catalog = catalog
			actor := access.Principal{ManageAll: true}
			task, err := s.Create(t.Context(), actor, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			catalog.inactive = !removed
			catalog.missing = removed
			in.Status = "paused"
			_, err = s.Create(t.Context(), actor, in, nil)
			requireStatus(t, err, 422)
			for _, patch := range []string{`{"name":"new"}`, `{"template":"sandbox"}`, `{"profile":"default"}`, `{"status":"active"}`, `{"status":"paused","template":"sandbox"}`} {
				_, err = s.Update(t.Context(), actor, task.ID, []byte(patch))
				requireStatus(t, err, 422)
				problem, _ := errors.AsType[*schedule.Error](err)
				want := "template_inactive"
				if removed {
					want = "template_not_found"
				}
				if len(problem.Problem.Details) != 1 || problem.Problem.Details[0].Code != want || strings.Join(problem.Problem.Details[0].Path, ".") != "body.template" {
					t.Fatal(problem)
				}
			}
			before := catalog.calls
			if _, err = s.Update(t.Context(), actor, task.ID, []byte(`{"status":"paused"}`)); err != nil || catalog.calls != before {
				t.Fatal(err, catalog.calls)
			}
			_, err = s.Update(t.Context(), actor, task.ID, []byte(`{"status":"paused","name":"`+task.Name+`"}`))
			requireStatus(t, err, 422)
			before = catalog.calls
			if err = s.Delete(t.Context(), actor, task.ID); err != nil || catalog.calls != before {
				t.Fatal(err, catalog.calls)
			}
		})
	}
}

func TestSelectionDefaultsReplayAndOfflineEdits(t *testing.T) {
	s, in := fixture(t)
	catalog := &checkedCatalog{}
	s.Catalog = catalog
	key := uuid.New()
	first, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	if err != nil || first.Profile != "default" || first.Template != "sandbox" || catalog.calls != 2 {
		t.Fatal(first, err, catalog.calls)
	}
	s.DefaultProfile, s.DefaultTemplate = "removed", "removed"
	catalog.err = errors.New("offline")
	replay, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	if err != nil || replay.ID != first.ID || replay.Profile != first.Profile || catalog.calls != 2 {
		t.Fatal(replay, err, catalog.calls)
	}
	in.Profile = "default"
	_, err = s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	requireStatus(t, err, 409)
	if _, err := s.Update(t.Context(), access.Principal{ManageAll: true}, first.ID, []byte(`{"status":"paused"}`)); err != nil || catalog.calls != 2 {
		t.Fatal(err, catalog.calls)
	}
	for _, patch := range []string{`{"name":"renamed"}`, `{"profile":"default","template":"sandbox"}`, `{"prompt":"changed"}`, `{"status":"active"}`, `{"status":"paused","name":"unchanged"}`} {
		_, err := s.Update(t.Context(), access.Principal{ManageAll: true}, first.ID, []byte(patch))
		requireStatus(t, err, 503)
	}
	_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, first.ID, []byte(`{"profile":"other"}`))
	requireStatus(t, err, 503)
	if err = s.Delete(t.Context(), access.Principal{ManageAll: true}, first.ID); err != nil {
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
		_, err := s.Create(t.Context(), access.Principal{ManageAll: true}, bad, nil)
		requireStatus(t, err, 422)
	}
	in.Profile, in.Template, in.SessionMode = "other", "other", "reuse"
	task, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
	if err != nil || task.Profile != "other" || task.Template != "other" {
		t.Fatal(task, err)
	}
	_, err = s.Pool.Exec(t.Context(), `UPDATE schedules SET reusable_session_id=$2,reusable_fingerprint='saved' WHERE id=$1`, task.ID, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []string{`{"profile":null}`, `{"template":null}`, `{"profile":""}`, `{"template":" "}`, `{"profile":"missing"}`} {
		_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(patch))
		requireStatus(t, err, 422)
	}
	task, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"profile":"default"}`))
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
	task, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
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
	out, err := s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"profile":"other"}`))
	if err != nil || out.Profile != "other" || out.Template != "other" || out.Prompt != "concurrent edit" || catalog.calls != 4 {
		t.Fatal(out, err, catalog.calls)
	}
}
