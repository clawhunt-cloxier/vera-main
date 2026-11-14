package dataservice_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

type collisionProvider struct {
	id      string
	records []dataservice.Record
}

func (p collisionProvider) ID() string                                             { return p.id }
func (p collisionProvider) Name() string                                           { return p.id }
func (p collisionProvider) Synthetic() bool                                        { return false }
func (p collisionProvider) AllowModelProcessing() bool                             { return true }
func (p collisionProvider) Basis() map[string]string                               { return nil }
func (p collisionProvider) Now() time.Time                                         { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
func (p collisionProvider) Entities(context.Context) ([]dataservice.Entity, error) { return nil, nil }
func (p collisionProvider) Records(context.Context) ([]dataservice.Record, error) {
	return p.records, nil
}
func (p collisionProvider) Capabilities(context.Context) ([]dataservice.Capability, error) {
	return nil, nil
}

func TestSyncRejectsConflictingImmutableRecordsAtomically(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	store := dataservice.NewVendorStore(pool, cat)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := dataservice.Record{DatasetID: "ref.trading_calendar", RecordKey: "CNSESH:2026-01-01", Period: &day, AvailableAt: day, Origin: "exchange", SourceTier: "L2", Payload: map[string]any{"trade_date": "2026-01-01", "is_open": true}}
	a := collisionProvider{id: "vendor-a", records: []dataservice.Record{rec}}
	if n, err := store.Sync(ctx, a); err != nil || n != 1 {
		t.Fatalf("first sync: %d %v", n, err)
	}
	if n, err := store.Sync(ctx, a); err != nil || n != 0 {
		t.Fatalf("idempotent sync: %d %v", n, err)
	}
	changed := rec
	changed.Basis = "other basis"
	if n, err := store.Sync(ctx, collisionProvider{id: "vendor-a", records: []dataservice.Record{changed}}); err == nil || n != 0 {
		t.Fatal("changed provenance accepted")
	}
	another := rec
	another.RecordKey = "CNSESH:2026-01-02"
	b := collisionProvider{id: "vendor-b", records: []dataservice.Record{rec, another}}
	if n, err := store.Sync(ctx, b); err == nil || n != 0 {
		t.Fatal("cross-provider collision accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM providers WHERE id='vendor-b') + (SELECT count(*) FROM records WHERE record_key='CNSESH:2026-01-02')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed sync was partially committed: count=%d err=%v", count, err)
	}
}

func TestConcurrentSyncConflictingSources(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	store := dataservice.NewVendorStore(pool, cat)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := dataservice.Record{DatasetID: "ref.trading_calendar", RecordKey: "CNSESH:2026-01-01", Period: &day, AvailableAt: day, Origin: "exchange", SourceTier: "L2", Payload: map[string]any{"trade_date": "2026-01-01", "is_open": true}}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range []string{"vendor-a", "vendor-b"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, errs[i] = store.Sync(ctx, collisionProvider{id: id, records: []dataservice.Record{rec}})
		}(i, id)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("expected one winner, got %v", errs)
	}
	var providers, records int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM providers),(SELECT count(*) FROM records)`).Scan(&providers, &records); err != nil || providers != 1 || records != 1 {
		t.Fatalf("unexpected committed state: providers=%d records=%d err=%v", providers, records, err)
	}
}
