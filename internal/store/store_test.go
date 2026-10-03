//go:build integration

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func fixture(t *testing.T) (*Store, schedule.Input) {
	t.Helper()
	s := &Store{DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}, Pool: testutil.Database(t), Now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }}
	in := schedule.Defaults()
	in.Name = "Report"
	in.Prompt = "Summarize incidents"
	in.Cron = "0 10 * * *"
	in.Timezone = "Europe/Moscow"
	return s, in
}
func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	e, ok := errors.AsType[*schedule.Error](err)
	if !ok || e.Status != status {
		t.Fatalf("want %d got %v", status, err)
	}
}
func TestIdempotencyAndTombstones(t *testing.T) {
	s, in := fixture(t)
	key := uuid.New()
	in.OwnerEmail = new(" ALICE@example.com ")
	first, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	if err != nil {
		t.Fatal(err)
	}
	in.OwnerEmail = new("alice@example.com")
	replay, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	if err != nil || first.ID != replay.ID {
		t.Fatal(replay, err)
	}
	in.Name = "Other"
	_, err = s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	requireStatus(t, err, 409)
	in.Name = "Report"
	for range 2 {
		if err = s.Delete(t.Context(), access.Principal{ManageAll: true}, first.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(t.Context(), first.ID)
	if err != nil || got.DeletedAt == nil || got.NextRunAt != nil {
		t.Fatal(got, err)
	}
	page, err := s.List(t.Context(), Filter{}, 50, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, first.ID, []byte(`{"name":"No"}`))
	requireStatus(t, err, 409)
	replay, err = s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
	if err != nil || replay.DeletedAt != nil || replay.ID != first.ID {
		t.Fatal(replay, err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatal("replay did not preserve original response")
	}
	_, err = s.Get(t.Context(), uuid.New())
	requireStatus(t, err, 404)
}
func TestConcurrentCreateAndPatch(t *testing.T) {
	s, in := fixture(t)
	key := uuid.New()
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 8)
	for range 8 {
		wg.Go(func() {
			out, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, &key)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- out.ID
		})
	}
	wg.Wait()
	close(ids)
	var id uuid.UUID
	for got := range ids {
		if id != uuid.Nil && got != id {
			t.Fatal("duplicate creation")
		}
		id = got
	}
	patches := []string{`{"name":"New name"}`, `{"owner_email":"bob@example.com"}`}
	for _, patch := range patches {
		wg.Go(func() {
			if _, err := s.Update(t.Context(), access.Principal{ManageAll: true}, id, []byte(patch)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := s.Get(t.Context(), id)
	if err != nil || got.Name != "New name" || got.OwnerEmail == nil || *got.OwnerEmail != "bob@example.com" {
		t.Fatal(got, err)
	}
}
func TestPauseResumeAndPartialChanges(t *testing.T) {
	s, in := fixture(t)
	in.Model = new("model")
	in.OwnerEmail = new("alice@example.com")
	in.Services = []string{"a"}
	created, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(48 * time.Hour)
	s.Now = func() time.Time { return now }
	updated, err := s.Update(t.Context(), access.Principal{ManageAll: true}, created.ID, []byte(`{"name":"Renamed","model":null,"owner_email":null,"services":[]}`))
	if err != nil || updated.Model != nil || updated.OwnerEmail != nil || len(updated.Services) != 0 || !updated.NextRunAt.Equal(*created.NextRunAt) {
		t.Fatal(updated, err)
	}
	paused, err := s.Update(t.Context(), access.Principal{ManageAll: true}, created.ID, []byte(`{"status":"paused"}`))
	if err != nil || paused.NextRunAt != nil {
		t.Fatal(paused, err)
	}
	resumed, err := s.Update(t.Context(), access.Principal{ManageAll: true}, created.ID, []byte(`{"status":"active"}`))
	if err != nil || !resumed.NextRunAt.After(now) {
		t.Fatal(resumed, err)
	}
	zone, err := s.Update(t.Context(), access.Principal{ManageAll: true}, created.ID, []byte(`{"timezone":"UTC"}`))
	if err != nil || zone.NextRunAt.Hour() != 10 {
		t.Fatal(zone, err)
	}
	for _, patch := range []string{`{"name":""}`, `{"services":["missing"]}`, `{"services":["a","a"]}`, `{"cron":"bad"}`} {
		_, err = s.Update(t.Context(), access.Principal{ManageAll: true}, created.ID, []byte(patch))
		requireStatus(t, err, 422)
	}
}
func TestCursorAndFilters(t *testing.T) {
	s, in := fixture(t)
	for i := range 6 {
		in.Name = fmt.Sprintf("Report %d", i)
		in.OwnerEmail = new(fmt.Sprintf("%d@example.com", i%2))
		if i == 0 {
			in.OwnerEmail = nil
		}
		if _, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List(t.Context(), Filter{}, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.List(t.Context(), Filter{}, 2, "")
	if err != nil || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	added, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := []uuid.UUID{first.Items[0].ID, first.Items[1].ID}
	token := first.NextCursor
	for token != nil {
		page, err := s.List(t.Context(), Filter{}, 2, *token)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		token = page.NextCursor
	}
	want := []uuid.UUID{}
	for _, item := range all.Items {
		want = append(want, item.ID)
	}
	if !slices.Equal(seen, want) || slices.Contains(seen, added.ID) {
		t.Fatal("cursor traversal changed", seen, want)
	}
	_, err = s.List(t.Context(), Filter{Status: "active"}, 2, *first.NextCursor)
	requireStatus(t, err, 422)
	_, err = s.List(t.Context(), Filter{}, 2, "bad")
	requireStatus(t, err, 422)
	page, err := s.List(t.Context(), Filter{Owners: []string{"0@EXAMPLE.COM", "1@example.com"}}, 200, "")
	if err != nil || len(page.Items) != 6 {
		t.Fatal(page, err)
	}
	page, err = s.List(t.Context(), Filter{Unowned: true}, 200, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	_, err = s.List(t.Context(), Filter{Owners: []string{"a@example.com"}, Unowned: true}, 2, "")
	requireStatus(t, err, 422)
}

func TestCursorWithManyLongOwners(t *testing.T) {
	s, in := fixture(t)
	owners := make([]string, 100)
	for i := range owners {
		owners[i] = fmt.Sprintf("%060d@example.com", i)
	}
	in.OwnerEmail = &owners[0]
	for range 3 {
		if _, err := s.Create(t.Context(), access.Principal{ManageAll: true}, in, nil); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.List(t.Context(), Filter{Owners: owners}, 1, "")
	if err != nil || first.NextCursor == nil {
		t.Fatal(first, err)
	}
	if len(*first.NextCursor) > 512 {
		t.Fatal("cursor grows with filters", len(*first.NextCursor))
	}
	slices.Reverse(owners)
	second, err := s.List(t.Context(), Filter{Owners: owners}, 1, *first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatal(second, err)
	}
	owners[0] = "changed@example.com"
	_, err = s.List(t.Context(), Filter{Owners: owners}, 1, *first.NextCursor)
	requireStatus(t, err, 422)
}
