package identity

import (
	"time"

	"github.com/google/uuid"
)

type Persona string

const (
	PersonaFundManager Persona = "fund_manager"
	PersonaAnalyst     Persona = "analyst"
	PersonaIndividual  Persona = "individual"
	PersonaUnspecified Persona = "unspecified"
)

type User struct {
	ID         uuid.UUID
	Issuer     string
	Subject    string
	Email      string
	Persona    Persona
	InvitedBy  *uuid.UUID
	CreatedAt  time.Time
}

type Workspace struct {
	ID          uuid.UUID
	OwnerUserID uuid.UUID
	CreatedAt   time.Time
	Version     int64
}

type Session struct {
	ID              uuid.UUID
	SessionIDHash   string
	UserID          uuid.UUID
	WorkspaceID     *uuid.UUID
	CSRFTokenHash   string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	RevokedAt       *time.Time
}

type Invite struct {
	ID        uuid.UUID
	CodeHash  string
	Quota     int
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	ExpiresAt time.Time
}
