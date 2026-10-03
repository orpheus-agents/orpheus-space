package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRootCatalogCommands(t *testing.T) {
	for _, name := range []string{"profiles", "templates", "services"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/api/v1/"+name || r.Header.Get("Authorization") != "Bearer key" {
					t.Error("unexpected catalog request")
				}
				_, _ = io.WriteString(w, `{"items":[]}`)
			}))
			defer server.Close()
			code, out, errout := invoke(t, server.URL, "key", "", name, "--json")
			if code != 0 || out != "{\"items\":[]}\n" || errout != "" || calls != 1 {
				t.Fatal(code, out, errout, calls)
			}
			code, _, _ = invoke(t, server.URL, "key", "", "schedule", name)
			if code != 1 || calls != 1 {
				t.Fatal("legacy nested catalog command accepted", code, calls)
			}
		})
	}
}
