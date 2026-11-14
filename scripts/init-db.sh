#!/bin/bash
set -e

# Create Keycloak schema for separation from application data
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE SCHEMA IF NOT EXISTS keycloak;
    GRANT ALL ON SCHEMA keycloak TO postgres;
EOSQL

echo "Keycloak schema created"
