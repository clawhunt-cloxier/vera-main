-- +goose Up
-- T013: store only the hash of the per-session CSRF token (DATA_MODEL sessions.csrf_hash).
-- The token is now derived from the session cookie, so existing sessions (random plaintext tokens)
-- cannot be converted; they are revoked and users sign in again.
ALTER TABLE sessions ADD COLUMN csrf_hash text;
UPDATE sessions SET csrf_hash = '', revoked_at = coalesce(revoked_at, now());
ALTER TABLE sessions ALTER COLUMN csrf_hash SET NOT NULL;
ALTER TABLE sessions DROP COLUMN csrf_token;

-- +goose Down
ALTER TABLE sessions ADD COLUMN csrf_token text;
UPDATE sessions SET csrf_token = '', revoked_at = coalesce(revoked_at, now());
ALTER TABLE sessions ALTER COLUMN csrf_token SET NOT NULL;
ALTER TABLE sessions DROP COLUMN csrf_hash;
