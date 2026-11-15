-- +goose Up
-- These tables are preparation for the negotiated 0.4 plan flow. No intent is
-- created automatically, and the workspace application role cannot write or
-- read either table directly. Trusted confirmation code must bind the exact
-- approved plan, epoch, quote requirement and subjects before freezing.
CREATE TABLE quote_task_intents (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL CHECK (epoch >= 1),
    plan_hash text NOT NULL CHECK (length(btrim(plan_hash)) > 0),
    protocol text NOT NULL CHECK (protocol='0.4'),
    requirement text NOT NULL CHECK (requirement IN ('latest_available','realtime_trade')),
    entity_ids uuid[] NOT NULL CHECK (cardinality(entity_ids) BETWEEN 1 AND 64 AND array_position(entity_ids, NULL) IS NULL),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(workspace_id,task_id,epoch),
    FOREIGN KEY(workspace_id,task_id) REFERENCES research_tasks(workspace_id,id)
);

-- This is one component, not a full task snapshot. It must be bound with all
-- other components in the same transaction before a 0.4 task uses it.
CREATE TABLE quote_task_components (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL,
    task_revision bigint NOT NULL CHECK (task_revision >= 1),
    plan_hash text NOT NULL,
    cutoff timestamptz NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    manifest bytea NOT NULL CHECK (octet_length(manifest) BETWEEN 1 AND 1048576),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(workspace_id,task_id,epoch,content_hash),
    FOREIGN KEY(workspace_id,task_id,epoch) REFERENCES quote_task_intents(workspace_id,task_id,epoch)
);
ALTER TABLE quote_task_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE quote_task_components ENABLE ROW LEVEL SECURITY;
CREATE POLICY quote_intents_workspace ON quote_task_intents USING (workspace_id=current_setting('app.workspace_id',true)::uuid) WITH CHECK (workspace_id=current_setting('app.workspace_id',true)::uuid);
CREATE POLICY quote_components_workspace ON quote_task_components USING (workspace_id=current_setting('app.workspace_id',true)::uuid) WITH CHECK (workspace_id=current_setting('app.workspace_id',true)::uuid);

-- +goose StatementBegin
CREATE FUNCTION protect_quote_task_component() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'quote task intents and components cannot be rewritten or deleted';
END $$;
-- +goose StatementEnd
CREATE TRIGGER quote_intents_immutable BEFORE UPDATE OR DELETE ON quote_task_intents FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();
CREATE TRIGGER quote_components_immutable BEFORE UPDATE OR DELETE ON quote_task_components FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM quote_task_components) OR EXISTS (SELECT 1 FROM quote_task_intents) THEN
        RAISE EXCEPTION 'cannot discard task quote intents or frozen components';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE quote_task_components;
DROP TABLE quote_task_intents;
DROP FUNCTION protect_quote_task_component();
