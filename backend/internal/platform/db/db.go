// Package db owns the PostgreSQL pool, migrations and workspace-scoped transactions.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects and verifies the pool.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies River's schema and the application migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	rm, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	if _, err := rm.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// InWorkspace runs fn in a transaction as vera_app with app.workspace_id set, so RLS applies.
func InWorkspace(ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID, fn func(pgx.Tx) error) error {
	if ws == uuid.Nil {
		return errors.New("db: workspace id required")
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE vera_app"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.workspace_id', $1, true)", ws.String()); err != nil {
			return err
		}
		return fn(tx)
	})
}

// InSystem runs fn in a transaction with the connecting role (platform jobs, admin CLI, catalog writes).
func InSystem(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, fn)
}
