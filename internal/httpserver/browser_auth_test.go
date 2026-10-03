//go:build integration

package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/google/uuid"
	"github.com/orpheus-agents/orpheus-space/internal/browserauth"
	"github.com/orpheus-agents/orpheus-space/internal/config"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
	"github.com/orpheus-agents/orpheus-space/internal/store"
	"github.com/orpheus-agents/orpheus-space/internal/store/db"
	"github.com/orpheus-agents/orpheus-space/internal/testutil"
)

func TestSAMLBrowserCRUDAndIsolation(t *testing.T) {
	pool := testutil.Database(t)
	f := testutil.NewSAML(t, "https://space.test")
	cfg := config.Config{Execution: testutil.Execution(), Auth: f.Config, PublicAPIKeys: []string{"key"}, AllowedEnv: []string{"A"}, MaxRequestBytes: 4096}
	h, err := Handler(&Server{Core: testutil.Catalog{}, Config: cfg, Store: &store.Store{Pool: pool, AllowedEnv: cfg.AllowedEnv, DefaultProfile: cfg.Execution.Agent.Profile, DefaultTemplate: cfg.Execution.Sandbox.Template, Catalog: testutil.Catalog{}}})
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "GET", "/api/v1/schedules", "", nil, 401)
	metadata := httptest.NewRecorder()
	h.ServeHTTP(metadata, httptest.NewRequest("GET", "/saml/metadata", nil))
	if metadata.Code != 200 || metadata.Header().Get("Content-Type") != "application/samlmetadata+xml" {
		t.Fatal(metadata.Code)
	}
	// Real signed callback goes through generated routes and writes a shared DB session.
	login := request(t, h, "GET", "/auth/login?next=/schedules", "", nil, 302)
	target, _ := url.Parse(login.Header().Get("Location"))
	relay := target.Query().Get("RelayState")
	var id string
	if err := pool.QueryRow(t.Context(), "SELECT request_id FROM browser_login_requests WHERE id=$1", relay).Scan(&id); err != nil {
		t.Fatal(err)
	}
	nonce := login.Result().Cookies()[0]
	form := url.Values{"RelayState": {relay}, "SAMLResponse": {f.Response(t, id, func(root *etree.Element) {
		attribute := root.FindElement("./Assertion/AttributeStatement").CreateElement("saml:Attribute")
		attribute.CreateAttr("Name", "email")
		attribute.CreateElement("saml:AttributeValue").SetText(" ALICE@example.com ")
	})}}.Encode()
	callback := request(t, h, "POST", "/auth/callback", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Cookie": nonce.String()}, 303)
	var cookie *http.Cookie
	for _, c := range callback.Result().Cookies() {
		if c.Name == browserauth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Path != "/" || cookie.Domain != "" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal(cookie)
	}
	headers := map[string]string{"Cookie": cookie.String(), "Origin": cfg.Auth.PublicURL, "X-Orpheus-CSRF": "1", "Idempotency-Key": uuid.NewString()}
	state := request(t, h, "GET", "/api/v1/auth/session", "", headers, 200)
	var auth browserauth.State
	if err := json.Unmarshal(state.Body.Bytes(), &auth); err != nil || !auth.WriteAccess || !auth.ReadAccess || auth.User.Subject == "" {
		t.Fatal(auth, err)
	}
	created := request(t, h, "POST", "/api/v1/schedules", createBody, headers, 201)
	var task schedule.Schedule
	if err := json.Unmarshal(created.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/schedules/" + task.ID.String()
	request(t, h, "PATCH", path, `{"status":"paused"}`, headers, 200)
	request(t, h, "GET", path+"/occurrences", "", headers, 200)
	request(t, h, "POST", path+"/reset-session", "", headers, 200)
	// Bearer bypasses CSRF; invalid bearer cannot fall back to a valid cookie.
	request(t, h, "PATCH", path, `{"status":"active"}`, map[string]string{"Authorization": "Bearer key"}, 200)
	for _, value := range []string{"", "Bearer invalid"} {
		request(t, h, "GET", path, "", map[string]string{"Cookie": cookie.String(), "Authorization": value}, 401)
	}
	request(t, h, "GET", path, "", map[string]string{"Cookie": "__Host-orpheus_session=" + cookie.Value}, 401)
	request(t, h, "DELETE", path, "", map[string]string{"Cookie": cookie.String()}, 403)
	request(t, h, "DELETE", path, "", map[string]string{"Cookie": cookie.String(), "Origin": "https://other.test", "X-Orpheus-CSRF": "1"}, 403)
	request(t, h, "DELETE", path, "", headers, 204)
	request(t, h, "POST", "/auth/logout", "", headers, 204)
	request(t, h, "GET", path, "", headers, 401)
	request(t, h, "POST", "/auth/callback", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Cookie": nonce.String()}, 400)
	request(t, h, "GET", "/api/v1/auth/session", "", headers, 200)
	// DB failures must remain retriable errors; do not clear a potentially valid cookie.
	pool.Close()
	for _, path := range []string{"/api/v1/auth/session", "/api/v1/schedules"} {
		w := request(t, h, "GET", path, "", headers, 503)
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("cleared cookie during storage failure")
		}
	}
}

func TestBrowserExpiryAndDuplicateCSRF(t *testing.T) {
	pool := testutil.Database(t)
	f := testutil.NewSAML(t, "https://space.test")
	h, err := Handler(&Server{Config: config.Config{Auth: f.Config}, Store: &store.Store{Pool: pool}})
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	q := db.New(pool)
	if err := q.InsertBrowserSession(t.Context(), db.InsertBrowserSessionParams{TokenHash: hash[:], Subject: "subject", DisplayName: "User", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"Origin", "X-Orpheus-CSRF"} {
		r := httptest.NewRequest("POST", "/auth/logout", nil)
		r.AddCookie(&http.Cookie{Name: browserauth.CookieName, Value: token})
		r.Header.Set("Origin", f.Config.PublicURL)
		r.Header.Set("X-Orpheus-CSRF", "1")
		r.Header.Add(header, r.Header.Get(header))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if _, err := pool.Exec(t.Context(), "UPDATE browser_sessions SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	request(t, h, "GET", "/api/v1/schedules", "", map[string]string{"Cookie": browserauth.CookieName + "=" + token}, 401)
	w := request(t, h, "GET", "/api/v1/auth/session", "", map[string]string{"Cookie": browserauth.CookieName + "=" + token}, 200)
	if len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("expired cookie not cleared")
	}
}
