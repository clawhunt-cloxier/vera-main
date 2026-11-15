-- +goose Up
-- Add password authentication support for demo accounts and direct login

ALTER TABLE users ADD COLUMN password_hash text;
ALTER TABLE users ADD COLUMN auth_method text NOT NULL DEFAULT 'oidc';

-- +goose Down
ALTER TABLE users DROP COLUMN auth_method;
ALTER TABLE users DROP COLUMN password_hash;
