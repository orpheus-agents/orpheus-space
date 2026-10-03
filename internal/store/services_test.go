//go:build integration

package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

type serviceCatalog struct {
	testutil.Catalog
	calls  int
	err    error
	items  *coreapi.Services
	before func()
}

func (c *serviceCatalog) Services(ctx context.Context) (coreapi.Services, error) {
	c.calls++
	if c.before != nil {
		c.before()
	}
	if c.err != nil {
		return coreapi.Services{}, c.err
	}
	if c.items != nil {
		return *c.items, nil
	}
	return c.Catalog.Services(ctx)
}

func TestServiceSelectionReplayAndOfflineEdits(t *testing.T) {
	s, in := fixture(t)
	catalog := &serviceCatalog{}
	s.Catalog = catalog
	in.Services = []string{"b", "a"}
	key := uuid.New()
	task, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	if err != nil || !slices.Equal(task.Services, []string{"a", "b"}) || catalog.calls != 1 {
		t.Fatal(task, err, catalog.calls)
	}
	catalog.err = errors.New("offline")
	in.Services = []string{"a", "b"}
	if replay, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key); err != nil || replay.ID != task.ID || catalog.calls != 1 {
		t.Fatal(replay, err, catalog.calls)
	}
	conflicting := in
	conflicting.Services = []string{"a"}
	_, err = s.Create(t.Context(), access.Principal{ManageAll: true}, conflicting, &key)
	requireStatus(t, err, 409)
	if catalog.calls != 1 {
		t.Fatal("conflicting replay queried catalog")
	}
	for _, patch := range []string{`{"name":"Renamed"}`, `{"services":["b","a"]}`, `{"status":"paused"}`} {
		if _, err := s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(patch)); err != nil {
			t.Fatal(patch, err)
		}
	}
	if catalog.calls != 1 {
		t.Fatal("unchanged selection used catalog", catalog.calls)
	}
	_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"services":["a"]}`))
	requireStatus(t, err, 503)
	_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"status":"active"}`))
	requireStatus(t, err, 503)
	before := catalog.calls
	for _, patch := range []string{`{"services":[]}`, `{"status":"active"}`} {
		if _, err := s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(patch)); err != nil {
			t.Fatal(patch, err)
		}
	}
	if catalog.calls != before {
		t.Fatal("empty selection queried catalog")
	}
	catalog.err = nil
	if _, err := s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"services":["a"],"status":"paused"}`)); err != nil {
		t.Fatal(err)
	}
	catalog.items = &coreapi.Services{Items: []coreapi.Service{}}
	_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"status":"active"}`))
	requireStatus(t, err, 422)
	if problem, ok := errors.AsType[*schedule.Error](err); !ok || len(problem.Problem.Details) != 1 || !slices.Equal(problem.Problem.Details[0].Path, []string{"body", "services"}) {
		t.Fatal(err)
	}
	stored, err := s.Get(t.Context(), task.ID)
	if err != nil || stored.Status != "paused" || stored.NextRunAt != nil || !slices.Equal(stored.Services, []string{"a"}) {
		t.Fatal(stored, err)
	}
	if _, err := s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"name":"Still editable","services":["a"]}`)); err != nil {
		t.Fatal(err)
	}
	bad := in
	bad.Services = []string{"missing"}
	badKey := uuid.New()
	_, err = s.Create(t.Context(), access.Principal{ManageAll: true}, bad, &badKey)
	requireStatus(t, err, 422)
	var count int
	if err = s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM schedule_create_keys WHERE key=$1`, badKey).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestServiceChangesResetReuseButPreserveTiming(t *testing.T) {
	s, in := fixture(t)
	in.SessionMode, in.Services = "reuse", []string{"a", "b"}
	task, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	sid := uuid.New()
	if _, err = s.Pool.Exec(t.Context(), `UPDATE schedules SET reusable_session_id=$2,reusable_fingerprint='saved' WHERE id=$1`, task.ID, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"services":["b","a"]}`)); err != nil {
		t.Fatal(err)
	}
	var saved *uuid.UUID
	if err = s.Pool.QueryRow(t.Context(), `SELECT reusable_session_id FROM schedules WHERE id=$1`, task.ID).Scan(&saved); err != nil || saved == nil || *saved != sid {
		t.Fatal(saved, err)
	}
	changed, err := s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"services":["b"]}`))
	if err != nil || changed.NextRunAt == nil || task.NextRunAt == nil || !changed.NextRunAt.Equal(*task.NextRunAt) || changed.Status != task.Status {
		t.Fatal(changed, err)
	}
	var cleared bool
	if err = s.Pool.QueryRow(t.Context(), `SELECT reusable_session_id IS NULL AND reusable_fingerprint IS NULL FROM schedules WHERE id=$1`, task.ID).Scan(&cleared); err != nil || !cleared {
		t.Fatal(cleared, err)
	}
}

func TestServiceActivationRechecksConcurrentSelectionWithoutHoldingLock(t *testing.T) {
	s, in := fixture(t)
	in.Status, in.Services = "paused", []string{"a"}
	task, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &serviceCatalog{}
	s.Catalog = catalog
	catalog.before = func() {
		catalog.before = nil
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := s.Pool.Exec(ctx, `UPDATE schedules SET services=ARRAY['removed'] WHERE id=$1`, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, task.ID, []byte(`{"status":"active"}`))
	requireStatus(t, err, 422)
	stored, err := s.Get(t.Context(), task.ID)
	if err != nil || stored.Status != "paused" || !slices.Equal(stored.Services, []string{"removed"}) || catalog.calls != 2 {
		t.Fatal(stored, err, catalog.calls)
	}
}

func TestServiceAuthorizationBeforeCatalogAndAfterOwnershipChange(t *testing.T) {
	s, in := fixture(t)
	catalog := &serviceCatalog{}
	s.Catalog = catalog
	in.OwnerEmail, in.Services = new("alice@example.com"), []string{"orpheus-space"}
	alice := access.Principal{Email: new("alice@example.com")}
	bob := access.Principal{Email: new("bob@example.com")}
	_, err := s.Create(t.Context(), bob, in, nil)
	requireStatus(t, err, 403)
	if catalog.calls != 0 {
		t.Fatal("denied creation accessed catalog")
	}
	task, err := s.Create(t.Context(), alice, in, nil)
	if err != nil || !slices.Equal(task.Services, []string{"orpheus-space"}) {
		t.Fatal(task, err)
	}
	catalog.calls = 0
	_, err = s.Update(t.Context(), bob, task.ID, []byte(`{"services":["a"]}`))
	requireStatus(t, err, 403)
	if catalog.calls != 0 {
		t.Fatal("denied update accessed catalog")
	}
	catalog.before = func() {
		catalog.before = nil
		if _, err := s.Pool.Exec(t.Context(), `UPDATE schedules SET owner_email='bob@example.com' WHERE id=$1`, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Update(t.Context(), alice, task.ID, []byte(`{"services":["a"]}`))
	requireStatus(t, err, 403)
	stored, err := s.Get(t.Context(), task.ID)
	if err != nil || !slices.Equal(stored.Services, task.Services) || *stored.OwnerEmail != "bob@example.com" {
		t.Fatal(stored, err)
	}
}
