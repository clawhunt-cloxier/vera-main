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
	q := dataservice.Query{DatasetIDs: []string{"fin.statement"}, EntityIDs: []uuid.UUID{dm9}, From: &fy, To: &fy, AsOf: p.Now()}
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

func TestFreezeRejectsQuarantinedRecords(t *testing.T) {
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
	if _, err := st.Sync(ctx, p); err != nil {
		t.Fatal(err)
	}

	// Pick a real record and get its ID.
	recs, err := st.Query(ctx, dataservice.Query{
		DatasetIDs: []string{"mkt.daily_bar"},
		AsOf:       p.Now(),
		ModelUse:   true,
	})
	if err != nil || len(recs) == 0 {
		t.Fatalf("no records: %v", err)
	}
	goodID := recs[0].ID

	// Directly update one record to QUARANTINED to simulate a bad record.
	quarantinedID := recs[1].ID
	if _, err := pool.Exec(ctx, `UPDATE records SET quality_state='QUARANTINED' WHERE id=$1`, quarantinedID); err != nil {
		t.Fatal(err)
	}

	// Freeze with only the good record must succeed.
	hash, err := st.Freeze(ctx, p.Now(), []uuid.UUID{goodID})
	if err != nil {
		t.Fatalf("freeze good record failed: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash")
	}

	// Freeze including the quarantined record must fail.
	_, err = st.Freeze(ctx, p.Now(), []uuid.UUID{goodID, quarantinedID})
	if err == nil {
		t.Fatal("freeze should have rejected QUARANTINED record but did not")
	}
}

func TestCoverageExcludesQuarantined(t *testing.T) {
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
	if _, err := st.Sync(ctx, p); err != nil {
		t.Fatal(err)
	}

	// Get all entity IDs.
	ents, err := st.Search(ctx, "", "", 100)
	if err != nil || len(ents) == 0 {
		t.Fatalf("no entities: %v", err)
	}
	ids := make([]uuid.UUID, len(ents))
	for i, e := range ents {
		ids[i] = e.ID
	}

	coverageBefore, err := st.Coverage(ctx, ids, p.Now())
	if err != nil {
		t.Fatal(err)
	}

	// Quarantine all daily_bar records for the first entity.
	firstID := ids[0]
	if _, err := pool.Exec(ctx, `UPDATE records SET quality_state='QUARANTINED' WHERE dataset_id='mkt.daily_bar' AND entity_id=$1`, firstID); err != nil {
		t.Fatal(err)
	}

	coverageAfter, err := st.Coverage(ctx, ids, p.Now())
	if err != nil {
		t.Fatal(err)
	}

	// First entity should no longer show daily_bar coverage.
	if coverageAfter[firstID]["mkt.daily_bar"] {
		t.Fatal("QUARANTINED records should not count toward Coverage")
	}
	// Other entities should be unaffected.
	for id, ds := range coverageBefore {
		if id == firstID {
			continue
		}
		for d := range ds {
			if !coverageAfter[id][d] {
				t.Fatalf("entity %s lost coverage for %s unexpectedly", id, d)
			}
		}
	}
}
