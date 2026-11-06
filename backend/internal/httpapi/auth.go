package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type AuthHandler struct {
	oauth2Config *oauth2.Config
	verifier     *oidc.IDTokenVerifier
	// TODO: add identity service
}

func NewAuthHandler(issuer, clientID, clientSecret, redirectURL string) (*AuthHandler, error) {
	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("create OIDC provider: %w", err)
	}

	oauth2Config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})

	return &AuthHandler{
		oauth2Config: oauth2Config,
		verifier:     verifier,
	}, nil
}

func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomString(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	nonce, err := generateRandomString(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// TODO: store state and nonce in session or signed cookie
	
	url := h.oauth2Config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("code_challenge", generatePKCEChallenge()),
	)

	http.Redirect(w, r, url, http.StatusFound)
}

func (h *AuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	// TODO: verify state and nonce
	// TODO: exchange code for token
	// TODO: verify ID token
	// TODO: create or update user
	// TODO: create session
	// TODO: set session cookie
	http.Error(w, "Not implemented", http.StatusNotImplemented)
}

func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	// TODO: verify CSRF
	// TODO: revoke session
	// TODO: clear cookie
	http.Error(w, "Not implemented", http.StatusNotImplemented)
}

func generateRandomString(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b)[:length], nil
}

func generatePKCEChallenge() string {
	// Simplified; real implementation should store verifier
	return "placeholder_challenge"
}
