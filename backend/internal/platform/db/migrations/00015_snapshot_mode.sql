-- +goose Up
-- Expand only: current writers continue to create fixed_legacy plans/tasks.
-- Deployments must drain old workers before enabling collect_latest creation.
ALTER TABLE research_plans ADD COLUMN snapshot_mode text NOT NULL DEFAULT 'fixed_legacy'
    CHECK (snapshot_mode IN ('fixed_legacy','collect_latest'));
ALTER TABLE research_tasks ADD COLUMN snapshot_mode text NOT NULL DEFAULT 'fixed_legacy'
    CHECK (snapshot_mode IN ('fixed_legacy','collect_latest')),
    ADD COLUMN snapshot_as_of timestamptz,
    ADD COLUMN snapshot_manifest_hash text;
ALTER TABLE research_tasks ADD CONSTRAINT task_snapshot_mode_consistency CHECK (
    (snapshot_mode='fixed_legacy' AND snapshot_as_of IS NULL AND snapshot_manifest_hash IS NULL)
    OR (snapshot_mode='collect_latest' AND snapshot_hash IS NULL AND
        ((snapshot_as_of IS NULL AND snapshot_manifest_hash IS NULL)
	         OR (snapshot_as_of IS NOT NULL AND snapshot_manifest_hash IS NOT NULL AND snapshot_as_of >= as_of
             AND snapshot_manifest_hash ~ '^[0-9a-f]{64}$')))
);

-- +goose StatementBegin
CREATE FUNCTION protect_snapshot_modes() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE plan_mode text;
BEGIN
    IF TG_TABLE_NAME='research_plans' THEN
        IF TG_OP='UPDATE' AND NEW.snapshot_mode IS DISTINCT FROM OLD.snapshot_mode THEN
            RAISE EXCEPTION 'plan snapshot mode is immutable';
        END IF;
        RETURN NEW;
    END IF;
    SELECT snapshot_mode INTO plan_mode FROM research_plans
      WHERE id=NEW.plan_id AND workspace_id=NEW.workspace_id;
    IF plan_mode IS NULL OR plan_mode <> NEW.snapshot_mode THEN
        RAISE EXCEPTION 'task snapshot mode must match its plan';
    END IF;
    IF TG_OP='UPDATE' THEN
        IF NEW.snapshot_mode IS DISTINCT FROM OLD.snapshot_mode OR
           (OLD.snapshot_mode='collect_latest' AND (NEW.as_of IS DISTINCT FROM OLD.as_of OR NEW.plan_id IS DISTINCT FROM OLD.plan_id)) THEN
            RAISE EXCEPTION 'task snapshot mode and preview time are immutable';
        END IF;
        IF OLD.snapshot_as_of IS NOT NULL AND
           ROW(NEW.snapshot_as_of,NEW.snapshot_manifest_hash) IS DISTINCT FROM
           ROW(OLD.snapshot_as_of,OLD.snapshot_manifest_hash) THEN
            RAISE EXCEPTION 'frozen snapshot binding is immutable';
        END IF;
        IF current_user='vera_app' AND ROW(NEW.snapshot_as_of,NEW.snapshot_manifest_hash) IS DISTINCT FROM
           ROW(OLD.snapshot_as_of,OLD.snapshot_manifest_hash) THEN
            RAISE EXCEPTION 'snapshot binding requires trusted service';
        END IF;
    ELSIF current_user='vera_app' AND (NEW.snapshot_as_of IS NOT NULL OR NEW.snapshot_manifest_hash IS NOT NULL) THEN
        RAISE EXCEPTION 'snapshot binding requires trusted service';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER plan_snapshot_mode_immutable BEFORE UPDATE ON research_plans
    FOR EACH ROW EXECUTE FUNCTION protect_snapshot_modes();
CREATE TRIGGER task_snapshot_mode_guard BEFORE INSERT OR UPDATE ON research_tasks
    FOR EACH ROW EXECUTE FUNCTION protect_snapshot_modes();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM research_plans WHERE snapshot_mode='collect_latest') OR
       EXISTS (SELECT 1 FROM research_tasks WHERE snapshot_mode='collect_latest') THEN
        RAISE EXCEPTION 'cannot discard collect_latest plans or snapshot bindings';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER task_snapshot_mode_guard ON research_tasks;
DROP TRIGGER plan_snapshot_mode_immutable ON research_plans;
DROP FUNCTION protect_snapshot_modes();
ALTER TABLE research_tasks DROP CONSTRAINT task_snapshot_mode_consistency,
    DROP COLUMN snapshot_manifest_hash, DROP COLUMN snapshot_as_of, DROP COLUMN snapshot_mode;
ALTER TABLE research_plans DROP COLUMN snapshot_mode;
