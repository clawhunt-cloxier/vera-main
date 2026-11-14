-- +goose Up
-- T014: add blood lineage and quality fields to records.
-- Depends on T012 migrations (00007, 00008). Renumber if merge order changes.

-- Blood lineage: how a record was obtained from raw storage.
ALTER TABLE records
  ADD COLUMN raw_ref         text,          -- object-storage key for the raw response
  ADD COLUMN raw_sha256      text,          -- SHA-256 of raw response body
  ADD COLUMN raw_locator     text,          -- JSON path or page range within raw body
  ADD COLUMN mapping_version int,           -- version of the MappingSpec used
  ADD COLUMN quality_state   text NOT NULL DEFAULT 'PASS'
    CHECK (quality_state IN ('PASS','WARN','QUARANTINED','REJECTED')),
  ADD COLUMN revision_of     uuid REFERENCES records(id);  -- set when record corrects another

-- Index for quality-filtered queries (exclude QUARANTINED/REJECTED).
CREATE INDEX records_quality ON records (dataset_id, quality_state, available_at DESC)
  WHERE quality_state IN ('PASS','WARN');

-- +goose Down
DROP INDEX IF EXISTS records_quality;
ALTER TABLE records
  DROP COLUMN IF EXISTS revision_of,
  DROP COLUMN IF EXISTS quality_state,
  DROP COLUMN IF EXISTS mapping_version,
  DROP COLUMN IF EXISTS raw_locator,
  DROP COLUMN IF EXISTS raw_sha256,
  DROP COLUMN IF EXISTS raw_ref;
