//go:build integration

package httpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/api"
	"github.com/orpheus-agents/orpheus-space/internal/browserauth"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
	coreapi "github.com/orpheus-agents/orpheus/client"
)

func accessFixture(t *testing.T) (http.Handler, *Server, func(*string) map[string]string) {
	t.Helper()
	f := testutil.NewSAML(t, "https://space.test")
	storage := &store.Store{Pool: testutil.Database(t), DefaultProfile: "default", DefaultTemplate: "sandbox", Catalog: testutil.Catalog{}}
	s := &Server{Config: config.Config{Auth: f.Config, Access: config.Access{AdminEmails: []string{"admin@example.com"}}, PublicAPIKeys: []string{"key"}, MaxRequestBytes: 4096}, Store: storage, Core: testutil.Catalog{}}
	h, err := Handler(s)
	if err != nil {
		t.Fatal(err)
	}
	login := func(email *string) map[string]string {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(token))
		if err := db.New(storage.Pool).InsertBrowserSession(t.Context(), db.InsertBrowserSessionParams{TokenHash: hash[:], Subject: uuid.NewString(), DisplayName: "User", Email: email, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return map[string]string{"Cookie": browserauth.CookieName + "=" + token, "Origin": f.Config.PublicURL, "X-Orpheus-CSRF": "1"}
	}
	return h, s, login
}

func readTask(t *testing.T, body []byte) scheduleView {
	t.Helper()
	var task scheduleView
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestScheduleAccessMatrix(t *testing.T) {
	h, _, login := accessFixture(t)
	for _, tc := range []struct {
		name        string
		email       *string
		bearer, all bool
	}{
		{"admin", new("admin@example.com"), false, true},
		{"owner", new("alice@example.com"), false, false},
		{"other", new("bob@example.com"), false, false},
		{"missing email", nil, false, false},
		{"bearer with cookie", nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := login(tc.email)
			if tc.bearer {
				headers["Authorization"] = "Bearer key"
			}
			state := request(t, h, "GET", "/api/v1/auth/session", "", headers, 200)
			var auth api.AuthSession
			if err := json.Unmarshal(state.Body.Bytes(), &auth); err != nil {
				t.Fatal(err)
			}
			if !auth.ReadAccess || auth.CanManageAll != tc.all || auth.WriteAccess != (tc.all || tc.email != nil) {
				t.Fatal(auth)
			}
			for _, owner := range []string{"alice@example.com", "bob@example.com", ""} {
				body := strings.Replace(createBody, `" ALICE@example.com "`, `"`+owner+`"`, 1)
				if owner == "" {
					body = strings.Replace(body, `"owner_email":""`, `"owner_email":null`, 1)
				}
				created := request(t, h, "POST", "/api/v1/schedules", body, map[string]string{"Authorization": "Bearer key"}, 201)
				task := readTask(t, created.Body.Bytes())
				path := "/api/v1/schedules/" + task.ID.String()
				canEdit := tc.all || tc.email != nil && *tc.email == owner
				got := readTask(t, request(t, h, "GET", path, "", headers, 200).Body.Bytes())
				if got.CanEdit != canEdit {
					t.Fatal("can_edit", got)
				}
				listed := request(t, h, "GET", "/api/v1/schedules", "", headers, 200)
				var page struct {
					Items []scheduleView `json:"items"`
				}
				if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					want := tc.all || tc.email != nil && item.OwnerEmail != nil && *tc.email == *item.OwnerEmail
					if item.CanEdit != want {
						t.Fatal("list permissions", item)
					}
				}
				request(t, h, "GET", path+"/occurrences", "", headers, 200)
				request(t, h, "GET", "/api/v1/profiles", "", headers, 200)
				request(t, h, "GET", "/api/v1/templates", "", headers, 200)
				services := request(t, h, "GET", "/api/v1/services", "", headers, 200)
				if !strings.Contains(services.Body.String(), `"code":"orpheus-space"`) {
					t.Fatal("service catalog filtered by role", services.Body.String())
				}
				request(t, h, "POST", "/api/v1/schedules/preview", `{"cron":"0 9 * * *","timezone":"UTC"}`, headers, 200)
				status := 403
				if canEdit {
					status = 200
				}
				for _, patch := range []string{`{"name":"Changed"}`, `{"status":"paused"}`, `{"status":"active"}`, `{"profile":"other"}`, `{"services":["orpheus-space"]}`} {
					w := request(t, h, "PATCH", path, patch, headers, status)
					if !canEdit && !strings.Contains(w.Body.String(), "schedule_forbidden") {
						t.Fatal(w.Body.String())
					}
				}
				request(t, h, "POST", path+"/reset-session", "", headers, status)
				if canEdit {
					status = 204
				}
				request(t, h, "DELETE", path, "", headers, status)
				if canEdit {
					request(t, h, "DELETE", path, "", headers, 204)
					if readTask(t, request(t, h, "GET", path, "", headers, 200).Body.Bytes()).CanEdit {
						t.Fatal("deleted is editable")
					}
				}
			}
		})
	}
}

func TestOwnerAssignmentAndReplay(t *testing.T) {
	h, server, login := accessFixture(t)
	alice, bob, admin := login(new("alice@example.com")), login(new("bob@example.com")), login(new("admin@example.com"))
	noEmail := login(nil)
	base := strings.Replace(createBody, `,"owner_email":" ALICE@example.com "`, "", 1)
	alice["Idempotency-Key"] = uuid.NewString()
	first := readTask(t, request(t, h, "POST", "/api/v1/schedules", base, alice, 201).Body.Bytes())
	if first.OwnerEmail == nil || *first.OwnerEmail != "alice@example.com" || !first.CanEdit {
		t.Fatal(first)
	}
	path := "/api/v1/schedules/" + first.ID.String()
	request(t, h, "POST", "/api/v1/schedules", createBody, alice, 201)
	for _, owner := range []string{`null`, `"bob@example.com"`} {
		body := strings.TrimSuffix(base, "}") + `,"owner_email":` + owner + `}`
		request(t, h, "POST", "/api/v1/schedules", body, alice, 403)
		request(t, h, "PATCH", path, `{"owner_email":`+owner+`}`, alice, 403)
		request(t, h, "POST", "/api/v1/schedules", body, admin, 201)
	}
	request(t, h, "PATCH", path, `{"owner_email":" ALICE@EXAMPLE.COM "}`, alice, 200)
	request(t, h, "PATCH", path, `{"owner_email":"bob@example.com"}`, bob, 403)
	request(t, h, "PATCH", path, `{"can_edit":true}`, bob, 422)
	request(t, h, "PATCH", path, `{"can_manage_all":true}`, bob, 422)
	shared := readTask(t, request(t, h, "POST", "/api/v1/schedules", base, admin, 201).Body.Bytes())
	request(t, h, "PATCH", "/api/v1/schedules/"+shared.ID.String(), `{"owner_email":"alice@example.com"}`, alice, 403)
	request(t, h, "POST", "/api/v1/schedules", base, noEmail, 403)
	request(t, h, "POST", "/api/v1/schedules", createBody, noEmail, 403)
	// A key does not bypass authorization, including a key first used by an admin.
	bob["Idempotency-Key"] = alice["Idempotency-Key"]
	request(t, h, "POST", "/api/v1/schedules", createBody, bob, 403)
	request(t, h, "POST", "/api/v1/schedules", base, bob, 409)
	request(t, h, "PATCH", path, `{"owner_email":"bob@example.com"}`, admin, 200)
	replay := readTask(t, request(t, h, "POST", "/api/v1/schedules", base, alice, 201).Body.Bytes())
	if replay.ID != first.ID || replay.OwnerEmail == nil || *replay.OwnerEmail != "alice@example.com" || replay.CanEdit {
		t.Fatal("stale replay permissions", replay)
	}
	request(t, h, "PATCH", path, `{"name":"Forbidden"}`, alice, 403)
	request(t, h, "PATCH", path, `{"name":"Allowed"}`, bob, 200)
	request(t, h, "DELETE", path, "", admin, 204)
	replay = readTask(t, request(t, h, "POST", "/api/v1/schedules", base, alice, 201).Body.Bytes())
	if replay.CanEdit || replay.DeletedAt != nil {
		t.Fatal(replay)
	}
	// Recomputing the admin set takes effect for an existing session, without login.
	admin["Idempotency-Key"] = uuid.NewString()
	request(t, h, "POST", "/api/v1/schedules", createBody, admin, 201)
	server.Config.Access.AdminEmails = nil
	request(t, h, "POST", "/api/v1/schedules", createBody, admin, 403)
	state := request(t, h, "GET", "/api/v1/auth/session", "", admin, 200)
	var auth api.AuthSession
	if err := json.Unmarshal(state.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	if auth.CanManageAll {
		t.Fatal("stale admin session")
	}
	// Untrusted client identity headers have no effect; invalid Bearer never falls back.
	forged := maps.Clone(noEmail)
	forged["X-User-Email"] = "admin@example.com"
	request(t, h, "POST", "/api/v1/schedules", createBody, forged, 403)
	forged["Authorization"] = "Bearer invalid"
	request(t, h, "GET", path, "", forged, 401)
}

func TestReadOnlyUsersCanReadOccurrenceResults(t *testing.T) {
	h, server, login := accessFixture(t)
	created := request(t, h, "POST", "/api/v1/schedules", createBody, map[string]string{"Authorization": "Bearer key"}, 201)
	task := readTask(t, created.Body.Bytes())
	now := time.Now().UTC()
	c := &resultCore{run: coreapi.Run{ID: uuid.New(), SessionID: uuid.New(), Status: coreapi.RunStatusCompleted, FinalMessage: &coreapi.Message{ID: uuid.New(), Text: "Shared result", CreatedAt: now}}}
	server.Core = c
	q := db.New(server.Store.Pool)
	occ, err := q.InsertOccurrence(t.Context(), db.InsertOccurrenceParams{ID: uuid.New(), ScheduleID: task.ID, ScheduledAt: now, State: "pending", CreatedAt: now, RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := server.Store.Prepare(t.Context(), task.ID, occ.ID, now, func(db.Schedule, db.ScheduleOccurrence, *db.ScheduleOccurrence, time.Time) (store.Snapshot, error) {
		return store.Snapshot{Path: "/api/v1/sessions", Body: []byte(`{}`), Fingerprint: "fixture"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Store.Accept(t.Context(), prepared, c.run.SessionID, c.run.ID, now); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/schedules/" + task.ID.String() + "/occurrences/" + occ.ID.String()
	for _, email := range []*string{new("bob@example.com"), nil} {
		headers := login(email)
		request(t, h, "GET", path, "", headers, 200)
		out := request(t, h, "GET", path+"/result", "", headers, 200)
		if !strings.Contains(out.Body.String(), "Shared result") {
			t.Fatal(out.Body.String())
		}
	}
}
