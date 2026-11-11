package db_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestRowLevelSecurityIsolatesWorkspaces(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var wsA, wsB uuid.UUID
	err := db.InSystem(ctx, pool, func(tx pgx.Tx) error {
		for i, ws := range []*uuid.UUID{&wsA, &wsB} {
			var u uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('t',$1) RETURNING id`, fmt.Sprint(i)).Scan(&u); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, u).Scan(ws); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(ws uuid.UUID) error {
		return db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys VALUES($1,'POST','/x','k','h',200,'{}',now()+interval '1 day')`, ws)
			return err
		})
	}
	if err := insert(wsA); err != nil {
		t.Fatal(err)
	}
	// Writing another workspace's row from wsB's context must be rejected by the policy.
	err = db.InWorkspace(ctx, pool, wsB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys VALUES($1,'POST','/x','k2','h',200,'{}',now())`, wsA)
		return err
	})
	if err == nil {
		t.Fatal("cross-workspace insert must fail")
	}
	var n int
	err = db.InWorkspace(ctx, pool, wsB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("wsB sees %d rows (err %v)", n, err)
	}
	if err := db.InWorkspace(ctx, pool, uuid.Nil, func(pgx.Tx) error { return nil }); err == nil {
		t.Fatal("nil workspace must be rejected")
	}
}
