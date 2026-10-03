//go:build integration

package httpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

func TestServicesInputContract(t *testing.T) {
	h := fixture(t, "api_only")
	headers := map[string]string{"Authorization": "Bearer test-key"}
	for _, selection := range []string{`null`, `["a","a"]`, `["missing"]`, `["INVALID"]`, `[null]`, `"a"`} {
		t.Run(selection, func(t *testing.T) {
			headers["Idempotency-Key"] = uuid.NewString()
			body := strings.Replace(createBody, `["a"]`, selection, 1)
			request(t, h, "POST", "/api/v1/schedules", body, headers, 422)
			// Rejected input never consumes the idempotency key.
			created := request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201)
			var task schedule.Schedule
			if err := json.Unmarshal(created.Body.Bytes(), &task); err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/schedules/" + task.ID.String()
			request(t, h, "PATCH", path, `{"services":`+selection+`}`, headers, 422)
			got := request(t, h, "GET", path, "", headers, 200)
			if !strings.Contains(got.Body.String(), `"services":["a"]`) {
				t.Fatal("rejected update changed selection", got.Body.String())
			}
			request(t, h, "PATCH", path, `{"env_from":[]}`, headers, 422)
		})
	}
	request(t, h, "POST", "/api/v1/schedules", strings.Replace(createBody, `"services"`, `"env_from"`, 1), headers, 422)
	settings := request(t, h, "GET", "/api/v1/schedules/settings", "", headers, 200)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(settings.Body.Bytes(), &fields); err != nil || len(fields) != 1 || fields["browser_auth"] == nil {
		t.Fatal(fields, err)
	}
}

func TestCatalogRoutesAreRootOperations(t *testing.T) {
	h := fixture(t, "api_only")
	headers := map[string]string{"Authorization": "Bearer test-key"}
	for _, name := range []string{"profiles", "templates", "services"} {
		request(t, h, "GET", "/api/v1/"+name, "", headers, 200)
		// Old names are no longer catalog routes: schedule IDs must be UUIDs.
		response := request(t, h, "GET", "/api/v1/schedules/"+name, "", headers, 422)
		if !strings.Contains(response.Body.String(), `"path":["path","id"]`) {
			t.Fatal(response.Body.String())
		}
	}
}
