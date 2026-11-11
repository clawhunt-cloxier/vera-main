package dataservice_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

// A provider without model-processing permission must never feed records into research, while
// non-model readers (dashboards, watchlist) keep working.
func TestModelProcessingLicenceIsEnforced(t *testing.T) {
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
	var dm1 uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM entities WHERE code='DM0001.DEMO'`).Scan(&dm1); err != nil {
		t.Fatal(err)
	}
	q := dataservice.Query{DatasetIDs: []string{"mkt.daily_bar"}, EntityIDs: []uuid.UUID{dm1}, AsOf: p.Now(), ModelUse: true}

	// Licensed: everything is visible to research.
	if recs, err := st.Query(ctx, q); err != nil || len(recs) == 0 {
		t.Fatalf("licensed provider: want records, got %d err %v", len(recs), err)
	}
	if cov, err := st.Coverage(ctx, []uuid.UUID{dm1}, p.Now()); err != nil || !cov[dm1]["mkt.daily_bar"] {
		t.Fatalf("licensed provider: want coverage, got %v err %v", cov, err)
	}
	if ok, err := st.HasGlobal(ctx, "ref.trading_calendar", p.Now()); err != nil || !ok {
		t.Fatalf("licensed provider: want global data, got %v err %v", ok, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id=$1`, p.ID()); err != nil {
		t.Fatal(err)
	}

	// Not licensed: research paths see nothing.
	if recs, err := st.Query(ctx, q); err != nil || len(recs) != 0 {
		t.Fatalf("unlicensed provider leaked %d records into a model-bound query (err %v)", len(recs), err)
	}
	if cov, err := st.Coverage(ctx, []uuid.UUID{dm1}, p.Now()); err != nil || cov[dm1]["mkt.daily_bar"] {
		t.Fatalf("unlicensed provider still reported as coverage: %v err %v", cov, err)
	}
	if ok, err := st.HasGlobal(ctx, "ref.trading_calendar", p.Now()); err != nil || ok {
		t.Fatalf("unlicensed provider still reported as global data: %v err %v", ok, err)
	}

	// Non-model readers are unaffected.
	q.ModelUse = false
	if recs, err := st.Query(ctx, q); err != nil || len(recs) == 0 {
		t.Fatalf("non-model query must still read the data, got %d err %v", len(recs), err)
	}
}
