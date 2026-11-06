-- +goose Up
-- River tables for job queue
CREATE SCHEMA IF NOT EXISTS river;

-- River will create its own tables; this migration just ensures the schema exists
-- and sets the search path for the river role if needed.

COMMENT ON SCHEMA river IS 'River job queue tables';

-- +goose Down
DROP SCHEMA IF EXISTS river CASCADE;
