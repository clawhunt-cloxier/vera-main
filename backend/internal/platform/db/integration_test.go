//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabaseSchema(t *testing.T) {
	ctx := context.Background()
	connStr := "host=localhost port=5432 user=postgres password=dev_password_change_in_production dbname=vera_dev sslmode=disable"

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	// Test 1: All expected tables exist
	expectedTables := []string{
		"invites", "entities", "entity_aliases", "providers", "capabilities",
		"records", "snapshots", "audit_events",
		"users", "workspaces", "sessions", "oidc_logins",
		"research_plans", "research_tasks", "task_events", "task_steps",
		"model_calls", "claims", "report_versions",
		"watchlist_items", "watchlist_snapshots", "watchlist_alerts",
		"feedback", "idempotency_keys",
		"usage_ledger", "publications", "raw_objects", "record_conflicts",
		"snapshot_items", "fact_sheets",
	}

	for _, table := range expectedTables {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_schema = 'public'
				AND table_name = $1
			)
		`, table).Scan(&exists)

		if err != nil {
			t.Errorf("Failed to check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("Table %s does not exist", table)
		}
	}

	// Test 2: RLS is enabled on private tables
	privateTables := []string{
		"research_plans", "research_tasks", "task_events", "task_steps",
		"model_calls", "claims", "report_versions",
		"watchlist_items", "watchlist_snapshots", "feedback", "idempotency_keys",
	}

	for _, table := range privateTables {
		var rlsEnabled bool
		err := pool.QueryRow(ctx, `
			SELECT relrowsecurity
			FROM pg_class
			WHERE relname = $1
		`, table).Scan(&rlsEnabled)

		if err != nil {
			t.Errorf("Failed to check RLS for %s: %v", table, err)
		}
		if !rlsEnabled {
			t.Errorf("RLS not enabled on %s", table)
		}
	}

	// Test 3: Foreign key constraints exist
	constraints := []struct {
		table  string
		column string
		fkTo   string
	}{
		{"workspaces", "owner_user_id", "users"},
		{"sessions", "user_id", "users"},
		{"research_tasks", "workspace_id", "workspaces"},
		{"model_calls", "workspace_id", "workspaces"},
		{"usage_ledger", "workspace_id", "workspaces"},
		{"usage_ledger", "call_id", "model_calls"},
		{"fact_sheets", "task_id", "research_tasks"},
		{"watchlist_alerts", "item_id", "watchlist_items"},
	}

	for _, c := range constraints {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu
					ON tc.constraint_name = kcu.constraint_name
				JOIN information_schema.constraint_column_usage ccu
					ON ccu.constraint_name = tc.constraint_name
				WHERE tc.table_name = $1
					AND kcu.column_name = $2
					AND ccu.table_name = $3
					AND tc.constraint_type = 'FOREIGN KEY'
			)
		`, c.table, c.column, c.fkTo).Scan(&exists)

		if err != nil {
			t.Errorf("Failed to check FK %s.%s -> %s: %v", c.table, c.column, c.fkTo, err)
		}
		if !exists {
			t.Errorf("Missing FK constraint: %s.%s -> %s", c.table, c.column, c.fkTo)
		}
	}
}

func TestBasicCRUD(t *testing.T) {
	ctx := context.Background()
	connStr := "host=localhost port=5432 user=postgres password=dev_password_change_in_production dbname=vera_dev sslmode=disable"

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	// Test basic CRUD operations
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	// Create a test user
	var userID string
	err = tx.QueryRow(ctx, `
		INSERT INTO users (issuer, subject, email, display_name)
		VALUES ('test-issuer', 'test-subject', 'test@example.com', 'Test User')
		RETURNING id
	`).Scan(&userID)

	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create workspace
	var wsID string
	err = tx.QueryRow(ctx, `
		INSERT INTO workspaces (owner_user_id)
		VALUES ($1)
		RETURNING id
	`, userID).Scan(&wsID)

	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}

	// Verify workspace was created
	var count int
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM workspaces WHERE id = $1
	`, wsID).Scan(&count)

	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("Expected 1 workspace, got %d", count)
	}
}
