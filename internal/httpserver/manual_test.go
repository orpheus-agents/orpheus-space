//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/client"
	"github.com/orpheus-agents/orpheus-space/internal/access"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

func TestManualRunContractAndClient(t *testing.T) {
	h := fixture(t, "anonymous")
	headers := map[string]string{"Origin": "http://space.test", "X-Orpheus-CSRF": "1"}
	created := request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201)
	task := readTask(t, created.Body.Bytes())
	path := "/api/v1/schedules/" + task.ID.String() + "/run"
	for _, key := range []string{"", "invalid", uuid.Nil.String()} {
		invalidHeaders := maps.Clone(headers)
		if key != "" {
			invalidHeaders["Idempotency-Key"] = key
		}
		response := request(t, h, "POST", path, "", invalidHeaders, 422)
		var problem struct{ Error schedule.Problem }
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || len(problem.Error.Details) != 1 {
			t.Fatal(response.Body.String(), err)
		}
	}
	headers["Idempotency-Key"] = uuid.NewString()
	request(t, h, "POST", path, `{"model":"override"}`, headers, 422)
	request(t, h, "POST", path, "", map[string]string{"Idempotency-Key": headers["Idempotency-Key"]}, 403)
	request(t, h, "POST", path+"?model=override", "", headers, 422)
	server := httptest.NewServer(h)
	defer server.Close()
	c, err := client.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.RunScheduleWithResponse(t.Context(), task.ID, &client.RunScheduleParams{IdempotencyKey: uuid.MustParse(headers["Idempotency-Key"])}, func(_ context.Context, r *http.Request) error {
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		return nil
	})
	if err != nil || result.JSON202 == nil || result.JSON202.Trigger != client.Manual || result.JSON202.State != client.Pending {
		t.Fatal(result, err)
	}
	replayed := request(t, h, "POST", path, "", headers, 202)
	var occ schedule.Occurrence
	if err = json.Unmarshal(replayed.Body.Bytes(), &occ); err != nil || occ.ID != result.JSON202.ID {
		t.Fatal(occ, err)
	}
	headers["Idempotency-Key"] = uuid.NewString()
	request(t, h, "POST", path, "", headers, 409)
	card := request(t, h, "GET", "/api/v1/schedules/"+task.ID.String(), "", headers, 200)
	got := readTask(t, card.Body.Bytes())
	if got.LastOccurrence == nil || got.LastOccurrence.Trigger != "manual" || !got.NextRunAt.Equal(*task.NextRunAt) {
		t.Fatal(got)
	}
}

func TestManualRunAccessMatrix(t *testing.T) {
	h, server, login := accessFixture(t)
	for _, tc := range []struct {
		name   string
		email  *string
		bearer bool
	}{
		{"admin", new("admin@example.com"), false},
		{"owner", new("alice@example.com"), false},
		{"other", new("bob@example.com"), false},
		{"without email", nil, false},
		{"bearer", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := login(tc.email)
			if tc.bearer {
				headers["Authorization"] = "Bearer key"
			}
			for _, owner := range []*string{new("alice@example.com"), nil} {
				in := schedule.Defaults()
				in.Name, in.Prompt, in.Cron, in.Timezone, in.Status = "Paused", "Prompt", "* * * * *", "UTC", "paused"
				in.OwnerEmail = owner
				task, err := server.Store.Create(t.Context(), access.Principal{ManageAll: true}, in, nil)
				if err != nil {
					t.Fatal(err)
				}
				headers["Idempotency-Key"] = uuid.NewString()
				status := 403
				if tc.bearer || tc.name == "admin" || tc.name == "owner" && owner != nil {
					status = 202
				}
				request(t, h, "POST", "/api/v1/schedules/"+task.ID.String()+"/run", "", headers, status)
			}
		})
	}
}
