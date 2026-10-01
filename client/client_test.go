package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestClientKeepsPatchNullAndOmissionDistinct(t *testing.T) {
	id := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/api/v1/schedules/"+id.String() || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["model"]) != "null" {
			t.Errorf("null model lost: %s", body["model"])
		}
		if _, present := body["owner_email"]; present {
			t.Error("omitted owner included")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"schedule_deleted","message":"Deleted","phase":null,"details":[]}}`))
	}))
	defer server.Close()
	client, err := NewClientWithResponses(server.URL, WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer test-key")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	patch := UpdateSchedule{}
	patch.Model.SetNull()
	result, err := client.UpdateScheduleWithResponse(t.Context(), id, patch)
	if err != nil || result.StatusCode() != 409 || result.JSONDefault == nil || result.JSONDefault.Error.Code != "schedule_deleted" {
		t.Fatal(result, err)
	}
}
