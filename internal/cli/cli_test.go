package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func invoke(t *testing.T, host, key, body string, args ...string) (int, string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	code := Execute(t.Context(), args, strings.NewReader(body), &out, &errout, func(k string) string {
		if k == "ORPHEUS_SPACE_HOST" {
			return host
		}
		return key
	}, "v-test")
	return code, out.String(), errout.String()
}
func TestCommands(t *testing.T) {
	id, oid := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		args               []string
		method, path, body string
	}{
		{[]string{"get", id}, "GET", "/" + id, ""},
		{[]string{"delete", id}, "DELETE", "/" + id, ""},
		{[]string{"pause", id}, "PATCH", "/" + id, `{"status":"paused"}`},
		{[]string{"resume", id}, "PATCH", "/" + id, `{"status":"active"}`},
		{[]string{"reset-session", id}, "POST", "/" + id + "/reset-session", ""},
		{[]string{"history", id}, "GET", "/" + id + "/occurrences", ""},
		{[]string{"occurrence", id, oid}, "GET", "/" + id + "/occurrences/" + oid, ""},
		{[]string{"result", id, oid}, "GET", "/" + id + "/occurrences/" + oid + "/result", ""},
		{[]string{"settings"}, "GET", "/settings", ""},
		{[]string{"preview", "--cron", "0 10 * * *", "--timezone", "Europe/Moscow"}, "POST", "/preview", `{"cron":"0 10 * * *","timezone":"Europe/Moscow"}`},
		{[]string{"update", id, "--file", "-"}, "PATCH", "/" + id, `{"model":null,"env_from":[],"prompt":"one\ntwo"}`},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != "/api/v1/schedules"+tc.path || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error(r.Method, r.URL.Path, "unexpected request/auth")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != tc.body {
					t.Errorf("body: %s", body)
				}
				if tc.method == "DELETE" {
					w.WriteHeader(204)
				} else {
					_, _ = io.WriteString(w, `{"ok":true}`)
				}
			}))
			defer server.Close()
			args := append([]string{"schedule"}, tc.args...)
			args = append(args, "--json")
			code, out, errout := invoke(t, server.URL, "test-key", tc.body, args...)
			if code != 0 || out != "{\"ok\":true}\n" || errout != "" {
				t.Fatal(code, out, errout)
			}
		})
	}
}
func TestListIsOnePageAndHostOverride(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if !slices.Equal(q["owner_email"], []string{"alice@example.com", "bob@example.com"}) || q.Get("cursor") != "opaque+/=" || q.Get("limit") != "2" || q.Get("status") != "paused" {
			t.Error(q)
		}
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":"next"}`)
	}))
	defer server.Close()
	code, out, errout := invoke(t, "invalid", "key", "", "--host", server.URL, "schedule", "list", "--owner-email", "alice@example.com", "--owner-email", "bob@example.com", "--limit", "2", "--cursor", "opaque+/=", "--status", "paused", "--json")
	if code != 0 || calls != 1 || !strings.Contains(out, `"next_cursor":"next"`) {
		t.Fatal(code, out, errout, calls)
	}
}
func TestCreateFileAndRetryKey(t *testing.T) {
	body := `{"prompt":"one\ntwo","name":"test"}`
	file := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	key := ""
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		got := r.Header.Get("Idempotency-Key")
		if _, err := uuid.Parse(got); err != nil {
			t.Error("missing UUID key")
		}
		if key == "" {
			key = got
		} else if key != got {
			t.Error("retry key changed")
		}
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != body {
			t.Error("body changed")
		}
		if calls == 1 {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"code":"secret-value","message":"secret-value"}}`)
			return
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer server.Close()
	code, out, errout := invoke(t, server.URL, "key", "", "schedule", "create", "--file", file, "--json")
	if code != 1 || out != "" || key == "" || !strings.Contains(errout, key) || strings.Contains(errout, "secret-value") {
		t.Fatal(code, out, errout)
	}
	code, out, errout = invoke(t, server.URL, "key", body, "schedule", "create", "--file", "-", "--idempotency-key", key, "--json")
	if code != 0 || calls != 2 {
		t.Fatal(code, out, errout, calls)
	}
}
func TestRejectsInvalidInputWithoutRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") }))
	defer server.Close()
	for _, args := range [][]string{
		{"get", "bad-id"}, {"get"}, {"get", uuid.Nil.String()},
		{"list", "--limit", "201"}, {"list", "--unowned", "--owner-email", "alice@example.com"},
		{"list", "--status", "bad"}, {"create"}, {"preview"}, {"create", "--file", "-", "--idempotency-key", "bad"},
	} {
		code, out, _ := invoke(t, server.URL, "key", "{}", append([]string{"schedule"}, args...)...)
		if code != 1 || out != "" {
			t.Fatal(args, code, out)
		}
	}
	for _, body := range []string{"null", "[]", "{} {}", "{", strings.Repeat(" ", maxBody+1)} {
		code, out, _ := invoke(t, server.URL, "key", body, "schedule", "create", "--file", "-")
		if code != 1 || out != "" {
			t.Fatal("invalid body accepted")
		}
	}
	code, _, _ := invoke(t, server.URL, "", "", "schedule", "settings")
	if code != 1 {
		t.Fatal("missing key accepted")
	}
	for _, host := range []string{"", server.URL + "/api/v1", server.URL + "?", server.URL + "#frag", "http://user:secret@localhost"} {
		code, _, errout := invoke(t, host, "key", "", "schedule", "settings")
		if code != 1 || strings.Contains(errout, "secret") {
			t.Fatal(code, errout)
		}
	}
}
func TestRedirectAndUnsafeErrors(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	code, out, errout := invoke(t, server.URL, "secret-key", "", "schedule", "settings")
	if code != 1 || out != "" || strings.Contains(errout, "secret-key") || !strings.Contains(errout, "302") {
		t.Fatal(code, out, errout)
	}
}
func TestHelpVersionAndInvalidResponse(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"schedule", "result", "--help"}} {
		code, out, errout := invoke(t, "", "", "", args...)
		if code != 0 || out == "" || errout != "" {
			t.Fatal(code, out, errout)
		}
	}
	for _, raw := range []string{"not json", strings.Repeat("x", 8*maxBody+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, raw) }))
		code, out, errout := invoke(t, server.URL, "key", "", "schedule", "settings")
		server.Close()
		if code != 1 || out != "" || !json.Valid([]byte(errout)) {
			t.Fatal(code, out, errout)
		}
	}
}

func TestHelpDoesNotEchoHostCredentials(t *testing.T) {
	code, out, errout := invoke(t, "https://user:secret@localhost", "secret-key", "", "--help")
	if code != 0 || strings.Contains(out+errout, "secret") {
		t.Fatal("help disclosed configuration")
	}
}

func TestScheduleURLPassthrough(t *testing.T) {
	id := uuid.NewString()
	for _, link := range []string{`"https://public.example.com/schedules/` + id + `"`, `null`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"id":"`+id+`","url":`+link+`}`)
		}))
		for _, compact := range []bool{false, true} {
			args := []string{"schedule", "get", id}
			if compact {
				args = append(args, "--json")
			}
			code, out, errout := invoke(t, server.URL, "test-key", "", args...)
			var result map[string]json.RawMessage
			if code != 0 || errout != "" || json.Unmarshal([]byte(out), &result) != nil || string(result["url"]) != link {
				t.Fatal(code, out, errout)
			}
		}
		server.Close()
	}
}
