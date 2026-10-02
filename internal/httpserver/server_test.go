//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orpheus-agents/orpheus-space/internal/core"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	coreapi "github.com/orpheus-agents/orpheus/client"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/client"
	"github.com/orpheus-agents/orpheus-space/internal/api"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

const createBody = `{"name":"Report","prompt":"Summarize incidents","cron":"0 10 * * 1-5","timezone":"Europe/Moscow","owner_email":" ALICE@example.com ","model":"custom","env_from":["A"]}`

func fixture(t *testing.T, mode string) http.Handler {
	t.Helper()
	cfg := config.Config{Execution: testutil.Execution(), Auth: config.BrowserAuth{Mode: mode, PublicURL: "http://space.test"}, PublicAPIKeys: []string{"test-key"}, AllowedEnv: []string{"A", "B"}, MaxRequestBytes: 4096}
	cfg.Execution.Sandbox.EnvFrom = []string{"A"}
	s := &store.Store{DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}, Pool: testutil.Database(t), AllowedEnv: cfg.AllowedEnv}
	h, err := Handler(&Server{Core: testutil.Catalog{}, Store: s, Config: cfg, Now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func request(t *testing.T, h http.Handler, method, path, body string, headers map[string]string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response is cacheable")
	}
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	if route, params, err := router.FindRoute(r); err == nil {
		input := &openapi3filter.RequestValidationInput{Request: r, Route: route, PathParams: params}
		validation := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: w.Code, Header: w.Header(), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
		validation.SetBodyBytes(w.Body.Bytes())
		if err := openapi3filter.ValidateResponse(t.Context(), validation); err != nil {
			t.Fatalf("response violates OpenAPI: %v body=%s", err, w.Body.String())
		}
	}
	return w
}
func TestCRUDAndGeneratedClient(t *testing.T) {
	h := fixture(t, "api_only")
	headers := map[string]string{"Authorization": "Bearer test-key", "Idempotency-Key": uuid.NewString()}
	first := request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201)
	var created schedule.Schedule
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "active" || created.SessionMode != "new" || created.OwnerEmail == nil || *created.OwnerEmail != "alice@example.com" || created.NextRunAt == nil {
		t.Fatal(created)
	}
	retry := request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201)
	if !bytes.Equal(first.Body.Bytes(), retry.Body.Bytes()) {
		t.Fatal("retry response changed")
	}
	request(t, h, "POST", "/api/v1/schedules", strings.Replace(createBody, "Report", "Different", 1), headers, 409)
	path := "/api/v1/schedules/" + created.ID.String()
	request(t, h, "PATCH", path, `{"model":null,"owner_email":null,"env_from":[],"status":"paused"}`, headers, 200)
	got := request(t, h, "GET", path, "", headers, 200)
	var updated schedule.Schedule
	if err := json.Unmarshal(got.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Model != nil || updated.OwnerEmail != nil || len(updated.EnvFrom) != 0 || updated.NextRunAt != nil {
		t.Fatal(updated)
	}
	request(t, h, "PATCH", path, `{"status":"active"}`, headers, 200)
	server := httptest.NewServer(h)
	defer server.Close()
	apiClient, err := client.NewClientWithResponses(server.URL, client.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer test-key")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := apiClient.GetScheduleWithResponse(t.Context(), created.ID)
	if err != nil || result.JSON200 == nil || result.JSON200.ID != created.ID || !result.JSON200.Model.IsNull() {
		t.Fatal(result, err)
	}
	request(t, h, "GET", "/api/v1/schedules?unowned=true&limit=1", "", headers, 200)
	request(t, h, "GET", "/api/v1/schedules?owner_email=alice%40example.com&owner_email=bob%40example.com", "", headers, 200)
	request(t, h, "GET", "/api/v1/schedules/settings", "", headers, 200)
	request(t, h, "POST", "/api/v1/schedules/preview", `{"cron":"0 10 * * 1-5","timezone":"Europe/Moscow"}`, headers, 200)
	for range 2 {
		request(t, h, "DELETE", path, "", headers, 204)
	}
	deleted := request(t, h, "GET", path, "", headers, 200)
	if err := json.Unmarshal(deleted.Body.Bytes(), &updated); err != nil || updated.DeletedAt == nil {
		t.Fatal(updated, err)
	}
	request(t, h, "PATCH", path, `{"name":"again"}`, headers, 409)
	request(t, h, "GET", "/api/v1/schedules/"+uuid.NewString(), "", headers, 404)
}
func TestAuthorizationAndCSRF(t *testing.T) {
	for _, mode := range []string{"api_only", "anonymous"} {
		t.Run(mode, func(t *testing.T) {
			h := fixture(t, mode)
			status := 401
			if mode == "anonymous" {
				status = 200
			}
			request(t, h, "GET", "/api/v1/schedules", "", nil, status)
			request(t, h, "GET", "/api/v1/auth/session", "", nil, 200)
			for _, auth := range []string{"", "Bearer wrong", "Basic test-key", "Bearer"} {
				request(t, h, "GET", "/api/v1/schedules", "", map[string]string{"Authorization": auth, "Cookie": "__Host-orpheus_session=ignored"}, 401)
			}
			request(t, h, "POST", "/api/v1/schedules", createBody, map[string]string{"Authorization": "Bearer test-key", "Origin": "http://evil.test"}, 201)
			if mode == "anonymous" {
				for _, headers := range []map[string]string{nil, {"Origin": "http://space.test"}, {"Origin": "http://evil.test", "X-Orpheus-CSRF": "1"}, {"Origin": "http://space.test", "X-Orpheus-CSRF": "0"}} {
					request(t, h, "POST", "/api/v1/schedules", createBody, headers, 403)
				}
				request(t, h, "POST", "/api/v1/schedules", createBody, map[string]string{"Origin": "http://space.test", "X-Orpheus-CSRF": "1"}, 201)
			}
		})
	}
}
func TestInvalidInputsDoNotReachStorage(t *testing.T) {
	h, err := Handler(&Server{Config: config.Config{Auth: config.BrowserAuth{Mode: "api_only"}, PublicAPIKeys: []string{"test-key"}, MaxRequestBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	auth := map[string]string{"Authorization": "Bearer test-key"}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/schedules", `null`, 422},
		{"POST", "/api/v1/schedules", `{}`, 422},
		{"POST", "/api/v1/schedules", strings.Replace(createBody, `"Report"`, `null`, 1), 422},
		{"POST", "/api/v1/schedules", strings.Replace(createBody, `"name":"Report"`, `"name":"Report","id":"fake"`, 1), 422},
		{"POST", "/api/v1/schedules", strings.Replace(createBody, `"name":"Report"`, `"name":"Report","name":"Other"`, 1), 422},
		{"PATCH", "/api/v1/schedules/" + uuid.NewString(), `{"status":null}`, 422},
		{"PATCH", "/api/v1/schedules/" + uuid.NewString(), `{"env_from":null}`, 422},
		{"GET", "/api/v1/schedules?status=bad", "", 422},
		{"GET", "/api/v1/schedules?limit=201", "", 422},
		{"GET", "/api/v1/schedules?limit=0", "", 422},
		{"GET", "/api/v1/schedules?limit=1&limit=2", "", 422},
		{"GET", "/api/v1/schedules?unknown=field", "", 422},
		{"GET", "/api/v1/schedules?owner_email=%ZZ", "", 422},
		{"GET", "/api/v1/schedules/not-uuid", "", 422},
		{"POST", "/api/v1/schedules", strings.Repeat("x", 4097), 413},
		{"GET", "/api/v1/missing", "", 404},
	} {
		request(t, h, tc.method, tc.path, tc.body, auth, tc.status)
	}
	request(t, h, "POST", "/api/v1/schedules", createBody, map[string]string{"Authorization": "Bearer test-key", "Idempotency-Key": "bad"}, 422)
	request(t, h, "POST", "/api/v1/schedules", createBody, map[string]string{"Authorization": "Bearer test-key", "Content-Type": "text/plain"}, 415)
}
func TestUnavailableDatabaseDoesNotAffectPreview(t *testing.T) {
	pool := testutil.Database(t)
	pool.Close()
	cfg := config.Config{Execution: testutil.Execution(), Auth: config.BrowserAuth{Mode: "api_only"}, PublicAPIKeys: []string{"test-key"}, MaxRequestBytes: 4096}
	h, err := Handler(&Server{Store: &store.Store{DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}, Pool: pool}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	auth := map[string]string{"Authorization": "Bearer test-key"}
	request(t, h, "GET", "/api/v1/schedules", "", auth, 503)
	request(t, h, "GET", "/api/v1/schedules/settings", "", auth, 200)
	request(t, h, "POST", "/api/v1/schedules/preview", `{"cron":"* * * * *","timezone":"UTC"}`, auth, 200)
	request(t, h, "GET", "/api/v1/auth/session", "", auth, 200)
}

func TestValidationDetails(t *testing.T) {
	h := fixture(t, "api_only")
	for _, tc := range []struct{ name, method, path, body, location, field, code string }{
		{"required", "POST", "/api/v1/schedules", strings.Replace(createBody, `"name":"Report",`, "", 1), "body", "name", "required"},
		{"type", "POST", "/api/v1/schedules", strings.Replace(createBody, `"Report"`, `12`, 1), "body", "name", "invalid_type"},
		{"unknown", "POST", "/api/v1/schedules", strings.Replace(createBody, `"name":`, `"extra":1,"name":`, 1), "body", "extra", "unknown_field"},
		{"length", "POST", "/api/v1/schedules", strings.Replace(createBody, "Report", strings.Repeat("x", 201), 1), "body", "name", "invalid_value"},
		{"empty", "POST", "/api/v1/schedules", strings.Replace(createBody, "Summarize incidents", "", 1), "body", "prompt", "invalid_value"},
		{"enum", "PATCH", "/api/v1/schedules/" + uuid.NewString(), `{"status":"bad"}`, "body", "status", "invalid_value"},
		{"pattern", "POST", "/api/v1/schedules", strings.Replace(createBody, `["A"]`, `["A=B"]`, 1), "body", "env_from", "invalid_value"},
		{"limit", "GET", "/api/v1/schedules?limit=201", "", "query", "limit", "invalid_value"},
		{"status", "GET", "/api/v1/schedules?status=bad", "", "query", "status", "invalid_value"},
		{"email", "GET", "/api/v1/schedules?owner_email=bad", "", "query", "owner_email", "invalid_value"},
		{"cursor", "GET", "/api/v1/schedules?cursor=bad", "", "query", "cursor", "invalid_value"},
		{"conflict", "GET", "/api/v1/schedules?owner_email=a%40example.com&unowned=true", "", "query", "unowned", "invalid_value"},
		{"duplicate", "GET", "/api/v1/schedules?limit=1&limit=2", "", "query", "limit", "invalid_value"},
		{"path", "GET", "/api/v1/schedules/not-uuid", "", "path", "id", "invalid_value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, h, tc.method, tc.path, tc.body, map[string]string{"Authorization": "Bearer test-key"}, 422)
			var envelope struct {
				Error schedule.Problem `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			want := []string{tc.location, tc.field}
			if tc.name == "pattern" {
				want = append(want, "0")
			}
			if len(envelope.Error.Details) != 1 || !slices.Equal(envelope.Error.Details[0].Path, want) || envelope.Error.Details[0].Code != tc.code {
				t.Fatalf("want %v %s: %s", want, tc.code, w.Body.String())
			}
		})
	}
}

type resultCore struct {
	testutil.Catalog
	calls int
	run   coreapi.Run
	err   error
}

func (c *resultCore) Run(_ context.Context, sid, rid uuid.UUID) (coreapi.Run, error) {
	c.calls++
	if sid != c.run.SessionID || rid != c.run.ID {
		return coreapi.Run{}, errors.New("wrong linkage")
	}
	return c.run, c.err
}
func TestHistoryAndExplicitResult(t *testing.T) {
	pool := testutil.Database(t)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	storage := &store.Store{DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}, Pool: pool, Now: func() time.Time { return now }}
	in := schedule.Defaults()
	in.Name = "task"
	in.Prompt = "prompt"
	in.Cron = "* * * * *"
	in.Timezone = "UTC"
	task, err := storage.Create(t.Context(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err = storage.Plan(t.Context(), task.ID, now); err != nil {
		t.Fatal(err)
	}
	history, err := storage.History(t.Context(), task.ID, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	occ := history.Items[0]
	cfg := config.Config{Execution: testutil.Execution(), Auth: config.BrowserAuth{Mode: "api_only"}, PublicAPIKeys: []string{"test-key"}, MaxRequestBytes: 4096}
	c := &resultCore{run: coreapi.Run{ID: uuid.New(), SessionID: uuid.New(), Status: coreapi.RunStatusCompleted, FinalMessage: &coreapi.Message{ID: uuid.New(), Text: "sensitive result", CreatedAt: now}}}
	handler, err := Handler(&Server{Store: storage, Config: cfg, Core: c})
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer test-key"}
	base := "/api/v1/schedules/" + task.ID.String()
	path := base + "/occurrences/" + occ.ID.String()
	request(t, handler, "GET", path+"/result", "", headers, 409)
	request(t, handler, "POST", base+"/reset-session", "", headers, 409)
	prepared, err := storage.Prepare(t.Context(), task.ID, occ.ID, now, func(db.Schedule, db.ScheduleOccurrence, *db.ScheduleOccurrence, time.Time) (store.Snapshot, error) {
		return store.Snapshot{Path: "/api/v1/sessions", Body: []byte(`{}`), Fingerprint: "fixture"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Accept(t.Context(), prepared, c.run.SessionID, c.run.ID, now); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"/api/v1/schedules", base, base + "/occurrences", path} {
		request(t, handler, "GET", url, "", headers, 200)
	}
	if c.calls != 0 {
		t.Fatal("ordinary reads called core")
	}
	before := request(t, handler, "GET", path, "", headers, 200).Body.String()
	result := request(t, handler, "GET", path+"/result", "", headers, 200)
	if c.calls != 1 || !strings.Contains(result.Body.String(), "sensitive result") {
		t.Fatal(c.calls, result.Body.String())
	}
	after := request(t, handler, "GET", path, "", headers, 200).Body.String()
	if before != after || strings.Contains(after, "sensitive result") {
		t.Fatal("result changed local history")
	}
	request(t, handler, "GET", "/api/v1/schedules/"+uuid.NewString()+"/occurrences/"+occ.ID.String()+"/result", "", headers, 404)
	if c.calls != 1 {
		t.Fatal("foreign occurrence called core")
	}
	c.err = &core.Failure{Code: "unauthorized", Status: 401}
	request(t, handler, "GET", path+"/result", "", headers, 503)
	c.err = &core.Failure{Code: "run_not_found", Status: 404}
	request(t, handler, "GET", path+"/result", "", headers, 404)
	request(t, handler, "DELETE", base, "", headers, 204)
	request(t, handler, "GET", base+"/occurrences", "", headers, 200)
}

func TestScheduleURLs(t *testing.T) {
	for _, origin := range []string{"https://space.example.com", "http://localhost:8080", "http://[::1]:8080", ""} {
		t.Run(origin, func(t *testing.T) {
			cfg := config.Config{Execution: testutil.Execution(), Auth: config.BrowserAuth{Mode: "api_only", PublicURL: origin}, PublicAPIKeys: []string{"test-key"}, AllowedEnv: []string{"A"}, MaxRequestBytes: 4096}
			s := &Server{Core: testutil.Catalog{}, Store: &store.Store{DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}, Pool: testutil.Database(t), AllowedEnv: cfg.AllowedEnv}, Config: cfg}
			h, err := Handler(s)
			if err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"Authorization": "Bearer test-key", "Idempotency-Key": uuid.NewString(), "Forwarded": "host=evil.test;proto=https", "X-Forwarded-Host": "evil.test"}
			first := request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201)
			var task client.Schedule
			if err := json.Unmarshal(first.Body.Bytes(), &task); err != nil {
				t.Fatal(err)
			}
			want := ""
			if origin != "" {
				want = origin + "/schedules/" + task.ID.String()
			}
			check := func(w *httptest.ResponseRecorder) {
				t.Helper()
				var got map[string]json.RawMessage
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				raw, ok := got["url"]
				if !ok {
					t.Fatal("missing url")
				}
				var link *string
				if err := json.Unmarshal(raw, &link); err != nil {
					t.Fatal(err)
				}
				if want == "" && link != nil || want != "" && (link == nil || *link != want) {
					t.Fatalf("url=%s want=%q", raw, want)
				}
			}
			check(first)
			path := "/api/v1/schedules/" + task.ID.String()
			check(request(t, h, "GET", path, "", headers, 200))
			check(request(t, h, "PATCH", path, `{"status":"paused"}`, headers, 200))
			check(request(t, h, "POST", path+"/reset-session", "", headers, 200))
			page := request(t, h, "GET", "/api/v1/schedules", "", headers, 200)
			var list struct {
				Items []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(page.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 1 {
				t.Fatal("unexpected list", page.Body.String())
			}
			item := httptest.NewRecorder()
			_, _ = item.Write(list.Items[0])
			check(item)
			request(t, h, "DELETE", path, "", headers, 204)
			check(request(t, h, "GET", path, "", headers, 200))
			// A new public origin applies to old idempotency snapshots as well.
			s.Config.Auth.PublicURL = "https://new.example.com"
			want = s.Config.Auth.PublicURL + "/schedules/" + task.ID.String()
			check(request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201))
		})
	}
}
