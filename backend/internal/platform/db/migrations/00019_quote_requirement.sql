-- +goose Up
-- The quote requirement is part of the user-confirmed 0.4 plan identity. It
-- is nullable for fixed_legacy and observation.v1 history, whose old hashes
-- must remain valid and whose intent must not be inferred retroactively.
ALTER TABLE research_plans ADD COLUMN quote_requirement text;
ALTER TABLE research_plans ADD CONSTRAINT research_plan_quote_requirement_check CHECK (
    (quote_requirement IS NULL AND skill_version <> '1.1.0-demo.3')
    OR (quote_requirement IS NOT NULL AND snapshot_mode='collect_latest' AND skill_id='stock_analysis'
        AND skill_version <> '1.1.0-demo.2'
        AND quote_requirement IN ('realtime_trade','latest_available'))
);

-- A confirmed plan is immutable. Changing the requirement after displaying a
-- plan would invalidate the hash and could silently change the source policy.
-- +goose StatementBegin
CREATE FUNCTION protect_plan_quote_requirement() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.quote_requirement IS DISTINCT FROM OLD.quote_requirement THEN
        RAISE EXCEPTION 'plan quote requirement is immutable';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER plan_quote_requirement_immutable BEFORE UPDATE ON research_plans
    FOR EACH ROW EXECUTE FUNCTION protect_plan_quote_requirement();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM research_plans WHERE quote_requirement IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot discard confirmed 0.4 quote requirements';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER plan_quote_requirement_immutable ON research_plans;
DROP FUNCTION protect_plan_quote_requirement();
ALTER TABLE research_plans DROP CONSTRAINT research_plan_quote_requirement_check;
ALTER TABLE research_plans DROP COLUMN quote_requirement;
