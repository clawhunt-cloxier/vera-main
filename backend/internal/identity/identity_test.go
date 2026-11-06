package identity_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity/devoidc"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

func TestLoginOnboardCSRFLogout(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	idp, err := devoidc.New(srv.URL+"/dev-oidc", "vera-web", "dev-secret", srv.URL+"/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool, identity.Options{Issuer: idp.Issuer, ClientID: "vera-web", ClientSecret: "dev-secret", PublicURL: srv.URL})
	mux.Handle("/dev-oidc/", http.StripPrefix("/dev-oidc", idp.Handler()))
	mux.HandleFunc("GET /auth/login", svc.Login)
	mux.HandleFunc("GET /auth/callback", svc.Callback)
	mux.Handle("POST /auth/logout", svc.Require(false)(http.HandlerFunc(svc.Logout)))
	mux.Handle("GET /api/v1/me", svc.Require(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		httpx.JSON(w, r, 200, map[string]any{"user_id": p.UserID, "workspace_id": p.WorkspaceID, "csrf_token": p.CSRF})
	})))
	mux.Handle("POST /api/v1/onboarding", svc.Require(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		var in struct{ InviteCode, Persona string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, err := svc.Onboard(r.Context(), p, in.InviteCode, in.Persona); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(204)
	})))
	mux.Handle("GET /api/v1/private", svc.Require(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	mux.HandleFunc("GET /onboarding", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	// 1. login → dev IdP form
	resp, err := c.Get(srv.URL + "/auth/login?return_to=/app/tasks")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", err, resp)
	}
	form := url.Values{}
	for k, v := range resp.Request.URL.Query() {
		form.Set(k, v[0])
	}
	resp.Body.Close()
	form.Set("username", "alice")
	// 2. submit → callback → /onboarding (no workspace yet)
	resp, err = c.PostForm(resp.Request.URL.String(), form)
	if err != nil || resp.Request.URL.Path != "/onboarding" {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback landed on %s: %v %s", resp.Request.URL, err, b)
	}
	// 3. me works, private API refused before onboarding
	var me struct {
		UserID      string  `json:"user_id"`
		WorkspaceID *string `json:"workspace_id"`
		CSRF        string  `json:"csrf_token"`
	}
	getJSON(t, c, srv.URL+"/api/v1/me", &me)
	if me.WorkspaceID != nil || me.CSRF == "" {
		t.Fatalf("unexpected me %+v", me)
	}
	if r, _ := c.Get(srv.URL + "/api/v1/private"); r.StatusCode != 403 {
		t.Fatalf("private before onboarding: %d", r.StatusCode)
	}
	codes, err := identity.CreateInvites(ctx, pool, 1, 1, time.Hour, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"InviteCode":"` + strings.ToLower(codes[0]) + `","Persona":"analyst"}`
	// 4. CSRF required
	if r := post(t, c, srv.URL+"/api/v1/onboarding", body, "", ""); r != 403 {
		t.Fatalf("missing csrf: %d", r)
	}
	if r := post(t, c, srv.URL+"/api/v1/onboarding", body, me.CSRF, "https://evil.example"); r != 403 {
		t.Fatalf("foreign origin: %d", r)
	}
	if r := post(t, c, srv.URL+"/api/v1/onboarding", `{"InviteCode":"VERA-NOPE","Persona":"analyst"}`, me.CSRF, srv.URL); r != 400 {
		t.Fatalf("bad invite: %d", r)
	}
	if r := post(t, c, srv.URL+"/api/v1/onboarding", body, me.CSRF, srv.URL); r != 204 {
		t.Fatalf("onboard: %d", r)
	}
	if r, _ := c.Get(srv.URL + "/api/v1/private"); r.StatusCode != 204 {
		t.Fatalf("private after onboarding: %d", r.StatusCode)
	}
	// invite is single use
	var remaining int
	_ = pool.QueryRow(ctx, `SELECT remaining FROM invites`).Scan(&remaining)
	if remaining != 0 {
		t.Fatalf("remaining %d", remaining)
	}
	// 5. replayed state is rejected
	var states int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM oidc_logins`).Scan(&states)
	if states != 0 {
		t.Fatalf("login state not consumed")
	}
	// 6. logout revokes
	if r := post(t, c, srv.URL+"/auth/logout", "", me.CSRF, srv.URL); r != 204 {
		t.Fatalf("logout %d", r)
	}
	if r, _ := c.Get(srv.URL + "/api/v1/me"); r.StatusCode != 401 {
		t.Fatalf("me after logout: %d", r.StatusCode)
	}
}

func getJSON(t *testing.T, c *http.Client, u string, v any) {
	t.Helper()
	r, err := c.Get(u)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("GET %s: %v %v", u, err, r.StatusCode)
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, c *http.Client, u, body, csrf, origin string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return r.StatusCode
}
