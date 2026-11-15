package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestBundleRejectsLegacySnapshotWithDifferentTaskCutoff(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name) VALUES('stock','SYNTHETIC.SH','synthetic') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	later := cutoff.Add(500 * time.Millisecond)
	digest := sha256.Sum256([]byte(later.Format(time.RFC3339)))
	hash := hex.EncodeToString(digest[:])
	if _, err := pool.Exec(ctx, `INSERT INTO snapshots(hash,as_of,record_ids) VALUES($1,$2,$3)`, hash, later, []uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: dataservice.NewStore(pool, nil)}
	subject := dataservice.EntityRow{ID: id}
	if _, err := e.bundleFromSnapshot(ctx, subject, collectOut{Snapshot: hash}, cutoff); err == nil {
		t.Fatal("task accepted a later legacy snapshot")
	}
	if _, err := e.bundleFromSnapshot(ctx, subject, collectOut{Snapshot: hash}, later); err != nil {
		t.Fatalf("matching legacy snapshot was rejected: %v", err)
	}
}

func TestLegacyWorkerRefusesCollectLatest(t *testing.T) {
	ctx := context.Background()
	e, original := leaseTask(t)
	var mode string
	if err := e.Pool.QueryRow(ctx, `SELECT snapshot_mode FROM research_tasks WHERE id=$1`, original.ID).Scan(&mode); err != nil || mode != "fixed_legacy" {
		t.Fatalf("legacy task mode: %q %v", mode, err)
	}
	var planID, taskID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `INSERT INTO research_plans(workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,reason_code,expires_at,snapshot_mode)
		SELECT workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,reason_code,expires_at,'collect_latest'
		FROM research_plans WHERE id=(SELECT plan_id FROM research_tasks WHERE id=$1) RETURNING id`, original.ID).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(ctx, `INSERT INTO research_tasks(workspace_id,plan_id,skill_id,skill_version,subject_id,params,sections,as_of,status,deadline,budget)
		SELECT workspace_id,$2,skill_id,skill_version,subject_id,params,sections,as_of,'QUEUED',deadline,budget FROM research_tasks WHERE id=$1`, original.ID, planID); err == nil {
		t.Fatal("task mode different from plan was accepted")
	}
	if err := e.Pool.QueryRow(ctx, `INSERT INTO research_tasks(workspace_id,plan_id,skill_id,skill_version,subject_id,params,sections,as_of,status,deadline,budget,snapshot_mode)
		SELECT workspace_id,$2,skill_id,skill_version,subject_id,params,sections,as_of,'QUEUED',deadline,budget,'collect_latest'
		FROM research_tasks WHERE id=$1 RETURNING id`, original.ID, planID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(ctx, original.Workspace, taskID, 1); !errors.Is(err, ErrSnapshotModeUnsupported) {
		t.Fatalf("legacy worker accepted new snapshot mode: %v", err)
	}
	var status string
	if err := e.Pool.QueryRow(ctx, `SELECT status FROM research_tasks WHERE id=$1`, taskID).Scan(&status); err != nil || status != "QUEUED" {
		t.Fatalf("worker changed new-mode task: %q %v", status, err)
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE research_tasks SET snapshot_as_of=as_of WHERE id=$1`, taskID); err == nil {
		t.Fatal("partial snapshot binding was accepted")
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE research_tasks SET snapshot_mode='fixed_legacy' WHERE id=$1`, taskID); err == nil {
		t.Fatal("task snapshot mode changed")
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE research_plans SET snapshot_mode='fixed_legacy' WHERE id=$1`, planID); err == nil {
		t.Fatal("plan snapshot mode changed")
	}
	hash := strings.Repeat("a", 64)
	if err := db.InWorkspace(ctx, e.Pool, original.Workspace, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE research_tasks SET snapshot_as_of=as_of,snapshot_manifest_hash=$2 WHERE id=$1`, taskID, hash)
		return err
	}); err == nil {
		t.Fatal("workspace role forged a snapshot binding")
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE research_tasks SET snapshot_as_of=as_of,snapshot_manifest_hash=$2 WHERE id=$1`, taskID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE research_tasks SET snapshot_manifest_hash=$2 WHERE id=$1`, taskID, strings.Repeat("b", 64)); err == nil {
		t.Fatal("frozen snapshot binding was rewritten")
	}
	sqlDB := stdlib.OpenDBFromPool(e.Pool)
	defer sqlDB.Close()
	migrator, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../platform/db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Down(ctx); err == nil {
		t.Fatal("rollback discarded a collect_latest task")
	}
}
