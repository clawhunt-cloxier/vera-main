-- +goose Up
-- vera_app is the role used inside workspace-scoped transactions (SET LOCAL ROLE vera_app).
-- It owns nothing, so row-level security applies to it. The connecting role must be a member.
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'vera_app') THEN
    CREATE ROLE vera_app NOLOGIN NOSUPERUSER NOBYPASSRLS;
  END IF;
END $$;
-- +goose StatementEnd
GRANT vera_app TO CURRENT_USER;
GRANT USAGE ON SCHEMA public TO vera_app;
GRANT SELECT ON invites, entities, entity_aliases, providers, capabilities, records, snapshots, users, workspaces TO vera_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON research_plans, research_tasks, task_events, task_steps, model_calls, claims,
  report_versions, watchlist_items, watchlist_snapshots, feedback, idempotency_keys TO vera_app;
GRANT INSERT ON audit_events TO vera_app;

-- +goose Down
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM vera_app;
