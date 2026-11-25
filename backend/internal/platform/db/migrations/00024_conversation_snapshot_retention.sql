-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION protect_observation_brief_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND EXISTS (
        SELECT 1 FROM research_tasks r
        JOIN research_plans p ON p.workspace_id=r.workspace_id AND p.id=r.plan_id
        JOIN conversation_turns t ON t.workspace_id=p.workspace_id AND t.id=p.conversation_turn_id
        JOIN conversations c ON c.workspace_id=t.workspace_id AND c.id=t.conversation_id
        WHERE r.workspace_id=OLD.workspace_id AND r.id=OLD.task_id
          AND p.conversation_plan AND p.conversation_brief AND c.deleted_at IS NOT NULL
          AND r.status IN ('COMPLETED','ABSTAINED','BUDGET_EXHAUSTED','CANCELLED','BLOCKED','FAILED')
    ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'bound observation snapshot is immutable';
END $$;
-- +goose StatementEnd
DROP TRIGGER observation_briefs_immutable ON observation_brief_snapshots;
CREATE TRIGGER observation_briefs_immutable BEFORE UPDATE OR DELETE ON observation_brief_snapshots
    FOR EACH ROW EXECUTE FUNCTION protect_observation_brief_snapshot();

-- +goose Down
DROP TRIGGER observation_briefs_immutable ON observation_brief_snapshots;
DROP FUNCTION protect_observation_brief_snapshot();
CREATE TRIGGER observation_briefs_immutable BEFORE UPDATE OR DELETE ON observation_brief_snapshots
    FOR EACH ROW EXECUTE FUNCTION protect_quote_task_component();
