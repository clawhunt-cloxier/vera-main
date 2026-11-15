-- +goose Up
-- These rows must be created by trusted 0.4 confirmation from the confirmed
-- plan, never by a model or an untrusted workspace role.
CREATE TABLE task_snapshot_requirements (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL,
    dataset_ids text[] NOT NULL CHECK (cardinality(dataset_ids) BETWEEN 1 AND 128 AND array_position(dataset_ids,NULL) IS NULL),
    required_ids text[] NOT NULL CHECK (array_position(required_ids,NULL) IS NULL AND required_ids <@ dataset_ids),
    PRIMARY KEY(workspace_id,task_id,epoch),
    FOREIGN KEY(workspace_id,task_id,epoch) REFERENCES quote_task_intents(workspace_id,task_id,epoch)
);

-- A manifest is bound to the task in the same transaction.  A component or
-- manifest row alone does not authorize its content for display or a model.
CREATE TABLE task_snapshot_manifests (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL,
    task_revision bigint NOT NULL CHECK (task_revision >= 1),
    plan_hash text NOT NULL,
    cutoff timestamptz NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    quote_hash text NOT NULL CHECK (quote_hash ~ '^[0-9a-f]{64}$'),
    manifest bytea NOT NULL CHECK (octet_length(manifest) BETWEEN 1 AND 1048576),
    PRIMARY KEY(workspace_id,task_id,epoch),
    FOREIGN KEY(workspace_id,task_id,epoch,quote_hash) REFERENCES quote_task_components(workspace_id,task_id,epoch,content_hash)
);
ALTER TABLE task_snapshot_requirements ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_snapshot_manifests ENABLE ROW LEVEL SECURITY;
CREATE POLICY snapshot_requirements_workspace ON task_snapshot_requirements USING (workspace_id=current_setting('app.workspace_id',true)::uuid) WITH CHECK (workspace_id=current_setting('app.workspace_id',true)::uuid);
CREATE POLICY snapshot_manifests_workspace ON task_snapshot_manifests USING (workspace_id=current_setting('app.workspace_id',true)::uuid) WITH CHECK (workspace_id=current_setting('app.workspace_id',true)::uuid);
CREATE TRIGGER snapshot_requirements_immutable BEFORE UPDATE OR DELETE ON task_snapshot_requirements FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();
CREATE TRIGGER snapshot_manifests_immutable BEFORE UPDATE OR DELETE ON task_snapshot_manifests FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM task_snapshot_requirements) OR EXISTS (SELECT 1 FROM task_snapshot_manifests) THEN
        RAISE EXCEPTION 'cannot discard task snapshot requirements or manifests';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE task_snapshot_manifests;
DROP TABLE task_snapshot_requirements;
