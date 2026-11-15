-- +goose Up
-- Closed-market observation briefs are separate from verified realtime quotes.
CREATE TABLE observation_brief_snapshots (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL CHECK (epoch > 0),
    entity_id uuid NOT NULL REFERENCES entities(id),
    cutoff timestamptz NOT NULL,
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    manifest bytea NOT NULL CHECK (octet_length(manifest) BETWEEN 1 AND 1048576),
    PRIMARY KEY (workspace_id,task_id,epoch),
    FOREIGN KEY(task_id,workspace_id) REFERENCES research_tasks(id,workspace_id)
);
ALTER TABLE observation_brief_snapshots ENABLE ROW LEVEL SECURITY;
CREATE POLICY observation_briefs_workspace ON observation_brief_snapshots
    USING (workspace_id=current_setting('app.workspace_id',true)::uuid);
GRANT SELECT ON observation_brief_snapshots TO vera_app;
CREATE TRIGGER observation_briefs_immutable BEFORE UPDATE OR DELETE ON observation_brief_snapshots
    FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM observation_brief_snapshots) THEN
        RAISE EXCEPTION 'cannot discard delivered observation provenance';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE observation_brief_snapshots;
