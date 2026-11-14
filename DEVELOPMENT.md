# Development Environment Setup

This directory contains the Docker Compose-based development environment for Vera.

## Quick Start

```bash
# Start core services (Postgres, Keycloak, Caddy)
# MinIO is optional; start with: docker compose --profile storage up -d
make dev-up

# Apply database migrations
make dev-migrate

# In separate terminals:
# Terminal 1: Start API server
make dev-api

# Terminal 2: Start web dev server  
make dev-web

# Access the application at http://localhost:3000
```

## Services

- **Postgres** (port 5432): Main database
  - Database: `vera_dev`
  - User: `postgres`
  - Password: `dev_password_change_in_production`
  
- **MinIO** (ports 9000/9001): S3-compatible object storage — optional, start with `docker compose --profile storage up -d`
  - API: http://localhost:9000
  - Console: http://localhost:9001
  - Access Key: `minioadmin`
  - Secret Key: `minioadmin`

- **Keycloak** (port 8080): Identity provider
  - Admin console: http://localhost:8081
  - Admin user: `admin`
  - Admin password: `admin`

- **Caddy** (port 3000): Reverse proxy
  - Main app: http://localhost:3000
  - Routes `/api/*` to backend (port 8000)
  - Routes `/auth/*` to Keycloak
  - Routes other requests to frontend (port 5173)

## Database Migrations

Migrations are managed with [goose](https://github.com/pressly/goose).

```bash
# Apply all migrations
make dev-migrate

# Manual migration commands:
cd backend
go run github.com/pressly/goose/v3/cmd/goose@v3.23.1 \
  -dir internal/platform/db/migrations \
  postgres "host=localhost port=5432 user=postgres password=dev_password_change_in_production dbname=vera_dev sslmode=disable" \
  <command>

# Available commands:
# up          - Apply all pending migrations
# down        - Roll back the last migration
# status      - Show migration status
# version     - Print current migration version
```

## Integration Tests

Integration tests require a running Postgres instance:

```bash
# With docker-compose services running:
make test-integration

# Or manually:
cd backend
go test -tags=integration ./internal/platform/db/...
```

## Stopping Services

```bash
# Stop all services (keeps data)
make dev-down

# Stop and remove volumes (DELETES ALL DATA)
docker compose down -v
```

## Environment Variables

The API server reads configuration from environment variables. For local development, create a `.env` file in the `backend` directory:

```env
DATABASE_URL=postgres://postgres:dev_password_change_in_production@localhost:5432/vera_dev?sslmode=disable
MINIO_ENDPOINT=localhost:9000
MINIO_ACCESS_KEY=minioadmin
MINIO_SECRET_KEY=minioadmin
OIDC_ISSUER=http://localhost:8080/realms/vera
OIDC_CLIENT_ID=vera-local
SESSION_SIGNING_KEY=<generate-with-openssl-rand-base64-32>
```

## Troubleshooting

### Services won't start

```bash
# Check logs
docker compose logs postgres
docker compose logs keycloak

# Recreate services
docker compose down
docker compose up -d
```

### Database connection errors

```bash
# Verify Postgres is ready
docker compose exec postgres pg_isready -U postgres

# Check if database exists
docker compose exec postgres psql -U postgres -l
```

### Port conflicts

If ports 5432, 8080, or 9000 are already in use, modify `docker-compose.yml` to use different host ports.

## Production Differences

This development setup differs from production in several ways:

- Uses `postgres` superuser directly (production uses limited-privilege `app` role)
- Keycloak runs in dev mode (production needs proper TLS and database)
- MinIO uses default credentials (production requires unique credentials)
- No TLS/HTTPS (production requires valid certificates)
- Single-container services (production may run replicated instances)

See `docs/DEPLOYMENT.md` for production deployment guidance.
