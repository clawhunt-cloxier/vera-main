-- +goose Up
ALTER TABLE conversations ADD COLUMN title_started boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE conversations DROP COLUMN title_started;
