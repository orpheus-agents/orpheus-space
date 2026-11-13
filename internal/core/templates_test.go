package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestTemplateResponseContract(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"items":       []any{map[string]any{"id": uuid.NewString(), "name": "sandbox", "template_name": "sandbox", "tag": nil, "description": nil, "active": false}},
			"next_cursor": nil, "prev_cursor": nil,
		}
	}
	check := func(t *testing.T, body map[string]any, wantError bool) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(body) }))
		t.Cleanup(server.Close)
		client, err := New(server.URL, "key")
		if err != nil {
			t.Fatal(err)
		}
		page, err := client.Templates(t.Context(), nil)
		if wantError {
			if ErrorCode(err) != "core_invalid_response" {
				t.Fatal(page, err)
			}
			return
		}
		if err != nil || page.Items == nil {
			t.Fatal(page, err)
		}
		if len(page.Items) > 0 && page.Items[0].Active {
			t.Fatal("explicit false was lost")
		}
	}
	t.Run("inactive", func(t *testing.T) { check(t, valid(), false) })
	t.Run("empty page", func(t *testing.T) {
		body := valid()
		body["items"] = []any{}
		check(t, body, false)
	})
	t.Run("old response", func(t *testing.T) {
		check(t, map[string]any{"items": []any{map[string]any{"name": "sandbox", "description": nil}}}, true)
	})
	for _, field := range []string{"items", "next_cursor", "prev_cursor"} {
		t.Run("missing "+field, func(t *testing.T) {
			body := valid()
			delete(body, field)
			check(t, body, true)
		})
	}
	for _, field := range []string{"id", "name", "template_name", "tag", "description", "active"} {
		t.Run("missing item "+field, func(t *testing.T) {
			body := valid()
			delete(body["items"].([]any)[0].(map[string]any), field)
			check(t, body, true)
		})
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"zero ID", "id", uuid.Nil.String()}, {"bad ID", "id", "invalid"},
		{"empty name", "name", ""}, {"empty alias", "template_name", ""},
		{"null active", "active", nil}, {"string active", "active", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := valid()
			body["items"].([]any)[0].(map[string]any)[tc.field] = tc.value
			check(t, body, true)
		})
	}
}
