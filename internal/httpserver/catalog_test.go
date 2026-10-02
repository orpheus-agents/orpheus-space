//go:build integration

package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orpheus-agents/orpheus-space/internal/api"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/core"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestCatalogProxyAndSelectionContract(t *testing.T) {
	calls, status := 0, 200
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer service-key" || r.Header.Get("Cookie") != "" {
			t.Error("upstream credentials or cookies are incorrect")
		}
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"private upstream details"}}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/profiles":
			_, _ = w.Write([]byte(`{"items":[{"name":"default","description":"Default profile","harness":"codex","model":null,"codex":{},"instructions":"Be concise"}]}`))
		case "/api/v1/templates":
			_, _ = w.Write([]byte(`{"items":[{"name":"sandbox","description":null}]}`))
		default:
			t.Error("unexpected upstream path", r.URL.Path)
		}
	}))
	defer upstream.Close()
	c, err := core.New(upstream.URL, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Auth: config.BrowserAuth{Mode: "api_only"}, PublicAPIKeys: []string{"user-key"}, MaxRequestBytes: 4096}
	s := &Server{Config: cfg, Core: c, Store: &store.Store{Pool: testutil.Database(t), DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: c}}
	h, err := Handler(s)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer user-key", "Cookie": "unrelated=private-browser-value"}
	request(t, h, "GET", "/api/v1/schedules/profiles", "", nil, 401)
	if calls != 0 {
		t.Fatal("unauthorized request reached upstream")
	}
	raw := request(t, h, "GET", "/api/v1/schedules/profiles", "", headers, 200)
	var profiles api.Profiles
	if err = json.Unmarshal(raw.Body.Bytes(), &profiles); err != nil || len(profiles.Items) != 1 || !profiles.Items[0].IsDefault || !profiles.Items[0].Description.IsSpecified() || profiles.Items[0].Description.IsNull() || profiles.Items[0].Instructions != "Be concise" {
		t.Fatal(profiles, err)
	}
	raw = request(t, h, "GET", "/api/v1/schedules/templates", "", headers, 200)
	var templates api.Templates
	if err = json.Unmarshal(raw.Body.Bytes(), &templates); err != nil || !templates.Items[0].IsDefault || !templates.Items[0].Description.IsNull() {
		t.Fatal(templates, err)
	}
	s.Store.DefaultProfile = "removed"
	raw = request(t, h, "GET", "/api/v1/schedules/profiles", "", headers, 200)
	if err = json.Unmarshal(raw.Body.Bytes(), &profiles); err != nil || profiles.Items[0].IsDefault {
		t.Fatal(profiles, err)
	}
	s.Store.DefaultProfile = "default"
	const body = `{"name":"task","prompt":"work","cron":"* * * * *","timezone":"UTC"}`
	created := request(t, h, "POST", "/api/v1/schedules", body, headers, 201)
	var task schedule.Schedule
	if err = json.Unmarshal(created.Body.Bytes(), &task); err != nil || task.Profile != "default" || task.Template != "sandbox" {
		t.Fatal(task, err)
	}
	for _, operation := range []string{"POST", "PATCH"} {
		path := "/api/v1/schedules"
		if operation == "PATCH" {
			path += "/" + task.ID.String()
		}
		for _, field := range []string{"profile", "template"} {
			for _, value := range []string{`null`, `""`, `"unknown"`} {
				input := `{"` + field + `":` + value + `}`
				if operation == "POST" {
					input = strings.TrimSuffix(body, "}") + `,"` + field + `":` + value + `}`
				}
				got := request(t, h, operation, path, input, headers, 422)
				var problem api.Problem
				if err = json.Unmarshal(got.Body.Bytes(), &problem); err != nil || len(problem.Error.Details) != 1 || strings.Join(problem.Error.Details[0].Path, ".") != "body."+field {
					t.Fatal(got.Body.String(), err)
				}
			}
		}
	}
	for _, code := range []int{401, 403, 500} {
		status = code
		for _, name := range []string{"profiles", "templates"} {
			got := request(t, h, "GET", "/api/v1/schedules/"+name, "", headers, 503)
			if !strings.Contains(got.Body.String(), "core_unavailable") || strings.Contains(got.Body.String(), "private upstream") {
				t.Fatal(got.Body.String())
			}
		}
	}
	before := calls
	for _, path := range []string{"/api/v1/schedules", "/api/v1/schedules/settings", "/api/v1/schedules/" + task.ID.String(), "/api/v1/schedules/" + task.ID.String() + "/occurrences"} {
		request(t, h, "GET", path, "", headers, 200)
	}
	request(t, h, "PATCH", "/api/v1/schedules/"+task.ID.String(), `{"status":"paused"}`, headers, 200)
	if calls != before {
		t.Fatal("local reads or pause called Orpheus")
	}
}
