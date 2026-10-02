package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

func TestCoreFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
		uncertain  bool
	}{{422, `{"error":{"code":"unknown_profile"}}`, "unknown_profile", false}, {422, `{"error":{"code":"unknown_template"}}`, "unknown_template", false}, {401, `{"error":{"code":"unauthorized","message":"secret"}}`, "unauthorized", false}, {503, `{"error":{"code":"capacity_exhausted"}}`, "capacity_exhausted", false}, {500, `internal secret`, "core_unavailable", true}, {202, `{}`, "core_invalid_response", true}, {202, `broken`, "core_invalid_response", true}, {302, ``, "core_unavailable", true}} {
		t.Run(tc.code+http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing auth")
				}
				w.Header().Set("Location", "http://invalid.example")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(server.URL, "key")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Dispatch(t.Context(), "/api/v1/sessions", []byte(`{}`), uuid.New())
			failure, ok := errors.AsType[*Failure](err)
			if !ok || failure.Code != tc.code || failure.Uncertain != tc.uncertain {
				t.Fatal(err)
			}
		})
	}
}
func TestCoreCancellationAndRunLinkage(t *testing.T) {
	sid, rid := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(coreapi.Run{ID: uuid.New(), SessionID: sid, Status: coreapi.RunStatusCompleted})
	}))
	defer server.Close()
	client, err := New(server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Run(t.Context(), sid, rid); ErrorCode(err) != "core_invalid_response" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err = client.Dispatch(ctx, "/api/v1/sessions", []byte(`{}`), uuid.New()); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestCatalogCancellationAndMalformedResponse(t *testing.T) {
	for _, name := range []string{"profiles", "templates"} {
		t.Run(name, func(t *testing.T) {
			for _, body := range []string{`{}`, `{"items":null}`, `broken`} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
				client, err := New(server.URL, "key")
				if err != nil {
					t.Fatal(err)
				}
				if name == "profiles" {
					_, err = client.Profiles(t.Context())
				} else {
					_, err = client.Templates(t.Context())
				}
				server.Close()
				if ErrorCode(err) != "core_invalid_response" {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer server.Close()
			client, err := New(server.URL, "key")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if name == "profiles" {
				_, err = client.Profiles(ctx)
			} else {
				_, err = client.Templates(ctx)
			}
			if err == nil {
				t.Fatal("canceled catalog succeeded")
			}
		})
	}
}
