package dataservice_test

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

func TestVendorStoreWithholdsIntradayFromLegacyPath(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('intraday-test','synthetic test','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	var entityID, recordID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic) VALUES('stock','INTRADAY.TEST','Synthetic',false) RETURNING id`).Scan(&entityID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO records(dataset_id,entity_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash)
		VALUES('mkt.intraday_quote',$1,'intraday-test','intraday-test',clock_timestamp()-interval '1 minute','intraday-test','exchange','L2','{}','synthetic') RETURNING id`, entityID).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO records(dataset_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash)
		VALUES('mkt.intraday_index_quote','intraday-index','intraday-index',clock_timestamp()-interval '1 minute','intraday-test','exchange','L2','{}','synthetic')`); err != nil {
		t.Fatal(err)
	}
	store := dataservice.NewVendorStore(pool, nil)
	cutoff := time.Now().Add(time.Minute)
	rows, err := store.Query(ctx, dataservice.Query{DatasetIDs: []string{"mkt.intraday_quote"}, EntityIDs: []uuid.UUID{entityID}, AsOf: cutoff, ModelUse: true})
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy query exposed intraday data: rows=%d err=%v", len(rows), err)
	}
	coverage, err := store.Coverage(ctx, []uuid.UUID{entityID}, cutoff)
	if err != nil || coverage[entityID]["mkt.intraday_quote"] {
		t.Fatalf("legacy coverage exposed intraday data: %v", err)
	}
	if ok, err := store.HasGlobal(ctx, "mkt.intraday_index_quote", cutoff); err != nil || ok {
		t.Fatalf("legacy global coverage exposed intraday data: %v", err)
	}
	if _, err := store.Freeze(ctx, cutoff, []uuid.UUID{recordID}); err == nil {
		t.Fatal("legacy freeze accepted an intraday record without a publication-aware snapshot")
	}
	legacyHash, err := dataservice.NewStore(pool, nil).Freeze(ctx, cutoff, []uuid.UUID{recordID})
	if err != nil {
		t.Fatalf("construct historical test snapshot: %v", err)
	}
	if _, _, err := store.Snapshot(ctx, legacyHash); err == nil {
		t.Fatal("legacy snapshot replay exposed an intraday record")
	}
	got, err := store.Get(ctx, []uuid.UUID{recordID})
	if err != nil || len(got) != 0 {
		t.Fatalf("legacy evidence lookup exposed intraday data: %v", err)
	}
}

func TestQuarantinedRevisionDoesNotRestoreSupersededValues(t *testing.T) {
	ctx := t.Context()
	api, worker, pool := requestedStores(t)
	batch := syntheticRequestedBatch()
	collect := func(context.Context) (dataservice.Provider, error) { return batch, nil }
	if _, err := worker.PrepareRequestedSubject(ctx, coldTarget, collect); err != nil {
		t.Fatal(err)
	}
	entity, err := api.EntityByCode(ctx, coldTarget)
	if err != nil {
		t.Fatal(err)
	}
	before, err := api.PublicationCutoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	query := dataservice.Query{DatasetIDs: []string{"mkt.daily_bar"}, EntityIDs: []uuid.UUID{entity.ID}, AsOf: before, ModelUse: true}
	initial, err := api.Query(ctx, query)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial row: %v %v", initial, err)
	}
	ids := []uuid.UUID{initial[0].ID}
	hash, err := api.Freeze(ctx, before, ids)
	if err != nil {
		t.Fatal(err)
	}
	batch.records[0].QualityState = "QUARANTINED"
	if _, err := worker.SyncObserved(ctx, "tushare", collect); err != nil {
		t.Fatal(err)
	}
	now, err := api.PublicationCutoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	query.AsOf = now
	if rows, err := api.Query(ctx, query); err != nil || len(rows) != 0 {
		t.Errorf("current research returned %d rows: %v", len(rows), err)
	}
	if coverage, err := api.Coverage(ctx, query.EntityIDs, now); err != nil || coverage[entity.ID]["mkt.daily_bar"] {
		t.Errorf("superseded version counted for coverage: %v %v", coverage, err)
	}
	if _, err := api.Freeze(ctx, now, ids); err == nil {
		t.Error("new snapshot accepted a superseded row")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := api.ObservationBar(ctx, tx, entity.ID, coldTarget, now, nil); err == nil {
		t.Error("mixed observation selected the superseded close")
	}
	if ok, err := api.ResearchSubjectPrepared(ctx, coldTarget); err != nil || ok {
		t.Errorf("quarantined cold batch reported ready: %v %v", ok, err)
	}
	ref := &dataservice.ObservationRef{ID: ids[0], DatasetID: initial[0].DatasetID, ContentHash: initial[0].ContentHash}
	if _, err := api.ObservationBar(ctx, tx, entity.ID, coldTarget, before, ref); err != nil {
		t.Fatalf("could not replay original observation: %v", err)
	}
	if _, rows, err := api.Snapshot(ctx, hash); err != nil || len(rows) != 1 || rows[0].ID != ids[0] {
		t.Fatalf("frozen snapshot changed: %v %v", rows, err)
	}
	query.AsOf = before
	if rows, err := api.Query(ctx, query); err != nil || len(rows) != 1 || rows[0].ID != ids[0] {
		t.Fatalf("later quarantine hid historical value: %v %v", rows, err)
	}
}

func TestQueryRespectsDisclosureTimeBeforeSelectingRevision(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('publication-test','synthetic','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO records(dataset_id,record_key,logical_key,record_version,published_at,available_at,provider_id,origin,source_tier,payload,content_hash) VALUES
		('fin.statement','original','statement-revision',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','a'),
		('fin.statement','future-revision','statement-revision',2,'2026-01-03T00:00:00Z','2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','b'),
		('fin.statement','future-available','future-available',1,'2026-01-01T00:00:00Z','2026-01-03T00:00:00Z','publication-test','exchange','L2','{}','h'),
		('fin.statement','missing-date','statement-missing',1,NULL,'2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','c'),
		('fin.segment','missing-segment-date','segment-missing',1,NULL,'2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','d'),
		('fin.segment','at-cutoff','segment-cutoff',1,'2026-01-02T00:00:00Z','2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','e'),
		('news.item','future-news','future-news',1,'2026-01-03T00:00:00Z','2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','f'),
		('mkt.daily_bar','no-publication-date','market',1,NULL,'2026-01-01T00:00:00Z','publication-test','exchange','L2','{}','g')`)
	if err != nil {
		t.Fatal(err)
	}
	var entity uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name) VALUES('stock','SYNTHETIC.SH','synthetic') RETURNING id`).Scan(&entity); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE records SET entity_id=$1 WHERE record_key IN ('missing-date','missing-segment-date')`, entity); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	store := dataservice.NewStore(pool, nil)
	records, err := store.Query(ctx, dataservice.Query{
		DatasetIDs: []string{"fin.statement", "fin.segment", "news.item", "mkt.daily_bar"}, AsOf: cutoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int, len(records))
	for _, record := range records {
		got[record.RecordKey] = record.Version
	}
	if len(got) != 3 || got["original"] != 1 || got["at-cutoff"] != 1 || got["no-publication-date"] != 1 {
		t.Fatalf("unexpected records at cutoff: %v", got)
	}
	coverage, err := store.Coverage(ctx, []uuid.UUID{entity}, cutoff)
	if err != nil || coverage[entity]["fin.statement"] || coverage[entity]["fin.segment"] {
		t.Fatalf("coverage included unknown disclosure times: %v", err)
	}
	if available, err := store.HasGlobal(ctx, "news.item", cutoff); err != nil || available {
		t.Fatalf("global coverage included future disclosure: %v", err)
	}
	if available, err := store.HasGlobal(ctx, "mkt.daily_bar", cutoff); err != nil || !available {
		t.Fatalf("ordinary market data excluded: %v", err)
	}
	var valid []uuid.UUID
	for _, record := range records {
		valid = append(valid, record.ID)
	}
	hash, err := store.Freeze(ctx, cutoff, valid)
	if err != nil {
		t.Fatal(err)
	}
	storedCutoff, snapshot, err := store.Snapshot(ctx, hash)
	if err != nil || !storedCutoff.Equal(cutoff) || len(snapshot) != len(valid) {
		t.Fatalf("valid snapshot: %v", err)
	}
	otherHash, err := store.Freeze(ctx, cutoff.Add(500*time.Millisecond), valid)
	if err != nil || otherHash == hash {
		t.Fatalf("subsecond cutoffs collided: %v", err)
	}
	sameHash, err := store.Freeze(ctx, cutoff.Add(500*time.Millisecond+100*time.Nanosecond), valid)
	if err != nil || sameHash != otherHash {
		t.Fatalf("timestamps within database precision produced different hashes: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE snapshots SET as_of=as_of+interval '1 second' WHERE hash=$1`, otherHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Snapshot(ctx, otherHash); err == nil {
		t.Fatal("snapshot replay accepted a modified cutoff")
	}
	legacyCutoff := cutoff.Add(100 * time.Millisecond)
	legacyIDs := []uuid.UUID{valid[0]}
	legacyHasher := sha256.New()
	_, _ = legacyHasher.Write([]byte(legacyCutoff.UTC().Format(time.RFC3339)))
	_, _ = legacyHasher.Write(legacyIDs[0][:])
	legacyHash := hex.EncodeToString(legacyHasher.Sum(nil))
	if _, err := pool.Exec(ctx, `INSERT INTO snapshots(hash,as_of,record_ids) VALUES($1,$2,$3)`, legacyHash, legacyCutoff, legacyIDs); err != nil {
		t.Fatal(err)
	}
	if gotHash, err := store.Freeze(ctx, legacyCutoff, legacyIDs); err != nil || gotHash != legacyHash {
		t.Fatalf("matching legacy snapshot was not preserved: %v", err)
	}
	wholeSecondHash, err := store.Freeze(ctx, cutoff, legacyIDs)
	if err != nil || wholeSecondHash == legacyHash {
		t.Fatalf("whole-second cutoff reused a later legacy snapshot: %v", err)
	}
	if gotCutoff, _, err := store.Snapshot(ctx, wholeSecondHash); err != nil || !gotCutoff.Equal(cutoff) {
		t.Fatalf("whole-second snapshot cutoff changed: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE snapshots SET record_ids=$2 WHERE hash=$1`, wholeSecondHash, []uuid.UUID{valid[1]}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Snapshot(ctx, wholeSecondHash); err == nil {
		t.Fatal("snapshot replay accepted a modified record list")
	}
	if gotHash, err := store.Freeze(ctx, legacyCutoff.Add(100*time.Millisecond), legacyIDs); err != nil || gotHash == legacyHash {
		t.Fatalf("different cutoff reused legacy hash: %v", err)
	}
	poisonCutoff := cutoff.Add(700 * time.Millisecond)
	poisonHasher := sha256.New()
	_, _ = poisonHasher.Write([]byte("vera.snapshot.v2\x00"))
	_, _ = poisonHasher.Write([]byte(poisonCutoff.UTC().Format(time.RFC3339Nano)))
	_, _ = poisonHasher.Write(legacyIDs[0][:])
	poisonHash := hex.EncodeToString(poisonHasher.Sum(nil))
	if _, err := pool.Exec(ctx, `INSERT INTO snapshots(hash,as_of,record_ids) VALUES($1,$2,$3)`, poisonHash, poisonCutoff.Add(time.Second), legacyIDs); err != nil {
		t.Fatal(err)
	}
	if gotHash, err := store.Freeze(ctx, poisonCutoff, legacyIDs); err == nil || gotHash != "" {
		t.Fatal("snapshot hash with conflicting stored cutoff was accepted")
	}
	for _, key := range []string{"future-revision", "future-available", "missing-date", "missing-segment-date", "future-news"} {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM records WHERE record_key=$1`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if hash, err := store.Freeze(ctx, cutoff, []uuid.UUID{id}); err == nil || hash != "" {
			t.Fatalf("freeze accepted undisclosed record %q", key)
		}
	}
	if hash, err := store.Freeze(ctx, cutoff, []uuid.UUID{uuid.New()}); err == nil || hash != "" {
		t.Fatal("freeze accepted unknown record")
	}
	var futureID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM records WHERE record_key='future-revision'`).Scan(&futureID); err != nil {
		t.Fatal(err)
	}
	invalidHasher := sha256.New()
	_, _ = invalidHasher.Write([]byte(cutoff.UTC().Format(time.RFC3339)))
	_, _ = invalidHasher.Write(futureID[:])
	invalidHash := hex.EncodeToString(invalidHasher.Sum(nil))
	if _, err := pool.Exec(ctx, `INSERT INTO snapshots(hash,as_of,record_ids) VALUES($1,$2,$3)`, invalidHash, cutoff, []uuid.UUID{futureID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Snapshot(ctx, invalidHash); err == nil {
		t.Fatal("snapshot replay accepted an undisclosed record")
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='publication-test'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Snapshot(ctx, hash); err == nil {
		t.Fatal("snapshot replay accepted revoked model permission")
	}
	if _, err := store.Freeze(ctx, cutoff, valid); err == nil {
		t.Fatal("snapshot freeze accepted revoked model permission")
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=true, enabled=false WHERE id='publication-test'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Snapshot(ctx, hash); err == nil {
		t.Fatal("snapshot replay accepted disabled provider")
	}
}
