-- +goose Up
-- Add password authentication support for demo accounts and direct login

ALTER TABLE users ADD COLUMN password_hash text;
ALTER TABLE users ADD COLUMN auth_method text NOT NULL DEFAULT 'oidc' CHECK (auth_method IN ('oidc', 'password'));

-- Demo account: administrator / administrator
-- Password hash for "administrator" using PBKDF2-HMAC-SHA256 (100k iterations, 32-byte key)
INSERT INTO users (issuer, subject, email, display_name, persona, auth_method, password_hash, created_at)
VALUES (
  'local',
  'administrator',
  'admin@vera.local',
  '演示管理员',
  'analyst',
  'password',
  '6jeXPaHYQFReNeRl6dLJ9w==$lZainjaod9Fj/VTZms1hKG2xo1DfZX0qyjxw7fz9qe8=',
  now()
)
ON CONFLICT (issuer, subject) DO UPDATE SET
  password_hash = excluded.password_hash,
  auth_method = excluded.auth_method,
  email = excluded.email,
  display_name = excluded.display_name;

-- Create workspace for demo account
INSERT INTO workspaces (owner_user_id, created_at)
SELECT id, now() FROM users WHERE issuer = 'local' AND subject = 'administrator' AND NOT EXISTS (
  SELECT 1 FROM workspaces WHERE owner_user_id = users.id
)
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM workspaces WHERE owner_user_id IN (
  SELECT id FROM users WHERE issuer = 'local' AND subject = 'administrator'
);
DELETE FROM users WHERE issuer = 'local' AND subject = 'administrator';
ALTER TABLE users DROP COLUMN auth_method;
ALTER TABLE users DROP COLUMN password_hash;
