package dataservice_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

const (
	coldTarget = "600101.SH"
	coldPeer   = "600102.SH"
)

type requestedBatch struct {
	collisionProvider
	entities []ds.Entity
}

func (p requestedBatch) Entities(context.Context) ([]ds.Entity, error) { return p.entities, nil }

// Explicitly simulated vendor rows for testing atomic publication and shared catalog identity.
func syntheticRequestedBatch() requestedBatch {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return requestedBatch{collisionProvider: collisionProvider{id: "tushare", records: []ds.Record{{
		DatasetID: "mkt.daily_bar", EntityCode: coldTarget, RecordKey: "synthetic/daily/" + coldTarget, Period: &day, AvailableAt: day,
		Origin: "exchange", SourceTier: "L2", RawRef: "synthetic/daily", RawSHA256: strings.Repeat("1", 64), RawLocator: "row 0", MappingVersion: 1,
		Payload: map[string]any{"trade_date": "2026-01-02", "open": "10", "high": "11", "low": "9", "close": "10", "pre_close": "10", "volume": "100", "amount": "1000", "adj_factor": "1", "suspended": false},
	}}}, entities: []ds.Entity{
		{Kind: "stock", Code: coldTarget, Name: "模拟研究主体", Exchange: "SH", Status: "listed", Aliases: []string{"模拟研究主体"}, Attrs: map[string]any{"tushare_research_subject": true}},
		{Kind: "stock", Code: coldPeer, Name: "模拟可比同行", Exchange: "SH", Status: "listed", Aliases: []string{"模拟可比同行"}, Attrs: map[string]any{"tushare_research_subject": false}},
	}}
}

func requestedStores(t *testing.T) (api, worker *ds.Store, pool *pgxpool.Pool) {
	t.Helper()
	pool = dbtest.New(t)
	workerPool, err := pgxpool.NewWithConfig(t.Context(), pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(workerPool.Close)
	if _, err = pool.Exec(t.Context(), `INSERT INTO providers(id,name,kind,enabled,allow_model_processing,synthetic) VALUES ('tushare','simulated','vendor',true,true,false)`); err != nil {
		t.Fatal(err)
	}
	cat, err := ds.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return ds.NewOnDemandResearchStore(pool, cat, []string{"600519.SH"}, true), ds.NewOnDemandResearchStore(workerPool, cat, []string{"600519.SH"}, true), pool
}

func assertRequestedState(t *testing.T, store *ds.Store, code string, registered, prepared bool) {
	t.Helper()
	got, err := store.ResearchSubjectRegistered(t.Context(), code)
	if err != nil || got != registered {
		t.Fatalf("%s registered=%v, want %v: %v", code, got, registered, err)
	}
	got, err = store.ResearchSubjectPrepared(t.Context(), code)
	if err != nil || got != prepared {
		t.Fatalf("%s prepared=%v, want %v: %v", code, got, prepared, err)
	}
}

func TestRequestedSubjectAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	api, worker, pool := requestedStores(t)
	candidate, err := api.UpsertSearchEntity(ctx, "stock", coldTarget, "模拟目录简称", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRequestedState(t, api, coldTarget, false, false)
	collecting, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	collect := func(ctx context.Context) (ds.Provider, error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("concurrent duplicate collection")
		}
		close(collecting)
		select {
		case <-release:
			return syntheticRequestedBatch(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	results := make(chan error, 2)
	go func() { _, err := worker.PrepareRequestedSubject(ctx, coldTarget, collect); results <- err }()
	select {
	case <-collecting:
	case err := <-results:
		t.Fatalf("did not reach collector: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertRequestedState(t, api, coldTarget, false, false)
	acquires := pool.Stat().AcquireCount()
	go func() { _, err := api.PrepareRequestedSubject(ctx, coldTarget, collect); results <- err }()
	for pool.Stat().AcquireCount() <= acquires {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("collected %d times", calls.Load())
	}
	assertRequestedState(t, api, coldTarget, true, true)
	assertRequestedState(t, worker, coldPeer, false, false)
	peer, err := worker.EntityByCode(ctx, coldPeer)
	if err != nil || peer.Code != coldPeer {
		t.Fatalf("peer unavailable to data loader: %+v %v", peer, err)
	}
	if hits, err := api.Search(ctx, coldPeer, "stock", 10); err != nil || slices.ContainsFunc(hits, func(e ds.EntityRow) bool { return e.Code == coldPeer }) {
		t.Fatalf("peer appeared as a prepared target: %+v %v", hits, err)
	}
	for _, term := range []string{"模拟研究主体", "模拟目录简称"} {
		hits, err := api.Search(ctx, term, "stock", 10)
		if err != nil || len(hits) != 1 || hits[0].ID != candidate.ID || hits[0].Name != "模拟研究主体" {
			t.Fatalf("catalog and published subject diverged for %q: %+v %v", term, hits, err)
		}
	}
	hits, err := api.Mentioned(ctx, "观察模拟目录简称")
	if err != nil || len(hits) != 1 || hits[0].ID != candidate.ID {
		t.Fatalf("alias and shared research identity diverged: %+v %v", hits, err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		row, err := api.ObservationBar(ctx, tx, candidate.ID, coldTarget, time.Now().UTC(), nil)
		if err == nil && (row.EntityID == nil || *row.EntityID != candidate.ID) {
			return errors.New("mixed report bar is for a different entity")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// The same security may next appear as a peer while synchronizing another subject.
	_, err = worker.SyncObserved(ctx, "tushare", func(context.Context) (ds.Provider, error) {
		peerUpdate := syntheticRequestedBatch()
		peerUpdate.entities[0].Attrs["tushare_research_subject"] = false
		return peerUpdate, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := api.UpsertSearchEntity(ctx, "stock", coldTarget, "模拟搜索别名", nil)
	if err != nil || after.ID != candidate.ID || after.Name != "模拟研究主体" || after.Attrs["tushare_research_subject"] != true {
		t.Fatalf("peer or directory update lost research metadata: %+v %v", after, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tushare_requested_subjects SET published_at=now()-interval '25 hours' WHERE code=$1`, coldTarget); err != nil {
		t.Fatal(err)
	}
	assertRequestedState(t, worker, coldTarget, true, false)
	var refreshes int
	for range 2 {
		if _, err := worker.PrepareRequestedSubject(ctx, coldTarget, func(context.Context) (ds.Provider, error) {
			refreshes++
			return syntheticRequestedBatch(), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if refreshes != 1 {
		t.Fatalf("expired subject refreshed %d times", refreshes)
	}
	assertRequestedState(t, api, coldTarget, true, true)
}

func TestRequestedSubjectPublicationRollsBack(t *testing.T) {
	for _, tc := range []string{"missing target bar", "quarantined bar", "identity collision", "revoked while collecting"} {
		t.Run(tc, func(t *testing.T) {
			api, worker, pool := requestedStores(t)
			ctx := t.Context()
			batch := syntheticRequestedBatch()
			switch tc {
			case "missing target bar":
				batch.records[0].EntityCode = coldPeer
			case "quarantined bar":
				batch.records[0].QualityState = "QUARANTINED"
			case "identity collision":
				if _, err := api.UpsertSearchEntity(ctx, "industry", coldPeer, "模拟冲突类型", nil); err != nil {
					t.Fatal(err)
				}
			}
			n, err := worker.PrepareRequestedSubject(ctx, coldTarget, func(context.Context) (ds.Provider, error) {
				if tc == "revoked while collecting" {
					if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='tushare'`); err != nil {
						t.Fatal(err)
					}
				}
				return batch, nil
			})
			if err == nil || n != 0 {
				t.Fatalf("invalid batch published %d records: %v", n, err)
			}
			assertRequestedState(t, api, coldTarget, false, false)
			var writes int
			err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM records) + (SELECT count(*) FROM entities WHERE code=$1) + (SELECT count(*) FROM capabilities)`, coldTarget).Scan(&writes)
			if err != nil || writes != 0 {
				t.Fatalf("failed batch made partial changes: %d %v", writes, err)
			}
		})
	}
}
