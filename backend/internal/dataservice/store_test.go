package dataservice_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestSyncAndPointInTimeQuery(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	p, err := fixture.New("../../../tests/fixtures/demo-universe.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	st := dataservice.NewStore(pool, cat)
	n, err := st.Sync(ctx, p)
	if err != nil || n < 10000 {
		t.Fatalf("sync n=%d err=%v", n, err)
	}
	again, err := st.Sync(ctx, p)
	if err != nil || again != 0 {
		t.Fatalf("second sync must be a no-op, inserted %d err %v", again, err)
	}
	var dm9 uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM entities WHERE code='DM0009.DEMO'`).Scan(&dm9); err != nil {
		t.Fatal(err)
	}
	fy := time.Date(p.Now().Year()-1, 12, 31, 0, 0, 0, 0, time.UTC)
	q := dataservice.Query{DatasetID: "fin.statement", EntityIDs: []uuid.UUID{dm9}, From: &fy, To: &fy, AsOf: p.Now()}
	recs, err := st.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	var restated, original int
	for _, r := range recs {
		if r.Payload["item_code"] == "IS.net_profit_parent" && r.Payload["report_type"] == "FY" {
			if r.Version == 2 {
				restated++
			} else {
				original++
			}
		}
	}
	if restated != 1 || original != 0 {
		t.Fatalf("as of now want only the restated revision, got restated=%d original=%d", restated, original)
	}
	// Before the correction was published only the original revision is visible.
	early := time.Date(p.Now().Year(), 5, 1, 0, 0, 0, 0, time.UTC)
	q.AsOf = early
	recs, _ = st.Query(ctx, q)
	for _, r := range recs {
		if r.Payload["item_code"] == "IS.net_profit_parent" && r.Payload["report_type"] == "FY" && r.Version != 1 {
			t.Fatalf("point-in-time leak: version %d visible at %s", r.Version, early)
		}
	}
	h1, err := st.Freeze(ctx, p.Now(), []uuid.UUID{recs[0].ID, recs[1].ID})
	h2, _ := st.Freeze(ctx, p.Now(), []uuid.UUID{recs[1].ID, recs[0].ID})
	if err != nil || h1 != h2 {
		t.Fatalf("freeze must be order independent: %s %s %v", h1, h2, err)
	}
}
