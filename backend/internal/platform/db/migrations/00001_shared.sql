-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE invites (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    code_hash text NOT NULL UNIQUE,
    remaining int NOT NULL CHECK (remaining >= 0),
    note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE TABLE entities (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    kind text NOT NULL CHECK (kind IN ('stock','industry','market')),
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    exchange text,
    status text NOT NULL DEFAULT 'listed',
    is_financial boolean NOT NULL DEFAULT false,
    classification text,
    synthetic boolean NOT NULL DEFAULT false,
    attrs jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE entity_aliases (
    entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    alias text NOT NULL,
    PRIMARY KEY (entity_id, alias)
);
CREATE INDEX entity_aliases_trgm ON entity_aliases USING gin (alias gin_trgm_ops);

CREATE TABLE providers (
    id text PRIMARY KEY,
    name text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('fixture','vendor')),
    enabled boolean NOT NULL DEFAULT true,
    allow_model_processing boolean NOT NULL DEFAULT false,
    synthetic boolean NOT NULL DEFAULT false,
    basis jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE capabilities (
    provider_id text NOT NULL REFERENCES providers(id),
    dataset_id text NOT NULL,
    entity_id uuid REFERENCES entities(id),
    coverage_from date,
    coverage_to date,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (provider_id, dataset_id, entity_id)
);

CREATE TABLE records (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    dataset_id text NOT NULL,
    entity_id uuid REFERENCES entities(id),
    record_key text NOT NULL,
    logical_key text NOT NULL,
    record_version int NOT NULL DEFAULT 1,
    period date,
    published_at timestamptz,
    available_at timestamptz NOT NULL,
    provider_id text NOT NULL REFERENCES providers(id),
    origin text NOT NULL,
    source_tier text NOT NULL CHECK (source_tier IN ('L1','L2','L3','L4')),
    basis text,
    payload jsonb NOT NULL,
    content_hash text NOT NULL,
    is_latest boolean NOT NULL DEFAULT true,
    ingested_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (dataset_id, record_key, record_version)
);
CREATE INDEX records_lookup ON records (dataset_id, entity_id, period);
CREATE INDEX records_logical ON records (dataset_id, logical_key, available_at DESC);

CREATE TABLE snapshots (
    hash text PRIMARY KEY,
    as_of timestamptz NOT NULL,
    record_ids uuid[] NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid,
    actor text NOT NULL,
    action text NOT NULL,
    resource text NOT NULL DEFAULT '',
    result text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}',
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_time ON audit_events (occurred_at DESC);

-- +goose Down
DROP TABLE audit_events, snapshots, records, capabilities, providers, entity_aliases, entities, invites;
