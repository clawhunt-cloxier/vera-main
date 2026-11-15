package dataservice_test

import (
	"context"
	"strings"
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

func TestFreezeQualityCheckIsAtomic(t *testing.T) {
	// Verify that quality_state is checked in the same query that locks the
	// rows (FOR SHARE), so a concurrent quarantine cannot slip between check
	// and lock.  We test the observable outcome: Freeze rejects a record that
	// is QUARANTINED at the moment of locking, with a recognisable error.
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

	recs, err := st.Query(ctx, dataservice.Query{
		DatasetIDs: []string{"mkt.daily_bar"},
		AsOf:       p.Now(),
	})
	if err != nil || len(recs) < 2 {
		t.Fatalf("need at least 2 records: %v", err)
	}
	goodID := recs[0].ID
	targetID := recs[1].ID

	// Quarantine the target directly — simulates what happens between a
	// two-step (check then lock) implementation during the window.
	if _, err := pool.Exec(ctx, `UPDATE records SET quality_state='QUARANTINED' WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}

	// Freeze including quarantined record must fail with a quality error.
	_, ferr := st.Freeze(ctx, p.Now(), []uuid.UUID{goodID, targetID})
	if ferr == nil {
		t.Fatal("Freeze accepted a QUARANTINED record")
	}
	if !strings.Contains(ferr.Error(), "QUARANTINED") {
		t.Fatalf("expected QUARANTINED error, got: %v", ferr)
	}

	// Good record alone must still succeed.
	if _, err := st.Freeze(ctx, p.Now(), []uuid.UUID{goodID}); err != nil {
		t.Fatalf("Freeze rejected a PASS record: %v", err)
	}
}

func TestSyncRejectsLineageConflict(t *testing.T) {
	// A re-sync with same (dataset, record_key, version) but different
	// raw_ref/raw_sha256 must fail — lineage fields are part of the immutable
	// record identity.
	pool := dbtest.New(t)
	ctx := context.Background()
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	st := dataservice.NewStore(pool, cat)

	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := dataservice.Record{
		DatasetID:   "ref.trading_calendar",
		RecordKey:   "CNSESH:2026-01-01",
		Period:      &day,
		AvailableAt: day,
		Origin:      "exchange",
		SourceTier:  "L2",
		Payload:     map[string]any{"trade_date": "2026-01-01", "is_open": true},
		RawRef:      "s3://bucket/raw/2026-01-01.json",
		RawSHA256:   "abc123",
	}
	p1 := &singleRecordProvider{id: "lineage-vendor-1", record: rec}
	if _, err := st.Sync(ctx, p1); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	// Same record key but different raw lineage.
	rec2 := rec
	rec2.RawRef = "s3://bucket/raw/2026-01-01-v2.json"
	rec2.RawSHA256 = "def456"
	p2 := &singleRecordProvider{id: "lineage-vendor-1", record: rec2}
	if _, err := st.Sync(ctx, p2); err == nil {
		t.Fatal("Sync accepted conflicting lineage for same record key")
	}
}

// singleRecordProvider is a minimal Provider for testing immutable record conflicts.
type singleRecordProvider struct {
	id     string
	record dataservice.Record
}

func (p *singleRecordProvider) ID() string                 { return p.id }
func (p *singleRecordProvider) Name() string               { return p.id }
func (p *singleRecordProvider) Synthetic() bool            { return false }
func (p *singleRecordProvider) AllowModelProcessing() bool { return true }
func (p *singleRecordProvider) Basis() map[string]string   { return nil }
func (p *singleRecordProvider) Now() time.Time             { return time.Now() }
func (p *singleRecordProvider) Entities(context.Context) ([]dataservice.Entity, error) {
	return nil, nil
}
func (p *singleRecordProvider) Capabilities(context.Context) ([]dataservice.Capability, error) {
	return nil, nil
}
func (p *singleRecordProvider) Records(context.Context) ([]dataservice.Record, error) {
	return []dataservice.Record{p.record}, nil
}

func TestFreezeConcurrentQuarantineRace(t *testing.T) {
	// Prove that quality_state is checked atomically with the FOR SHARE lock.
	// Strategy: start a transaction that holds an UPDATE lock on a record
	// (simulating a quarantine in progress), then start Freeze on that same
	// record. Because Freeze's FOR SHARE needs to read the row, it must wait
	// until the UPDATE lock is released. We release it with the record already
	// QUARANTINED, so Freeze must see QUARANTINED and reject — not the PASS
	// it would have seen before the lock.
	//
	// In a two-step implementation (check quality, then lock separately) the
	// check would have run before the lock, seen PASS, and incorrectly allowed
	// the freeze to proceed. The atomic implementation cannot observe PASS and
	// then have QUARANTINED appear after acquiring the lock.
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

	recs, err := st.Query(ctx, dataservice.Query{
		DatasetIDs: []string{"mkt.daily_bar"},
		AsOf:       p.Now(),
	})
	if err != nil || len(recs) == 0 {
		t.Fatalf("need records: %v", err)
	}
	target := recs[0].ID

	// Open a transaction that immediately takes an UPDATE lock on the target
	// row. We hold it open until Freeze is definitely waiting for the lock.
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Release()

	tx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Lock the row and quarantine it — but don't commit yet.
	if _, err := tx.Exec(ctx, `UPDATE records SET quality_state='QUARANTINED' WHERE id=$1`, target); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}

	// Channel to receive the Freeze result once the lock is released.
	type result struct {
		hash string
		err  error
	}
	done := make(chan result, 1)

	go func() {
		h, e := st.Freeze(ctx, p.Now(), []uuid.UUID{target})
		done <- result{h, e}
	}()

	// Give Freeze time to reach the FOR SHARE and block on our lock.
	time.Sleep(100 * time.Millisecond)

	// Commit the quarantine — this releases the UPDATE lock.
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	r := <-done
	if r.err == nil {
		t.Fatal("Freeze succeeded despite record being QUARANTINED when the lock was released")
	}
	if !strings.Contains(r.err.Error(), "QUARANTINED") {
		t.Fatalf("expected QUARANTINED error, got: %v", r.err)
	}
}
