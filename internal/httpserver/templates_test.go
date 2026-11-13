//go:build integration

package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/core"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
)

func TestTemplatePageProxy(t *testing.T) {
	calls := 0
	query := ""
	upstreamStatus := 200
	body := `{"items":[{"id":"00000000-0000-0000-0000-000000000001","name":"sandbox","template_name":"sandbox","tag":null,"description":null,"active":false}],"next_cursor":"opaque-next","prev_cursor":"opaque-prev"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		query = r.URL.RawQuery
		w.WriteHeader(upstreamStatus)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	c, err := core.New(upstream.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(&Server{Core: c, Store: &store.Store{DefaultTemplate: "sandbox"}, Config: config.Config{Auth: config.BrowserAuth{Mode: "api_only"}, PublicAPIKeys: []string{"key"}, MaxRequestBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer key"}
	raw := request(t, h, "GET", "/api/v1/templates?active=false&name_contains=BOX&limit=2&cursor=opaque-input", "", headers, 200)
	for _, param := range []string{"active=false", "name_contains=BOX", "limit=2", "cursor=opaque-input"} {
		if !strings.Contains(query, param) {
			t.Fatal(query)
		}
	}
	for _, value := range []string{`"next_cursor":"opaque-next"`, `"prev_cursor":"opaque-prev"`, `"active":false`, `"is_default":true`} {
		if !strings.Contains(raw.Body.String(), value) {
			t.Fatal(raw.Body.String())
		}
	}
	request(t, h, "GET", "/api/v1/templates?name=team%2Fsandbox%3Av1", "", headers, 200)
	if query != "name=team%2Fsandbox%3Av1" {
		t.Fatal(query)
	}
	settings := request(t, h, "GET", "/api/v1/schedules/settings", "", headers, 200)
	if !strings.Contains(settings.Body.String(), `"default_template":"sandbox"`) {
		t.Fatal(settings.Body.String())
	}
	before := calls
	for _, query := range []string{"active=no", "limit=201", "name=", "name_contains=%20", "name=a&name_contains=a", "name=%00", "cursor=", "limit=1&limit=2"} {
		request(t, h, "GET", "/api/v1/templates?"+query, "", headers, 422)
	}
	if calls != before {
		t.Fatal("invalid queries reached core")
	}
	upstreamStatus = 422
	for _, code := range []string{"invalid_cursor", "validation_error"} {
		body = `{"error":{"code":"` + code + `","message":"SECRET","details":[{"path":["query","cursor"],"code":"invalid_value"},{"path":["query","SECRET"],"code":"invalid_value"},{"path":["query","name"],"code":"SECRET"}]}}`
		raw = request(t, h, "GET", "/api/v1/templates?cursor=invalid", "", headers, 422)
		var envelope struct{ Error schedule.Problem }
		if err := json.Unmarshal(raw.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error.Code != code || len(envelope.Error.Details) != 1 || strings.Join(envelope.Error.Details[0].Path, ".") != "query.cursor" || strings.Contains(raw.Body.String(), "SECRET") {
			t.Fatal(raw.Body.String())
		}
	}
	body = `{"error":{"code":"unexpected","message":"SECRET"}}`
	request(t, h, "GET", "/api/v1/templates", "", headers, 503)
	upstreamStatus = 500
	body = `{"error":{"code":"invalid_cursor"}}`
	request(t, h, "GET", "/api/v1/templates", "", headers, 503)
}
