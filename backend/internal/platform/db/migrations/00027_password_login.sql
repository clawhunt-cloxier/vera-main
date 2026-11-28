-- +goose Up
-- Older deployments may have provisioned the column separately.
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash text;

-- +goose Down
-- The old application uses this column too. Keep provisioned account hashes.
SELECT 1;
