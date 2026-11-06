package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	// TODO: add repository dependencies
}

func NewService() *Service {
	return &Service{}
}

func (s *Service) CreateSession(ctx context.Context, userID uuid.UUID, workspaceID *uuid.UUID) (*Session, string, string, error) {
	sessionID, err := generateRandomString(32)
	if err != nil {
		return nil, "", "", fmt.Errorf("generate session ID: %w", err)
	}
	
	csrfToken, err := generateRandomString(32)
	if err != nil {
		return nil, "", "", fmt.Errorf("generate CSRF token: %w", err)
	}

	sess := &Session{
		ID:            uuid.New(),
		SessionIDHash: hashString(sessionID),
		UserID:        userID,
		WorkspaceID:   workspaceID,
		CSRFTokenHash: hashString(csrfToken),
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}

	// TODO: persist session
	return sess, sessionID, csrfToken, nil
}

func generateRandomString(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b)[:length], nil
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.URLEncoding.EncodeToString(h[:])
}
