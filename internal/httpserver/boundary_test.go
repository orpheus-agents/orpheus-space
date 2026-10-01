package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

func TestUnexpectedErrorsLogClassWithoutData(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r := httptest.NewRequest("PATCH", "/api/v1/schedules/private-id?owner_email=secret@example.com", nil)
	r.Pattern = "PATCH /api/v1/schedules/{id}"
	w := httptest.NewRecorder()
	writeError(w, r, fmt.Errorf("sensitive wrapper: %w", &pgconn.PgError{Code: "23514", Message: "secret-value", Detail: "secret-value"}))
	if w.Code != 503 || !strings.Contains(logs.String(), `"sqlstate":"23514"`) || !strings.Contains(logs.String(), `"error_type":"*pgconn.PgError"`) {
		t.Fatal(w.Code, logs.String())
	}
	for _, output := range []string{logs.String(), w.Body.String()} {
		if strings.Contains(output, "secret-value") || strings.Contains(output, "sensitive wrapper") {
			t.Fatal("error data leaked", output)
		}
	}
	if !strings.Contains(logs.String(), `"method":"PATCH"`) || !strings.Contains(logs.String(), `"route":"PATCH /api/v1/schedules/{id}"`) || strings.Contains(logs.String(), "secret@example.com") || strings.Contains(logs.String(), "private-id") {
		t.Fatal(logs.String())
	}
	logs.Reset()
	writeError(httptest.NewRecorder(), r, schedule.Invalid("name"))
	if logs.Len() != 0 {
		t.Fatal("expected validation error logged as unexpected")
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	writeError(httptest.NewRecorder(), r.WithContext(ctx), context.Canceled)
	if logs.Len() != 0 {
		t.Fatal("cancelled request logged", logs.String())
	}
}
