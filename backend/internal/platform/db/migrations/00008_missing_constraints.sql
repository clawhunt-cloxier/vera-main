-- +goose Up
-- Add missing foreign key constraints and indexes that were documented but not implemented

-- Ensure watchlist_alerts table exists (mentioned in gaps but missing)
CREATE TABLE IF NOT EXISTS watchlist_alerts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    item_id uuid NOT NULL REFERENCES watchlist_items(id) ON DELETE CASCADE,
    alert_type text NOT NULL CHECK (alert_type IN ('price_change','volume_spike','news','research_ready')),
    threshold jsonb NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    last_triggered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (item_id, alert_type)
);
CREATE INDEX watchlist_alerts_ws ON watchlist_alerts (workspace_id, enabled);

-- Add composite foreign key from model_calls to task_steps
-- First add epoch column to model_calls to support the composite key
ALTER TABLE model_calls ADD COLUMN epoch bigint NOT NULL DEFAULT 1;

-- This ensures model calls reference valid steps (task_id + epoch + step_key)
-- Deferred to allow step creation after calls in same transaction
ALTER TABLE model_calls
  ADD CONSTRAINT model_calls_step_fk
  FOREIGN KEY (task_id, epoch, step_key)
  REFERENCES task_steps(task_id, epoch, step_key)
  DEFERRABLE INITIALLY DEFERRED;

-- Add missing index on task_events for efficient epoch filtering
CREATE INDEX task_events_epoch ON task_events (task_id, epoch, seq);

-- Add missing index on claims for section lookup
CREATE INDEX claims_section ON claims (task_id, section_id, claim_ref);

-- Add index for efficient RLS policy checks on sessions
CREATE INDEX sessions_workspace ON sessions (workspace_id, expires_at) WHERE revoked_at IS NULL;

-- Add index for OIDC cleanup of expired states
CREATE INDEX oidc_logins_expires ON oidc_logins (expires_at);

-- Add index for capability queries by dataset
CREATE INDEX capabilities_dataset ON capabilities (dataset_id, entity_id);

-- +goose Down
DROP INDEX IF EXISTS capabilities_dataset;
DROP INDEX IF EXISTS oidc_logins_expires;
DROP INDEX IF EXISTS sessions_workspace;
DROP INDEX IF EXISTS claims_section;
DROP INDEX IF EXISTS task_events_epoch;
DROP INDEX IF EXISTS watchlist_alerts_ws;
ALTER TABLE model_calls DROP CONSTRAINT IF EXISTS model_calls_step_fk;
ALTER TABLE model_calls DROP COLUMN IF EXISTS epoch;
DROP TABLE IF EXISTS watchlist_alerts;
