-- +goose Up
-- Create extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- Enable RLS
ALTER DATABASE vera SET row_security = on;

-- Create workspaces table
CREATE TABLE workspaces (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    owner_user_id UUID NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT workspaces_version_positive CHECK (version > 0)
);

CREATE INDEX idx_workspaces_owner ON workspaces(owner_user_id);

-- Create users table
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    email TEXT,
    persona TEXT NOT NULL DEFAULT 'unspecified' CHECK (persona IN ('fund_manager', 'analyst', 'individual', 'unspecified')),
    invited_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT users_issuer_subject_unique UNIQUE (issuer, subject)
);

CREATE INDEX idx_users_issuer_subject ON users(issuer, subject);
CREATE INDEX idx_users_invited_by ON users(invited_by);

-- Create sessions table (no workspace_id FK yet, will be added after first login flow creates workspace)
CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    session_id_hash TEXT NOT NULL UNIQUE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE,
    csrf_token_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT sessions_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_workspace ON sessions(workspace_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at) WHERE revoked_at IS NULL;

-- Create invites table
CREATE TABLE invites (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    code_hash TEXT NOT NULL UNIQUE,
    quota INT NOT NULL DEFAULT 1 CHECK (quota >= 0),
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT invites_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX idx_invites_expires ON invites(expires_at) WHERE quota > 0;

-- Create audit_events table (append-only, no workspace_id for platform events)
CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    workspace_id UUID,
    actor_user_id UUID,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT,
    result TEXT NOT NULL CHECK (result IN ('success', 'denied', 'error')),
    trace_id TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_workspace_time ON audit_events(workspace_id, occurred_at DESC);
CREATE INDEX idx_audit_actor_time ON audit_events(actor_user_id, occurred_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS workspaces;
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS uuid-ossp;
