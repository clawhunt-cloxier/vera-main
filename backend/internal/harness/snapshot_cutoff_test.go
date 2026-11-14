package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
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
