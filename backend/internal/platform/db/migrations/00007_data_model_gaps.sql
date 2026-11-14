-- +goose Up
-- Usage ledger: definitive record of model call costs for billing and quota tracking
CREATE TABLE usage_ledger (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    call_id uuid NOT NULL UNIQUE REFERENCES model_calls(id),
    task_id uuid,
    profile text NOT NULL,
    observed_model text NOT NULL,
    input_tokens int NOT NULL CHECK (input_tokens >= 0),
    output_tokens int NOT NULL CHECK (output_tokens >= 0),
    cost_usd numeric(12,6) NOT NULL CHECK (cost_usd >= 0),
    price_table text NOT NULL,
    occurred_at timestamptz NOT NULL,
    reconciled_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_ledger_ws_time ON usage_ledger (workspace_id, occurred_at DESC);
CREATE INDEX usage_ledger_task ON usage_ledger (task_id, occurred_at DESC);

-- Publications: public-facing report metadata (future feature)
CREATE TABLE publications (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    report_id uuid NOT NULL,
    version int NOT NULL,
    permalink text NOT NULL UNIQUE,
    visibility text NOT NULL CHECK (visibility IN ('private','unlisted','public')),
    published_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (report_id, version) REFERENCES report_versions(report_id, version)
);
CREATE INDEX publications_ws ON publications (workspace_id, published_at DESC);

-- Raw objects: archive of provider responses before normalization
CREATE TABLE raw_objects (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider_id text NOT NULL REFERENCES providers(id),
    dataset_id text NOT NULL,
    object_key text NOT NULL,
    content_hash text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    stored_path text NOT NULL,
    fetched_at timestamptz NOT NULL,
    archived_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, dataset_id, object_key, content_hash)
);
CREATE INDEX raw_objects_provider ON raw_objects (provider_id, dataset_id, fetched_at DESC);

-- Record conflicts: when multiple providers have different values for the same logical record
CREATE TABLE record_conflicts (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    dataset_id text NOT NULL,
    logical_key text NOT NULL,
    record_ids uuid[] NOT NULL,
    conflict_type text NOT NULL CHECK (conflict_type IN ('value_mismatch','period_mismatch','tier_disagree')),
    detected_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolution_note text
);
CREATE INDEX record_conflicts_unresolved ON record_conflicts (detected_at DESC) WHERE resolved_at IS NULL;

-- Snapshot items: denormalized view of what's in each snapshot
CREATE TABLE snapshot_items (
    snapshot_hash text NOT NULL REFERENCES snapshots(hash) ON DELETE CASCADE,
    record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    dataset_id text NOT NULL,
    entity_id uuid REFERENCES entities(id),
    period date,
    PRIMARY KEY (snapshot_hash, record_id)
);
CREATE INDEX snapshot_items_dataset ON snapshot_items (dataset_id, entity_id, period);

-- Fact sheets: computed facts for each task (persistent cache of FactSheet struct)
CREATE TABLE fact_sheets (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL UNIQUE,
    epoch bigint NOT NULL,
    sheet jsonb NOT NULL,
    sheet_hash text NOT NULL,
    computed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, task_id),
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id)
);

-- +goose Down
DROP TABLE fact_sheets, snapshot_items, record_conflicts, raw_objects, publications, usage_ledger;
