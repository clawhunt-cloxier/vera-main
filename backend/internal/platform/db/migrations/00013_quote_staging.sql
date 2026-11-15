-- +goose Up
-- A registered request must retain its content, source and grant binding.
-- No-op updates are allowed for the registry's idempotent upsert.
-- +goose StatementBegin
CREATE FUNCTION protect_registered_raw_object() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.request_fingerprint IS NOT NULL THEN
        IF TG_OP='DELETE' OR to_jsonb(OLD) IS DISTINCT FROM to_jsonb(NEW) THEN
            RAISE EXCEPTION 'registered raw object metadata is immutable';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    IF OLD.request_fingerprint IS NULL AND NEW.request_fingerprint IS NOT NULL THEN
        RAISE EXCEPTION 'legacy raw objects cannot be promoted to verified request lineage';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER registered_raw_objects_immutable BEFORE UPDATE OR DELETE ON raw_objects
    FOR EACH ROW EXECUTE FUNCTION protect_registered_raw_object();

-- Policy rows are controlled, independently verified configuration. Evidence
-- references are not vendor-provided instructions or contractual text.
CREATE TABLE quote_mapping_policies (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    source_license_id uuid NOT NULL REFERENCES source_licenses(id),
    dataset_id text NOT NULL CHECK (dataset_id='mkt.intraday_quote'),
    mapping_name text NOT NULL CHECK (mapping_name='emquant-quote'),
    mapping_version integer NOT NULL CHECK (mapping_version=1),
    time_convention text NOT NULL CHECK (time_convention='hhmmss-asia-shanghai'),
    time_evidence_ref text NOT NULL CHECK (length(btrim(time_evidence_ref))>0),
    price_unit text NOT NULL CHECK (price_unit='CNY'),
    price_evidence_ref text NOT NULL CHECK (length(btrim(price_evidence_ref))>0),
    latency_mode text NOT NULL CHECK (latency_mode IN ('real_time','delayed','unknown')),
    latency_evidence_ref text,
    CHECK (latency_mode='unknown' OR length(btrim(coalesce(latency_evidence_ref,'')))>0),
    max_age_ms bigint NOT NULL CHECK (max_age_ms BETWEEN 1 AND 86400000),
    verified_by text NOT NULL CHECK (length(btrim(verified_by))>0),
    verified_at timestamptz NOT NULL,
    valid_from timestamptz NOT NULL,
    valid_until timestamptz CHECK (valid_until IS NULL OR valid_until>valid_from),
    revoked_at timestamptz
);

-- Policy revisions use a new ID. Revocation is the only permitted change.
-- +goose StatementBegin
CREATE FUNCTION protect_quote_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'quote policies cannot be deleted'; END IF;
    IF OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
       OR (to_jsonb(OLD)-'revoked_at') IS DISTINCT FROM (to_jsonb(NEW)-'revoked_at') THEN
        RAISE EXCEPTION 'quote policy changes require a new identity; revocation is one-way';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER quote_policies_immutable BEFORE UPDATE OR DELETE ON quote_mapping_policies
    FOR EACH ROW EXECUTE FUNCTION protect_quote_policy();

-- A staging row is not a canonical record and cannot be read by legacy
-- research queries. Its staged_at is not an availability/commit timestamp.
CREATE TABLE staged_quotes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    raw_object_id uuid NOT NULL REFERENCES raw_objects(id),
    policy_id uuid NOT NULL REFERENCES quote_mapping_policies(id),
    entity_id uuid NOT NULL REFERENCES entities(id),
    security_code text NOT NULL,
    event_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    payload jsonb NOT NULL,
    field_locators jsonb NOT NULL,
    staged_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(raw_object_id,policy_id,entity_id)
);
-- +goose StatementBegin
CREATE FUNCTION protect_staged_quote() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'staged quote versions cannot be changed or deleted';
END $$;
-- +goose StatementEnd
CREATE TRIGGER staged_quotes_immutable BEFORE UPDATE OR DELETE ON staged_quotes
    FOR EACH ROW EXECUTE FUNCTION protect_staged_quote();

-- This marker is inserted only in a separate transaction after the staged
-- version has committed. available_at is not itself evidence that this row
-- was visible in an earlier database snapshot; readers must use an MVCC view.
CREATE TABLE quote_publications (
    staged_quote_id uuid PRIMARY KEY REFERENCES staged_quotes(id),
    available_at timestamptz NOT NULL
);
CREATE TRIGGER quote_publications_immutable BEFORE UPDATE OR DELETE ON quote_publications
    FOR EACH ROW EXECUTE FUNCTION protect_staged_quote();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM quote_publications) OR EXISTS (SELECT 1 FROM staged_quotes) OR EXISTS (SELECT 1 FROM quote_mapping_policies)
       OR EXISTS (SELECT 1 FROM raw_objects WHERE request_fingerprint IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot discard quote policies, staged evidence or raw object protection';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE quote_publications;
DROP TABLE staged_quotes;
DROP FUNCTION protect_staged_quote();
DROP TABLE quote_mapping_policies;
DROP FUNCTION protect_quote_policy();
DROP TRIGGER registered_raw_objects_immutable ON raw_objects;
DROP FUNCTION protect_registered_raw_object();
