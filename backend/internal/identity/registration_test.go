package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

// With registration closed, a valid invite must be refused without being consumed.
func TestRegistrationClosedRefusesOnboarding(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	svc := identity.New(pool, identity.Options{Registration: false, Issuer: "http://idp.invalid", ClientID: "vera-web", ClientSecret: "x", PublicURL: "http://localhost"})
	codes, err := identity.CreateInvites(ctx, pool, 1, 1, time.Hour, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Onboard(ctx, &identity.Principal{UserID: uuid.New()}, codes[0], "analyst")
	if err == nil || errs.As(err).Code != "REGISTRATION_CLOSED" {
		t.Fatalf("want REGISTRATION_CLOSED, got %v", err)
	}
	var remaining, workspaces int
	_ = pool.QueryRow(ctx, `SELECT remaining FROM invites`).Scan(&remaining)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM workspaces`).Scan(&workspaces)
	if remaining != 1 || workspaces != 0 {
		t.Fatalf("refused onboarding must not consume the invite or create a workspace: remaining=%d workspaces=%d", remaining, workspaces)
	}
}
