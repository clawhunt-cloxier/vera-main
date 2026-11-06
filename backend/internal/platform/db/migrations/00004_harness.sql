-- +goose Up
CREATE TABLE calculations (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    epoch bigint NOT NULL,
    formula_id text NOT NULL,
    formula_version text NOT NULL,
    inputs jsonb NOT NULL,
    result numeric,
    unit text NOT NULL,
    applicability text NOT NULL CHECK (applicability IN ('APPLICABLE','NOT_APPLICABLE','MISSING_INPUT','CONFLICT')),
    reason text NOT NULL DEFAULT '',
    FOREIGN KEY (workspace_id, task_id) REFERENCES research_tasks(workspace_id, id)
);
CREATE INDEX calculations_task ON calculations (task_id, epoch);
ALTER TABLE calculations ENABLE ROW LEVEL SECURITY;
CREATE POLICY ws_isolation ON calculations USING (workspace_id = current_setting('app.workspace_id', true)::uuid)
    WITH CHECK (workspace_id = current_setting('app.workspace_id', true)::uuid);
GRANT SELECT, INSERT ON calculations TO vera_app;

-- Platform switches (kill switch) are global, written by vera-admin only.
CREATE TABLE platform_settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT ON platform_settings TO vera_app;

-- Platform-wide in-flight count without exposing other workspaces' rows to vera_app.
-- +goose StatementBegin
CREATE FUNCTION platform_inflight_tasks() RETURNS bigint LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT count(*) FROM research_tasks WHERE status IN ('QUEUED','COLLECTING','ANALYZING','ASSEMBLING','DELIVERING','CANCEL_REQUESTED')
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION platform_inflight_tasks() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform_inflight_tasks() TO vera_app;

-- Tasks enqueue River jobs in the same workspace-scoped transaction.
GRANT USAGE ON SCHEMA public TO vera_app;
GRANT SELECT, INSERT, UPDATE ON river_job TO vera_app;
GRANT USAGE, SELECT ON SEQUENCE river_job_id_seq TO vera_app;
-- River notifies via pg_notify; no extra grant needed.

ALTER TABLE report_versions ADD COLUMN series jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE report_versions DROP COLUMN series;
REVOKE ALL ON river_job FROM vera_app;
DROP FUNCTION platform_inflight_tasks();
DROP TABLE platform_settings, calculations;
