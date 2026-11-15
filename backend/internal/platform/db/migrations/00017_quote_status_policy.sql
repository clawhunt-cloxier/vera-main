-- +goose Up
-- Optional, independently verified status mapping for a vendor snapshot.
-- Old policies remain unknown; the existing immutable-policy trigger permits
-- configuration only on a newly inserted policy, never retroactive changes.
ALTER TABLE quote_mapping_policies
    ADD COLUMN status_indicator text,
    ADD COLUMN status_evidence_ref text,
    ADD COLUMN status_mapping jsonb,
    ADD CONSTRAINT quote_status_policy_complete CHECK (
        (status_indicator IS NULL AND status_evidence_ref IS NULL AND status_mapping IS NULL)
        OR (status_indicator IS NOT NULL AND status_indicator ~ '^[A-Z][A-Z0-9_]{0,127}$'
            AND status_indicator NOT IN ('TIME','NOW') AND status_evidence_ref IS NOT NULL
            AND length(btrim(status_evidence_ref)) BETWEEN 1 AND 512 AND status_mapping IS NOT NULL
            AND jsonb_typeof(status_mapping)='object' AND status_mapping <> '{}'::jsonb)
    );

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM quote_mapping_policies WHERE status_indicator IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot discard verified quote status policies';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE quote_mapping_policies DROP CONSTRAINT quote_status_policy_complete,
    DROP COLUMN status_mapping, DROP COLUMN status_evidence_ref, DROP COLUMN status_indicator;
