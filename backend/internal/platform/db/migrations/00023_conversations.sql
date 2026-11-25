-- +goose Up
CREATE TABLE conversations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    title text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    scope jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '90 days',
    deleted_at timestamptz,
    UNIQUE (workspace_id, id)
);

CREATE INDEX conversations_history ON conversations (workspace_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX conversations_expiration ON conversations (expires_at) WHERE deleted_at IS NULL;

CREATE TABLE conversation_turns (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    client_message_id uuid NOT NULL,
    request_hash text NOT NULL,
    ordinal bigint NOT NULL,
    channel text NOT NULL CHECK (channel = 'web'),
    body text NOT NULL,
    reply_to_turn_id uuid,
    revision bigint NOT NULL DEFAULT 1,
    state text NOT NULL CHECK (state IN ('pending','clarify','confirm','research','reference','unavailable')),
    intent jsonb,
    message text,
    plan_id uuid,
    task_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, plan_id) REFERENCES research_plans(workspace_id, id),
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, client_message_id),
    UNIQUE (workspace_id, conversation_id, ordinal),
    UNIQUE (workspace_id, conversation_id, id),
    FOREIGN KEY (workspace_id, conversation_id, reply_to_turn_id) REFERENCES conversation_turns(workspace_id, conversation_id, id)
);

ALTER TABLE research_plans ADD COLUMN conversation_turn_id uuid;
ALTER TABLE research_plans ADD COLUMN conversation_plan boolean NOT NULL DEFAULT false;
ALTER TABLE research_plans ADD COLUMN conversation_brief boolean NOT NULL DEFAULT false;
ALTER TABLE research_plans ADD FOREIGN KEY (workspace_id, conversation_turn_id)
    REFERENCES conversation_turns(workspace_id, id) ON DELETE SET NULL (conversation_turn_id);
CREATE INDEX research_plans_conversation_turn ON research_plans(workspace_id, conversation_turn_id) WHERE conversation_turn_id IS NOT NULL;

ALTER TABLE conversations ENABLE ROW LEVEL SECURITY;
CREATE POLICY ws_isolation ON conversations USING (workspace_id = current_setting('app.workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.workspace_id', true)::uuid);
ALTER TABLE conversation_turns ENABLE ROW LEVEL SECURITY;
CREATE POLICY ws_isolation ON conversation_turns USING (workspace_id = current_setting('app.workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.workspace_id', true)::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON conversations, conversation_turns TO vera_app;

-- A removed short task still counts towards the existing UTC daily quota.
CREATE TABLE conversation_quota_debits (
    task_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    created_at timestamptz NOT NULL
);
CREATE INDEX conversation_quota_debits_ws_day ON conversation_quota_debits(workspace_id, created_at);
ALTER TABLE conversation_quota_debits ENABLE ROW LEVEL SECURITY;
CREATE POLICY ws_isolation ON conversation_quota_debits USING (workspace_id = current_setting('app.workspace_id', true)::uuid);
GRANT SELECT ON conversation_quota_debits TO vera_app;

-- +goose Down
DROP TABLE conversation_quota_debits;
ALTER TABLE research_plans DROP COLUMN conversation_turn_id, DROP COLUMN conversation_plan, DROP COLUMN conversation_brief;
DROP TABLE conversation_turns;
DROP TABLE conversations;
