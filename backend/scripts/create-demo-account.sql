-- Create demo account: administrator / administrator
-- Run this manually in development/staging environments: psql < scripts/create-demo-account.sql

-- Demo account with PBKDF2-HMAC-SHA256 password hash (100k iterations, 32-byte key)
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
SELECT id, now() FROM users WHERE issuer = 'local' AND subject = 'administrator'
ON CONFLICT DO NOTHING;
