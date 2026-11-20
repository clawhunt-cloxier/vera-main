-- +goose Up
ALTER TABLE research_plans DROP CONSTRAINT research_plan_quote_requirement_check;
ALTER TABLE research_plans ADD CONSTRAINT research_plan_quote_requirement_check CHECK (
    (skill_version = '1.0.4' AND snapshot_mode='fixed_legacy' AND quote_requirement IS NOT DISTINCT FROM 'latest_available')
    OR (skill_version <> '1.0.4' AND (
        (quote_requirement IS NULL AND skill_version <> '1.1.0-demo.3')
        OR (quote_requirement IS NOT NULL AND snapshot_mode='collect_latest' AND skill_id='stock_analysis'
            AND skill_version <> '1.1.0-demo.2' AND quote_requirement IN ('realtime_trade','latest_available'))
    ))
);
CREATE TABLE research_source_supplements (
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL CHECK (epoch > 0),
    provider_id text NOT NULL REFERENCES providers(id),
    snapshot_hash text NOT NULL REFERENCES snapshots(hash),
    evidence_id uuid NOT NULL REFERENCES raw_objects(id),
    cutoff timestamptz NOT NULL,
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^[0-9a-f]{64}$'),
    manifest bytea NOT NULL CHECK (octet_length(manifest) BETWEEN 1 AND 1048576),
    PRIMARY KEY (workspace_id, task_id, epoch, provider_id),
    FOREIGN KEY (task_id, workspace_id) REFERENCES research_tasks(id, workspace_id)
);
CREATE INDEX research_source_supplements_evidence ON research_source_supplements (workspace_id, evidence_id);
ALTER TABLE research_source_supplements ENABLE ROW LEVEL SECURITY;
CREATE POLICY research_source_supplements_workspace ON research_source_supplements
    USING (workspace_id = current_setting('app.workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.workspace_id', true)::uuid);
GRANT SELECT, INSERT ON research_source_supplements TO vera_app;
CREATE TRIGGER research_source_supplements_immutable BEFORE UPDATE OR DELETE ON research_source_supplements
    FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM research_source_supplements) OR EXISTS (SELECT 1 FROM research_plans WHERE skill_version='1.0.4') THEN
        RAISE EXCEPTION 'cannot discard bound research provenance';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE research_source_supplements;
ALTER TABLE research_plans DROP CONSTRAINT research_plan_quote_requirement_check;
ALTER TABLE research_plans ADD CONSTRAINT research_plan_quote_requirement_check CHECK (
    (quote_requirement IS NULL AND skill_version <> '1.1.0-demo.3')
    OR (quote_requirement IS NOT NULL AND snapshot_mode='collect_latest' AND skill_id='stock_analysis'
        AND skill_version <> '1.1.0-demo.2' AND quote_requirement IN ('realtime_trade','latest_available'))
);
