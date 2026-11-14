package identity_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity/devoidc"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

type rig struct {
	pool *pgxpool.Pool
	srv  *httptest.Server
	svc  *identity.Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := dbtest.New(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	idp, err := devoidc.New(srv.URL+"/dev-oidc", "vera-web", "dev-secret", srv.URL+"/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	svc := identity.New(pool, identity.Options{Registration: true, Issuer: idp.Issuer, ClientID: "vera-web", ClientSecret: "dev-secret", PublicURL: srv.URL})
	mux.Handle("/dev-oidc/", http.StripPrefix("/dev-oidc", idp.Handler()))
	mux.HandleFunc("GET /auth/login", svc.Login)
	mux.HandleFunc("GET /auth/callback", svc.Callback)
	mux.Handle("GET /api/v1/me", svc.Require(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		httpx.JSON(w, r, 200, map[string]any{"user_id": p.UserID, "workspace_id": p.WorkspaceID, "csrf_token": p.CSRF})
	})))
	mux.Handle("POST /api/v1/echo", svc.Require(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	mux.HandleFunc("GET /onboarding", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	return &rig{pool: pool, srv: srv, svc: svc}
}

// login drives the code flow; tamper may alter the IdP form (nonce, code_challenge) before submit.
// It returns the client and the final response (body drained).
func (g *rig) login(t *testing.T, user string, tamper func(url.Values)) (*http.Client, *http.Response, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(g.srv.URL + "/auth/login")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", err, resp)
	}
	resp.Body.Close()
	form := url.Values{}
	for k, v := range resp.Request.URL.Query() {
		form.Set(k, v[0])
	}
	form.Set("username", user)
	if tamper != nil {
		tamper(form)
	}
	resp, err = c.PostForm(resp.Request.URL.String(), form)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return c, resp, string(b)
}

func TestCallbackRejectsNonceMismatch(t *testing.T) {
	g := newRig(t)
	_, resp, body := g.login(t, "mallory", func(f url.Values) { f.Set("nonce", "attacker-chosen") })
	if resp.StatusCode != 400 || !strings.Contains(body, "AUTH_NONCE_MISMATCH") {
		t.Fatalf("want AUTH_NONCE_MISMATCH, got %d %s", resp.StatusCode, body)
	}
	assertNoSessions(t, g.pool)
}

func TestCallbackRejectsPKCEMismatch(t *testing.T) {
	g := newRig(t)
	// A challenge the client's verifier does not hash to: the IdP refuses the code exchange.
	_, resp, body := g.login(t, "mallory", func(f url.Values) { f.Set("code_challenge", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM") })
	if resp.StatusCode != 400 || !strings.Contains(body, "AUTH_EXCHANGE_FAILED") {
		t.Fatalf("want AUTH_EXCHANGE_FAILED, got %d %s", resp.StatusCode, body)
	}
	assertNoSessions(t, g.pool)
}

func TestSessionExpiryAndCSRFHashOnly(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	c, resp, _ := g.login(t, "alice", nil)
	if resp.Request.URL.Path != "/onboarding" {
		t.Fatalf("landed on %s", resp.Request.URL)
	}
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	getJSON(t, c, g.srv.URL+"/api/v1/me", &me)
	if me.CSRF == "" {
		t.Fatal("no csrf token")
	}
	var stored string
	if err := g.pool.QueryRow(ctx, `SELECT csrf_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == me.CSRF || stored != identity.Hash(me.CSRF) {
		t.Fatalf("sessions must store only the CSRF hash")
	}
	if r := post(t, c, g.srv.URL+"/api/v1/echo", "", me.CSRF+"x", g.srv.URL); r != 403 {
		t.Fatalf("wrong csrf: %d", r)
	}
	if r := post(t, c, g.srv.URL+"/api/v1/echo", "", me.CSRF, g.srv.URL); r != 204 {
		t.Fatalf("valid csrf: %d", r)
	}
	// A second login gets a different token; the first session's token does not work for it.
	c2, _, _ := g.login(t, "alice", nil)
	if r := post(t, c2, g.srv.URL+"/api/v1/echo", "", me.CSRF, g.srv.URL); r != 403 {
		t.Fatalf("csrf token reused across sessions: %d", r)
	}
	if _, err := g.pool.Exec(ctx, `UPDATE sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if r, _ := c.Get(g.srv.URL + "/api/v1/me"); r.StatusCode != 401 {
		t.Fatalf("expired session: %d", r.StatusCode)
	}
}

func TestOnboardRejectsExpiredAndRevokedInvites(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	c, _, _ := g.login(t, "bob", nil)
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	getJSON(t, c, g.srv.URL+"/api/v1/me", &me)
	mux := http.NewServeMux()
	mux.Handle("POST /onboarding", g.svc.Require(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		if _, err := g.svc.Onboard(r.Context(), p, r.URL.Query().Get("code"), "analyst"); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(204)
	})))
	onboard := func(code string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/onboarding?code="+url.QueryEscape(code), nil)
		u, _ := url.Parse(g.srv.URL)
		for _, ck := range c.Jar.Cookies(u) {
			req.AddCookie(ck)
		}
		req.Header.Set("X-CSRF-Token", me.CSRF)
		req.Header.Set("Origin", g.srv.URL)
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	expired, err := identity.CreateInvites(ctx, g.pool, 1, 1, time.Hour, "expired", "test")
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := identity.CreateInvites(ctx, g.pool, 1, 1, time.Hour, "revoked", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.pool.Exec(ctx, `UPDATE invites SET expires_at = now() - interval '1 minute' WHERE note='expired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.pool.Exec(ctx, `UPDATE invites SET revoked_at = now() WHERE note='revoked'`); err != nil {
		t.Fatal(err)
	}
	for name, code := range map[string]string{"expired": expired[0], "revoked": revoked[0]} {
		if got := onboard(code); got != 400 {
			t.Fatalf("%s invite: %d", name, got)
		}
	}
	var ws int
	_ = g.pool.QueryRow(ctx, `SELECT count(*) FROM workspaces`).Scan(&ws)
	if ws != 0 {
		t.Fatalf("workspace created from invalid invite")
	}
	var used int
	_ = g.pool.QueryRow(ctx, `SELECT count(*) FROM invites WHERE remaining <> 1`).Scan(&used)
	if used != 0 {
		t.Fatalf("invalid invite consumed")
	}
}

func assertNoSessions(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d sessions created by a rejected login", n)
	}
}
