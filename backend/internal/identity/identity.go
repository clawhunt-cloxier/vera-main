// Package identity implements OIDC (authorization code + PKCE) login, server-side sessions,
// CSRF protection and invite-based onboarding (SPEC §2, OPERATIONS §3).
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/audit"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

const (
	CookieName  = "vera_session"
	SessionTTL  = 7 * 24 * time.Hour
	loginTTL    = 10 * time.Minute
	callbackURI = "/auth/callback"
)

type Options struct {
	Issuer         string // must equal the iss claim
	InternalIssuer string // optional base used for server-side fetches (container network)
	ClientID       string
	ClientSecret   string
	PublicURL      string // browser-facing origin of the web app
	CookieSecure   bool
	Registration   bool // VERA_REGISTRATION_ENABLED: new workspaces may be opened with an invite
	HTTPClient     *http.Client
}

type Service struct {
	pool *pgxpool.Pool
	opt  Options

	mu       sync.Mutex
	provider *oidc.Provider
}

func New(pool *pgxpool.Pool, opt Options) *Service {
	if opt.HTTPClient == nil {
		opt.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if opt.InternalIssuer != "" && opt.InternalIssuer != opt.Issuer {
		opt.HTTPClient = &http.Client{Timeout: opt.HTTPClient.Timeout,
			Transport: rewrite{from: opt.Issuer, to: opt.InternalIssuer, next: transportOf(opt.HTTPClient)}}
	}
	return &Service{pool: pool, opt: opt}
}

func transportOf(c *http.Client) http.RoundTripper {
	if c.Transport != nil {
		return c.Transport
	}
	return http.DefaultTransport
}

// rewrite sends requests for the public issuer to an internal address, keeping the Host header public.
type rewrite struct {
	from, to string
	next     http.RoundTripper
}

func (t rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL.String()
	if strings.HasPrefix(u, t.from) {
		nu, err := url.Parse(t.to + strings.TrimPrefix(u, t.from))
		if err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		r.Host = r.URL.Host
		r.URL = nu
	}
	return t.next.RoundTrip(r)
}

func (s *Service) ctx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, s.opt.HTTPClient)
}

// oidcProvider discovers lazily so the API can start before the IdP is reachable.
func (s *Service) oidcProvider(ctx context.Context) (*oidc.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return s.provider, nil
	}
	p, err := oidc.NewProvider(s.ctx(ctx), s.opt.Issuer)
	if err != nil {
		return nil, errs.Unavailable("IDP_UNAVAILABLE", "身份服务暂不可用").Wrap(err)
	}
	s.provider = p
	return p, nil
}

func (s *Service) oauth(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{ClientID: s.opt.ClientID, ClientSecret: s.opt.ClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: s.opt.PublicURL + callbackURI, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
}

func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// safeReturn only allows same-origin relative paths into the app.
func safeReturn(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return "/app"
	}
	return p
}

func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	p, err := s.oidcProvider(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	state, nonce, verifier := Random(24), Random(24), oauth2.GenerateVerifier()
	_, err = s.pool.Exec(r.Context(), `INSERT INTO oidc_logins(state,nonce,code_verifier,return_to,expires_at) VALUES($1,$2,$3,$4,$5)`,
		Hash(state), nonce, verifier, safeReturn(r.URL.Query().Get("return_to")), time.Now().Add(loginTTL))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	_, _ = s.pool.Exec(r.Context(), `DELETE FROM oidc_logins WHERE expires_at < now()`)
	http.Redirect(w, r, s.oauth(p).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func authFail(w http.ResponseWriter, r *http.Request, code string, cause error) {
	httpx.Error(w, r, errs.BadRequest(code, "登录流程无效，请重新登录").Wrap(cause))
}

func (s *Service) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		authFail(w, r, "AUTH_DENIED", errors.New(e))
		return
	}
	var nonce, verifier, returnTo string
	err := s.pool.QueryRow(r.Context(), `DELETE FROM oidc_logins WHERE state=$1 AND expires_at > now() RETURNING nonce, code_verifier, return_to`,
		Hash(q.Get("state"))).Scan(&nonce, &verifier, &returnTo)
	if err != nil {
		authFail(w, r, "AUTH_STATE_INVALID", err)
		return
	}
	p, err := s.oidcProvider(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := s.ctx(r.Context())
	tok, err := s.oauth(p).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		authFail(w, r, "AUTH_EXCHANGE_FAILED", err)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		authFail(w, r, "AUTH_NO_ID_TOKEN", nil)
		return
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: s.opt.ClientID}).Verify(ctx, raw)
	if err != nil {
		authFail(w, r, "AUTH_TOKEN_INVALID", err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		authFail(w, r, "AUTH_NONCE_MISMATCH", nil)
		return
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Pref  string `json:"preferred_username"`
	}
	_ = idt.Claims(&claims)
	if claims.Name == "" {
		claims.Name = claims.Pref
	}
	sid, err := s.startSession(r.Context(), idt.Issuer, idt.Subject, claims.Email, claims.Name)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.setCookie(w, sid.token, SessionTTL)
	if sid.workspace == nil {
		returnTo = "/onboarding"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

type newSession struct {
	token     string
	workspace *uuid.UUID
}

func (s *Service) startSession(ctx context.Context, iss, sub, email, name string) (newSession, error) {
	var out newSession
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var uid uuid.UUID
		var disabled *time.Time
		err := tx.QueryRow(ctx, `INSERT INTO users(issuer,subject,email,display_name) VALUES($1,$2,$3,$4)
			ON CONFLICT (issuer,subject) DO UPDATE SET email=excluded.email, display_name=excluded.display_name
			RETURNING id, disabled_at`, iss, sub, email, name).Scan(&uid, &disabled)
		if err != nil {
			return err
		}
		if disabled != nil {
			_ = audit.Record(ctx, tx, nil, uid.String(), "auth.login", "user:"+uid.String(), "denied", map[string]any{"reason": "disabled"})
			return errs.Forbidden("ACCOUNT_DISABLED", "账号已停用")
		}
		var ws *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM workspaces WHERE owner_user_id=$1`, uid).Scan(&ws); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out.token, out.workspace = Random(32), ws
		if _, err := tx.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,workspace_id,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5)`,
			Hash(out.token), uid, ws, Random(24), time.Now().Add(SessionTTL)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, ws, uid.String(), "auth.login", "user:"+uid.String(), "success", nil)
	})
	return out, err
}

func (s *Service) setCookie(w http.ResponseWriter, v string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: v, Path: "/", HttpOnly: true, Secure: s.opt.CookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
}

// Principal is the authenticated caller.
type Principal struct {
	UserID      uuid.UUID
	WorkspaceID *uuid.UUID
	Persona     string
	CSRF        string
	sessionHash string
}

type ctxKey struct{}

func FromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(*Principal)
	return p, ok
}

// WithPrincipal is exported for tests of downstream handlers.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func (s *Service) principal(r *http.Request) (*Principal, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, errs.Unauthorized()
	}
	p := &Principal{sessionHash: Hash(c.Value)}
	err = s.pool.QueryRow(r.Context(), `SELECT s.user_id, w.id, u.persona, s.csrf_token FROM sessions s
		JOIN users u ON u.id = s.user_id LEFT JOIN workspaces w ON w.owner_user_id = s.user_id
		WHERE s.id_hash=$1 AND s.revoked_at IS NULL AND s.expires_at > now() AND u.disabled_at IS NULL`, p.sessionHash).
		Scan(&p.UserID, &p.WorkspaceID, &p.Persona, &p.CSRF)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.Unauthorized()
	}
	return p, err
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// Require authenticates the session and enforces CSRF + Origin on state-changing requests.
// needWorkspace=false lets not-yet-onboarded users reach /me, onboarding and logout.
func (s *Service) Require(needWorkspace bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := s.principal(r)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			if unsafeMethod(r.Method) {
				if o := r.Header.Get("Origin"); o != "" && o != s.opt.PublicURL {
					httpx.Error(w, r, errs.Forbidden("ORIGIN_MISMATCH", "请求来源无效"))
					return
				}
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRF)) != 1 {
					httpx.Error(w, r, errs.Forbidden("CSRF_INVALID", "页面已过期，请刷新后重试"))
					return
				}
			}
			if needWorkspace && p.WorkspaceID == nil {
				httpx.Error(w, r, errs.Forbidden("ONBOARDING_REQUIRED", "请先完成邀请码兑换"))
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	p, _ := FromContext(r.Context())
	_, err := s.pool.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE id_hash=$1`, p.sessionHash)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.setCookie(w, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}

var Personas = []string{"fund_manager", "analyst", "individual", "unspecified"}

// Onboard redeems an invite and creates the caller's private workspace (idempotent per user).
func (s *Service) Onboard(ctx context.Context, p *Principal, code, persona string) (uuid.UUID, error) {
	if p.WorkspaceID != nil {
		return *p.WorkspaceID, nil
	}
	if !s.opt.Registration {
		return uuid.Nil, errs.Unavailable("REGISTRATION_CLOSED", "注册暂未开放")
	}
	valid := false
	for _, x := range Personas {
		valid = valid || x == persona
	}
	if !valid {
		return uuid.Nil, errs.BadRequest("INVALID_PERSONA", "身份标签无效")
	}
	var ws uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inv uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE invites SET remaining = remaining - 1 WHERE code_hash=$1 AND remaining > 0
			AND revoked_at IS NULL AND expires_at > now() RETURNING id`, Hash(NormalizeInvite(code))).Scan(&inv)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = audit.Record(ctx, tx, nil, p.UserID.String(), "invite.redeem", "", "denied", nil)
			return errs.BadRequest("INVITE_INVALID", "邀请码无效、已用完或已过期")
		}
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id, invite_id) VALUES($1,$2) RETURNING id`, p.UserID, inv).Scan(&ws)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET persona=$2 WHERE id=$1`, p.UserID, persona); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET workspace_id=$2 WHERE user_id=$1 AND revoked_at IS NULL`, p.UserID, ws); err != nil {
			return err
		}
		return audit.Record(ctx, tx, &ws, p.UserID.String(), "invite.redeem", "invite:"+inv.String(), "success", map[string]any{"persona": persona})
	})
	if err != nil {
		return uuid.Nil, err
	}
	p.WorkspaceID, p.Persona = &ws, persona
	return ws, nil
}

// SetPersona updates the experience label; it never grants permissions.
func (s *Service) SetPersona(ctx context.Context, p *Principal, persona string) error {
	for _, x := range Personas {
		if x == persona {
			_, err := s.pool.Exec(ctx, `UPDATE users SET persona=$2 WHERE id=$1`, p.UserID, persona)
			p.Persona = persona
			return err
		}
	}
	return errs.BadRequest("INVALID_PERSONA", "身份标签无效")
}

var inviteEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewInviteCode returns a human-typable code like VERA-ABCD-EFGH-IJKL-MNOP.
func NewInviteCode() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	s := inviteEnc.EncodeToString(b)
	return fmt.Sprintf("VERA-%s-%s-%s-%s", s[0:4], s[4:8], s[8:12], s[12:16])
}

func NormalizeInvite(code string) string {
	return strings.ToUpper(strings.Join(strings.Fields(code), ""))
}

// CreateInvites stores hashes only; plaintext codes are returned once.
func CreateInvites(ctx context.Context, pool *pgxpool.Pool, count, uses int, ttl time.Duration, note, actor string) ([]string, error) {
	codes := make([]string, count)
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for i := range codes {
			codes[i] = NewInviteCode()
			if _, err := tx.Exec(ctx, `INSERT INTO invites(code_hash,remaining,note,expires_at) VALUES($1,$2,$3,$4)`,
				Hash(codes[i]), uses, note, time.Now().Add(ttl)); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, nil, actor, "invite.create", "", "success", map[string]any{"count": count, "uses": uses, "note": note})
	})
	return codes, err
}
