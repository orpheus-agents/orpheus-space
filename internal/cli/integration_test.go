//go:build integration

package cli

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/httpserver"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestCLIWithSpaceAPI(t *testing.T) {
	cfg := config.Config{BrowserAuth: "api_only", PublicAPIKeys: []string{"test-key"}, AllowedEnv: []string{"A"}, MaxRequestBytes: maxBody}
	h, err := httpserver.Handler(&httpserver.Server{Config: cfg, Store: &store.Store{Pool: testutil.Database(t), AllowedEnv: cfg.AllowedEnv}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	run := func(body string, args ...string) string {
		t.Helper()
		code, out, errout := invoke(t, server.URL, "test-key", body, append([]string{"schedule", "--json"}, args...)...)
		if code != 0 {
			t.Fatal(code, errout)
		}
		return out
	}
	body := `{"name":"Task","prompt":"one\ntwo","cron":"* * * * *","timezone":"Europe/Moscow","owner_email":"alice@example.com","env_from":["A"]}`
	key := uuid.NewString()
	raw := run(body, "create", "--file", "-", "--idempotency-key", key)
	if replay := run(body, "create", "--file", "-", "--idempotency-key", key); raw != replay {
		t.Fatal("replay differs")
	}
	var created schedule.Schedule
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ID.String()
	raw = run("", "list", "--owner-email", "alice@example.com")
	if !strings.Contains(raw, id) {
		t.Fatal(raw)
	}
	raw = run("", "list", "--owner-email", "bob@example.com")
	if strings.Contains(raw, id) {
		t.Fatal("filter ignored")
	}
	run("", "pause", id)
	raw = run("", "get", id)
	if !strings.Contains(raw, `"status":"paused"`) || !strings.Contains(raw, `"next_run_at":null`) {
		t.Fatal(raw)
	}
	run("", "resume", id)
	raw = run(`{"model":null,"env_from":[]}`, "update", id, "--file", "-")
	if !strings.Contains(raw, `"env_from":[]`) {
		t.Fatal(raw)
	}
	run("", "reset-session", id)
	run("", "history", id)
	run("", "settings")
	run("", "preview", "--cron", "0 10 * * *", "--timezone", "Europe/Moscow")
	code, out, errout := invoke(t, server.URL, "test-key", "", "schedule", "result", id, uuid.NewString(), "--json")
	if code != 1 || out != "" || !strings.Contains(errout, "occurrence_not_found") {
		t.Fatal(code, out, errout)
	}
	run("", "delete", id)
	raw = run("", "list")
	if strings.Contains(raw, id) {
		t.Fatal("deleted schedule listed")
	}
}
