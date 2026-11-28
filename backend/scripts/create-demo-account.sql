-- Create a local simulated demo account. Defaults to administrator / administrator.
-- Run with psql -v ON_ERROR_STOP=1 -v demo_username=NAME -f scripts/create-demo-account.sql.
\if :{?demo_username}
\else
\set demo_username administrator
\endif

BEGIN;

-- Demo account with PBKDF2-HMAC-SHA256 password hash (100k iterations, 32-byte key)
INSERT INTO users (issuer, subject, email, display_name, persona, password_hash, created_at)
VALUES (
  'local',
  :'demo_username',
  :'demo_username' || '@vera.local',
  '演示管理员',
  'analyst',
  '6jeXPaHYQFReNeRl6dLJ9w==$lZainjaod9Fj/VTZms1hKG2xo1DfZX0qyjxw7fz9qe8=',
  now()
)
ON CONFLICT (issuer, subject) DO UPDATE SET
  password_hash = excluded.password_hash,
  email = excluded.email,
  display_name = excluded.display_name;

-- Create workspace for demo account
INSERT INTO workspaces (owner_user_id, created_at)
SELECT id, now() FROM users WHERE issuer = 'local' AND subject = :'demo_username'
ON CONFLICT DO NOTHING;

COMMIT;
