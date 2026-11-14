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

func TestVendorStoreExcludesExistingSyntheticData(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	_, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic,allow_model_processing) VALUES
 ('f','fixture','fixture',true,true),('v','vendor','vendor',false,true);
 INSERT INTO entities(kind,code,name,synthetic) VALUES ('stock','FAKE.SH','fake',true),('stock','REAL.SH','real',false);
 INSERT INTO entity_aliases(entity_id,alias) SELECT id,lower(name) FROM entities;
 INSERT INTO records(dataset_id,entity_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash)
 SELECT 'mkt.daily_bar', id, 'fake-bar','fake-bar','2026-01-01','f','exchange','L2','{}','1' FROM entities WHERE code='FAKE.SH';
 INSERT INTO records(dataset_id,entity_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash)
 SELECT 'mkt.daily_bar', id, 'bad-vendor-bar','bad-vendor-bar','2026-01-01','v','exchange','L2','{}','2' FROM entities WHERE code='FAKE.SH';
 INSERT INTO records(dataset_id,entity_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash)
 SELECT 'mkt.daily_bar', id, 'real-bar','real-bar','2026-01-01','v','exchange','L2','{}','3' FROM entities WHERE code='REAL.SH';
 INSERT INTO records(dataset_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash)
 VALUES ('ref.trading_calendar','fake-cal','fake-cal','2026-01-01','f','exchange','L2','{}','4')`)
	if err != nil {
		t.Fatal(err)
	}
	var fakeEntity, realEntity, fakeRecord uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM entities WHERE code='FAKE.SH'`).Scan(&fakeEntity); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM entities WHERE code='REAL.SH'`).Scan(&realEntity); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM records WHERE record_key='fake-bar'`).Scan(&fakeRecord); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	demo := dataservice.NewStore(pool, nil)
	oldHash, err := demo.Freeze(ctx, asOf, []uuid.UUID{fakeRecord})
	if err != nil {
		t.Fatal(err)
	}
	store := dataservice.NewVendorStore(pool, nil)
	q := dataservice.Query{DatasetIDs: []string{"mkt.daily_bar"}, AsOf: asOf, ModelUse: true}
	records, err := store.Query(ctx, q)
	if err != nil || len(records) != 1 || records[0].RecordKey != "real-bar" {
		t.Fatalf("vendor query: %v, count %d", err, len(records))
	}
	coverage, err := store.Coverage(ctx, []uuid.UUID{fakeEntity, realEntity}, asOf)
	if err != nil || coverage[fakeEntity]["mkt.daily_bar"] || !coverage[realEntity]["mkt.daily_bar"] {
		t.Fatalf("coverage: %v", err)
	}
	if ok, err := store.HasGlobal(ctx, "ref.trading_calendar", asOf); err != nil || ok {
		t.Fatalf("synthetic global data visible: %v", err)
	}
	if _, err := store.Freeze(ctx, asOf, []uuid.UUID{fakeRecord}); err == nil {
		t.Fatal("synthetic record frozen")
	}
	if _, _, err := store.Snapshot(ctx, oldHash); err == nil {
		t.Fatal("synthetic legacy snapshot accepted")
	}
	if got, err := store.Get(ctx, []uuid.UUID{fakeRecord, records[0].ID}); err != nil || len(got) != 1 {
		t.Fatalf("synthetic evidence visible: %v", err)
	}
	if got, err := store.Entities(ctx, []uuid.UUID{fakeEntity, realEntity}); err != nil || len(got) != 1 {
		t.Fatalf("synthetic entity visible: %v", err)
	}
	if _, err := store.EntityByCode(ctx, "FAKE.SH"); err == nil {
		t.Fatal("synthetic entity resolved")
	}
	if got, err := store.EntitiesByKind(ctx, "stock"); err != nil || len(got) != 1 {
		t.Fatalf("synthetic entity listed: %v", err)
	}
	if got, err := store.Search(ctx, "fake", "stock", 10); err != nil || len(got) != 0 {
		t.Fatalf("synthetic entity searched: %v", err)
	}
	if got, err := store.Mentioned(ctx, "fake"); err != nil || len(got) != 0 {
		t.Fatalf("synthetic entity mentioned: %v", err)
	}
	p, err := fixture.New("../../../tests/fixtures/demo-universe.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sync(ctx, p); err == nil {
		t.Fatal("synthetic provider synced")
	}
}
