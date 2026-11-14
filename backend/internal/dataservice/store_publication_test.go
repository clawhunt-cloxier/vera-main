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
