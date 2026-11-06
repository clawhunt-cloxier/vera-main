-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    issuer text NOT NULL,
    subject text NOT NULL,
    email text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    persona text NOT NULL DEFAULT 'unspecified' CHECK (persona IN ('fund_manager','analyst','individual','unspecified')),
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

CREATE TABLE workspaces (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_user_id uuid NOT NULL UNIQUE REFERENCES users(id),
    invite_id uuid REFERENCES invites(id),
    quota_override jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id_hash text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    workspace_id uuid REFERENCES workspaces(id),
    csrf_token text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE TABLE oidc_logins (
    state text PRIMARY KEY,
    nonce text NOT NULL,
    code_verifier text NOT NULL,
    return_to text NOT NULL DEFAULT '/app',
    expires_at timestamptz NOT NULL
);

CREATE TABLE research_plans (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    skill_id text NOT NULL,
    skill_version text NOT NULL,
    subject_id uuid NOT NULL REFERENCES entities(id),
    params jsonb NOT NULL,
    sections jsonb NOT NULL,
    as_of timestamptz NOT NULL,
    plan_hash text NOT NULL,
    executable boolean NOT NULL,
    reason_code text,
    expires_at timestamptz NOT NULL,
    consumed_task_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);

CREATE TABLE research_tasks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    plan_id uuid NOT NULL UNIQUE,
    skill_id text NOT NULL,
    skill_version text NOT NULL,
    subject_id uuid NOT NULL REFERENCES entities(id),
    params jsonb NOT NULL,
    sections jsonb NOT NULL,
    as_of timestamptz NOT NULL,
    status text NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch >= 1),
    deadline timestamptz NOT NULL,
    budget jsonb NOT NULL,
    snapshot_hash text REFERENCES snapshots(hash),
    report_id uuid,
    reason_code text,
    reason_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, plan_id) REFERENCES research_plans(workspace_id, id),
    UNIQUE (workspace_id, id)
);
CREATE INDEX research_tasks_ws_time ON research_tasks (workspace_id, created_at DESC, id DESC);
CREATE INDEX research_tasks_status ON research_tasks (status, deadline);

CREATE TABLE task_events (
    task_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    seq bigint NOT NULL,
    epoch bigint NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, seq),
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id)
);

CREATE TABLE task_steps (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL,
    step_key text NOT NULL,
    label text NOT NULL,
    status text NOT NULL CHECK (status IN ('PENDING','RUNNING','SUCCEEDED','FAILED','SKIPPED')),
    input_hash text NOT NULL DEFAULT '',
    output jsonb,
    output_hash text,
    error text,
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (task_id, epoch, step_key),
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id)
);

CREATE TABLE model_calls (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL,
    task_id uuid,
    step_key text NOT NULL,
    attempt int NOT NULL,
    profile text NOT NULL,
    adapter text NOT NULL,
    model_id text NOT NULL,
    observed_model text,
    request_hash text NOT NULL,
    status text NOT NULL CHECK (status IN ('SENT','SUCCEEDED','FAILED','UNKNOWN','RECONCILED')),
    response jsonb,
    usage jsonb,
    reserved_usd numeric(12,6) NOT NULL DEFAULT 0,
    cost_usd numeric(12,6),
    price_table text,
    sent_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    error text
);
CREATE INDEX model_calls_task ON model_calls (task_id, step_key, attempt);
CREATE INDEX model_calls_status ON model_calls (status, sent_at);
CREATE INDEX model_calls_day ON model_calls (sent_at);

CREATE TABLE claims (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL,
    section_id text NOT NULL,
    claim_ref text NOT NULL,
    kind text NOT NULL,
    text text NOT NULL,
    refs text[] NOT NULL,
    scenario jsonb,
    verdict text,
    removed_reason text,
    PRIMARY KEY (task_id, epoch, claim_ref),
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id)
);

CREATE TABLE report_versions (
    report_id uuid NOT NULL,
    version int NOT NULL CHECK (version >= 1),
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL UNIQUE,
    epoch bigint NOT NULL,
    skill_id text NOT NULL,
    subject_id uuid NOT NULL,
    title text NOT NULL,
    result_kind text NOT NULL CHECK (result_kind IN ('full','limited')),
    synthetic boolean NOT NULL,
    document jsonb NOT NULL,
    document_hash text NOT NULL,
    snapshot_hash text NOT NULL,
    plan_hash text NOT NULL,
    versions jsonb NOT NULL,
    delivered_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (report_id, version),
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id)
);
CREATE INDEX report_versions_ws ON report_versions (workspace_id, delivered_at DESC, report_id DESC);

CREATE TABLE watchlist_items (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    entity_id uuid NOT NULL REFERENCES entities(id),
    tags text[] NOT NULL DEFAULT '{}',
    note text NOT NULL DEFAULT '',
    alerts jsonb NOT NULL DEFAULT '[]',
    added_at timestamptz NOT NULL DEFAULT now(),
    added_price numeric,
    added_trade_date date,
    version bigint NOT NULL DEFAULT 1,
    UNIQUE (workspace_id, entity_id)
);

CREATE TABLE watchlist_snapshots (
    workspace_id uuid NOT NULL,
    item_id uuid NOT NULL REFERENCES watchlist_items(id) ON DELETE CASCADE,
    trade_date date NOT NULL,
    metrics jsonb NOT NULL,
    triggered text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (item_id, trade_date)
);

CREATE TABLE feedback (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    user_id uuid NOT NULL REFERENCES users(id),
    report_id uuid NOT NULL,
    version int NOT NULL,
    kind text NOT NULL CHECK (kind IN ('useful','confusing','incorrect','other')),
    claim_ref text,
    comment text NOT NULL DEFAULT '' CHECK (length(comment) <= 2000),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (report_id, version) REFERENCES report_versions(report_id, version)
);

CREATE TABLE idempotency_keys (
    workspace_id uuid NOT NULL,
    method text NOT NULL,
    route text NOT NULL,
    key text NOT NULL,
    request_hash text NOT NULL,
    status int NOT NULL,
    response jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (workspace_id, method, route, key)
);

-- Row-level security as defence in depth. The application role sets app.workspace_id per transaction.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['research_plans','research_tasks','task_events','task_steps','model_calls','claims',
                           'report_versions','watchlist_items','watchlist_snapshots','feedback','idempotency_keys'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY ws_isolation ON %I USING (workspace_id = current_setting(''app.workspace_id'', true)::uuid) WITH CHECK (workspace_id = current_setting(''app.workspace_id'', true)::uuid)', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE idempotency_keys, feedback, watchlist_snapshots, watchlist_items, report_versions, claims, model_calls,
  task_steps, task_events, research_tasks, research_plans, oidc_logins, sessions, workspaces, users;
