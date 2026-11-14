-- +goose Up
-- T018: step leases (HARNESS §10.2). A worker claims a step by bumping attempt and setting lease_until; it renews the
-- lease while running and may only write the result while attempt still matches (fencing against a worker that lost
-- its lease). Rows written before this migration have no lease and are claimable.
ALTER TABLE task_steps ADD COLUMN attempt int NOT NULL DEFAULT 0, ADD COLUMN lease_until timestamptz;

-- +goose Down
ALTER TABLE task_steps DROP COLUMN lease_until, DROP COLUMN attempt;
