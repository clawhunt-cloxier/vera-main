// Package devoidc is a minimal OpenID Connect issuer for local development and automated tests.
// It implements discovery, JWKS, authorization code with PKCE (S256 only) and RS256 ID tokens.
// It has no passwords: anyone reaching it can log in as any username. config refuses it in production.
package devoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type Server struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURIs []string

	key    *rsa.PrivateKey
	signer jose.Signer
	mu     sync.Mutex
	codes  map[string]grant
}

type grant struct {
	user, nonce, challenge, redirect string
	expires                          time.Time
}

func New(issuer, clientID, secret string, redirects ...string) (*Server, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: k, KeyID: "dev-1", Algorithm: "RS256"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return nil, err
	}
	return &Server{Issuer: strings.TrimRight(issuer, "/"), ClientID: clientID, ClientSecret: secret, RedirectURIs: redirects,
		key: k, signer: sig, codes: map[string]grant{}}, nil
}

// Handler serves paths relative to the issuer (mount with http.StripPrefix).
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	m.HandleFunc("GET /jwks", s.jwks)
	m.HandleFunc("GET /authorize", s.authorizeForm)
	m.HandleFunc("POST /authorize", s.authorize)
	m.HandleFunc("POST /token", s.token)
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"issuer": s.Issuer, "authorization_endpoint": s.Issuer + "/authorize", "token_endpoint": s.Issuer + "/token",
		"jwks_uri": s.Issuer + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	})
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &s.key.PublicKey, KeyID: "dev-1", Algorithm: "RS256", Use: "sig"}}})
}

var form = template.Must(template.New("f").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Vera 开发登录</title>
<style>body{font:15px system-ui;max-width:360px;margin:12vh auto;padding:0 16px}input,button{font:inherit;padding:8px;width:100%;box-sizing:border-box;margin-top:8px}
p{color:#a33}</style>
<h1>Vera 开发登录</h1><p>仅限本地开发与自动化测试，无密码校验。</p>
<form method="post"><label for="u">用户名</label><input id="u" name="username" required pattern="[a-z0-9._\-]{1,40}" autofocus>
{{range $k, $v := .}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}<button type="submit">登录</button></form></html>`))

var userPat = regexp.MustCompile(`^[a-z0-9._-]{1,40}$`)

func (s *Server) checkClient(q url.Values) (string, bool) {
	if q.Get("client_id") != s.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		return "invalid_request", false
	}
	for _, r := range s.RedirectURIs {
		if r == q.Get("redirect_uri") {
			return "", true
		}
	}
	return "invalid_redirect_uri", false
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e, ok := s.checkClient(q); !ok {
		http.Error(w, e, http.StatusBadRequest)
		return
	}
	hidden := map[string]string{}
	for _, k := range []string{"client_id", "response_type", "redirect_uri", "scope", "state", "nonce", "code_challenge", "code_challenge_method"} {
		hidden[k] = q.Get(k)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	_ = form.Execute(w, hidden)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	q := r.PostForm
	if e, ok := s.checkClient(q); !ok {
		http.Error(w, e, http.StatusBadRequest)
		return
	}
	user := q.Get("username")
	if !userPat.MatchString(user) {
		http.Error(w, "invalid username", 400)
		return
	}
	code := rnd()
	s.mu.Lock()
	for k, g := range s.codes {
		if time.Now().After(g.expires) {
			delete(s.codes, k)
		}
	}
	s.codes[code] = grant{user: user, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"),
		expires: time.Now().Add(time.Minute)}
	s.mu.Unlock()
	u, _ := url.Parse(q.Get("redirect_uri"))
	v := u.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func rnd() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	id, sec, ok := r.BasicAuth()
	if !ok {
		id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != s.ClientID || subtle.ConstantTimeCompare([]byte(sec), []byte(s.ClientSecret)) != 1 {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	g, found := s.codes[code]
	delete(s.codes, code) // single use
	s.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || time.Now().After(g.expires) || g.redirect != r.PostForm.Get("redirect_uri") ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": s.Issuer, "sub": "dev:" + g.user, "aud": s.ClientID, "iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(), "nonce": g.nonce, "preferred_username": g.user, "email": g.user + "@dev.invalid"}
	idt, err := jwt.Signed(s.signer).Claims(claims).Serialize()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": rnd(), "token_type": "Bearer", "expires_in": 300, "id_token": idt})
}
