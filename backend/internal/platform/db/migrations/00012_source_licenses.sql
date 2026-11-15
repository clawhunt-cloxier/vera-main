-- +goose Up
-- Entries are controlled configuration, not vendor-provided claims. Store a
-- reference to independently verified evidence, never contract text or secrets.
CREATE TABLE source_licenses (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider_id text NOT NULL REFERENCES providers(id),
    product_code text NOT NULL CHECK (length(btrim(product_code)) > 0 AND length(product_code) <= 128),
    dataset_id text NOT NULL CHECK (length(btrim(dataset_id)) > 0 AND length(dataset_id) <= 128),
    vendor_method text NOT NULL CHECK (vendor_method IN ('CSD','CSS','CSQSNAPSHOT','TRADEDATES','CFN')),
    field_names text[] NOT NULL CHECK (cardinality(field_names) > 0 AND array_position(field_names, NULL) IS NULL),
    instrument_scope text NOT NULL CHECK (instrument_scope IN ('all','listed')),
    instrument_codes text[] NOT NULL DEFAULT '{}',
    CHECK ((instrument_scope='all' AND cardinality(instrument_codes)=0)
        OR (instrument_scope='listed' AND cardinality(instrument_codes)>0 AND array_position(instrument_codes, NULL) IS NULL)),
    evidence_ref text NOT NULL CHECK (length(btrim(evidence_ref)) > 0 AND length(evidence_ref) <= 512),
    verified_by text NOT NULL CHECK (length(btrim(verified_by)) > 0 AND length(verified_by) <= 128),
    verified_at timestamptz NOT NULL,
    valid_from timestamptz NOT NULL,
    valid_until timestamptz CHECK (valid_until IS NULL OR valid_until > valid_from),
    revoked_at timestamptz,
    allow_storage boolean NOT NULL DEFAULT false,
    allow_display boolean NOT NULL DEFAULT false,
    allow_model_processing boolean NOT NULL DEFAULT false
);
CREATE INDEX source_licenses_scope ON source_licenses (provider_id,dataset_id,product_code);
-- Workspace-scoped roles must not grant, change or revoke product licenses.
-- A changed grant gets a new identity; revocation is one-way. This preserves
-- the scope and evidence referenced by previously registered objects.
-- +goose StatementBegin
CREATE FUNCTION protect_source_license() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'source licenses cannot be deleted';
    END IF;
    IF OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
       OR (to_jsonb(OLD) - 'revoked_at') IS DISTINCT FROM (to_jsonb(NEW) - 'revoked_at') THEN
        RAISE EXCEPTION 'source license changes require a new identity; revocation is one-way';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER source_licenses_immutable BEFORE UPDATE OR DELETE ON source_licenses
    FOR EACH ROW EXECUTE FUNCTION protect_source_license();
ALTER TABLE raw_objects
    ADD COLUMN source_license_id uuid REFERENCES source_licenses(id),
    ADD COLUMN vendor_method text,
    ADD COLUMN vendor_fields text[],
    ADD COLUMN vendor_codes text[],
    ADD CONSTRAINT raw_object_license_scope CHECK (
      (source_license_id IS NULL AND vendor_method IS NULL AND vendor_fields IS NULL AND vendor_codes IS NULL)
      OR (source_license_id IS NOT NULL AND vendor_method IS NOT NULL AND vendor_fields IS NOT NULL AND cardinality(vendor_fields) > 0 AND array_position(vendor_fields, NULL) IS NULL AND vendor_codes IS NOT NULL AND cardinality(vendor_codes) > 0 AND array_position(vendor_codes, NULL) IS NULL)
    );

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM source_licenses) THEN
        RAISE EXCEPTION 'cannot discard configured data-product licenses';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE raw_objects DROP CONSTRAINT raw_object_license_scope,
    DROP COLUMN vendor_codes, DROP COLUMN vendor_fields, DROP COLUMN vendor_method, DROP COLUMN source_license_id;
DROP TABLE source_licenses;
DROP FUNCTION protect_source_license();
