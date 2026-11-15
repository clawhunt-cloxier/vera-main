-- +goose Up
-- Existing objects may predate request-bound archives; their unknown metadata
-- remains NULL and must not be interpreted as verified lineage.
ALTER TABLE raw_objects
    ADD COLUMN request_fingerprint text,
    ADD COLUMN archive_format text,
    ADD COLUMN registered_at timestamptz,
    ADD CONSTRAINT raw_object_request_metadata CHECK (
      (request_fingerprint IS NULL AND archive_format IS NULL AND registered_at IS NULL)
      OR (request_fingerprint IS NOT NULL AND request_fingerprint ~ '^[0-9a-f]{64}$' AND archive_format IS NOT NULL AND registered_at IS NOT NULL)
    );
CREATE INDEX raw_objects_request ON raw_objects (provider_id, dataset_id, request_fingerprint)
    WHERE request_fingerprint IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM raw_objects WHERE request_fingerprint IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot discard registered raw request lineage';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX raw_objects_request;
ALTER TABLE raw_objects DROP CONSTRAINT raw_object_request_metadata,
    DROP COLUMN registered_at, DROP COLUMN archive_format, DROP COLUMN request_fingerprint;
