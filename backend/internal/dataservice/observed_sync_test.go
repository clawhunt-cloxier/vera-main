package dataservice_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

const observedVendor = "test-observed-vendor"

func observedIndex(closePrice string) dataservice.Provider {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return &singleRecordProvider{id: observedVendor, record: dataservice.Record{
		DatasetID: "mkt.index_bar", RecordKey: "shared-index:2026-01-02", Period: &day, AvailableAt: day,
		Origin: "exchange", SourceTier: "L2", RawRef: "mock/index/" + closePrice,
		RawSHA256: strings.Repeat("a", 64), RawLocator: "row 0", MappingVersion: 1,
		Payload: map[string]any{"trade_date": "2026-01-02", "index_code": "MOCK", "close": closePrice},
	}}
}

func observedStore(t *testing.T) (*dataservice.Store, *pgxpool.Pool) {
	t.Helper()
	cfg := dbtest.New(t).Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	catalog, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return dataservice.NewVendorStore(pool, catalog), pool
}

func TestObservedSyncSerializesCollectionAndPublication(t *testing.T) {
	store, pool := observedStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	holding, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := store.SyncObserved(ctx, observedVendor, func(ctx context.Context) (dataservice.Provider, error) {
			close(holding)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return observedIndex("100"), nil
			}
		})
		first <- err
	}()
	select {
	case <-holding:
	case err := <-first:
		t.Fatalf("first collection did not start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// A rate-gated request needs a pool slot even while the collector owns its
	// session and other processes are trying to collect the same shared index.
	waitCtx, stopWait := context.WithCancel(ctx)
	defer stopWait()
	waiters := make(chan error, 8)
	acquires := pool.Stat().AcquireCount()
	for range cap(waiters) {
		go func() {
			_, err := store.SyncObserved(waitCtx, observedVendor, func(context.Context) (dataservice.Provider, error) {
				return nil, errors.New("overlapping collection")
			})
			waiters <- err
		}()
	}
	for pool.Stat().AcquireCount()-acquires < int64(cap(waiters)) {
		select {
		case <-ctx.Done():
			t.Fatal("contending collectors held pool slots", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("rate gate cannot use one-connection pool during collection: %v", err)
	}
	stopWait()
	for range cap(waiters) {
		if err := <-waiters; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter returned %v", err)
		}
	}

	second := make(chan error, 1)
	go func() {
		_, err := store.SyncObserved(ctx, observedVendor, func(ctx context.Context) (dataservice.Provider, error) {
			var previous string
			err := pool.QueryRow(ctx, `SELECT payload->>'close' FROM records WHERE provider_id=$1`, observedVendor).Scan(&previous)
			if err == nil && previous != "100" {
				err = errors.New("first observation was not yet published")
			}
			return observedIndex("101"), err
		})
		second <- err
	}()
	close(release)
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for version, price := range []string{"100", "101"} {
		var got string
		err := pool.QueryRow(ctx, `SELECT payload->>'close' FROM records WHERE provider_id=$1 AND record_version=$2 AND published_at IS NULL`, observedVendor, version+1).Scan(&got)
		if err != nil || got != price {
			t.Fatalf("version %d returned %q: %v", version+1, got, err)
		}
	}
}

func TestSecuritySnapshotSessionAndReplay(t *testing.T) {
	ctx := t.Context()
	api, worker, _ := requestedStores(t)
	batch := syntheticRequestedBatch()
	security := batch.records[0]
	security.DatasetID, security.RecordKey = "ref.security", "mock-security"
	security.Payload = map[string]any{"security_code": coldTarget, "short_name": "模拟", "exchange": "SH", "board": "main", "list_date": "2000-01-01",
		"status": "listed", "is_financial": false, "margin_eligible": true, "total_shares": "1000", "float_shares": "900"}
	day := func(v string) *time.Time {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	for _, tc := range []struct {
		date           string
		mapping, added int
		current        bool
	}{
		{"2026-01-06", 1, 1, false},
		{"2026-01-05", 2, 1, false},
		{"2026-01-06", 2, 1, true},
		{"2026-01-06", 2, 0, true},
		{"2026-01-05", 2, 0, false},
		{"2026-01-06", 1, 0, false},
		{"", 0, 0, false},
	} {
		batch.records = nil
		if tc.mapping > 0 {
			security.MappingVersion, security.Period = tc.mapping, day(tc.date)
			security.Payload["is_financial"] = tc.current
			security.Payload["margin_eligible"] = tc.current
			batch.records = []dataservice.Record{security}
		}
		entity := &batch.entities[0]
		entity.Classification = "模拟旧行业"
		entity.IsFinancial = tc.current
		entity.Attrs["industry_code"] = "OLD.SI"
		entity.Attrs["index_code"] = "OLD.SI"
		entity.Attrs["margin_eligible"] = tc.current
		if tc.current {
			entity.Classification = "模拟当前行业"
			entity.Attrs["industry_code"] = "NEW.SI"
			entity.Attrs["index_code"] = "NEW.SI"
		}
		n, err := worker.SyncObserved(ctx, "tushare", func(context.Context) (dataservice.Provider, error) { return batch, nil })
		if n != tc.added || err != nil {
			t.Fatalf("sync %+v: %d, %v", tc, n, err)
		}
		if tc.current || tc.added == 0 {
			got, err := api.EntityByCode(ctx, coldTarget)
			if err != nil || got.Classification == nil || *got.Classification != "模拟当前行业" || !got.IsFinancial ||
				got.Attrs["industry_code"] != "NEW.SI" || got.Attrs["index_code"] != "NEW.SI" ||
				got.Attrs["margin_eligible"] != true || got.Attrs["tushare_research_subject"] != true {
				t.Errorf("entity did not keep current session after %+v: %+v, %v", tc, got, err)
			}
		}
	}
	rows, err := api.Query(ctx, dataservice.Query{DatasetIDs: []string{"ref.security"}, AsOf: time.Now()})
	if err != nil || len(rows) != 1 || rows[0].Period == nil || rows[0].Period.Format(time.DateOnly) != "2026-01-06" || rows[0].Version != 3 {
		t.Fatalf("wrong session after replay: %+v, %v", rows, err)
	}
}

func TestObservedSyncLostLockSessionCannotPublish(t *testing.T) {
	store, pool := observedStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, oldErr := store.SyncObserved(ctx, observedVendor, func(ctx context.Context) (dataservice.Provider, error) {
		// Terminate only this test database's one holder. Wait for backend
		// termination before letting a replacement collect and publish.
		var killed bool
		err := pool.QueryRow(ctx, `SELECT pg_terminate_backend(l.pid, 3000) FROM pg_locks l
			WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&killed)
		if err != nil || !killed {
			t.Fatalf("cannot terminate collection session: killed=%v err=%v", killed, err)
		}
		n, err := store.SyncObserved(ctx, observedVendor, func(context.Context) (dataservice.Provider, error) {
			return observedIndex("101"), nil
		})
		if err != nil || n != 1 {
			t.Fatalf("replacement published %d records: %v", n, err)
		}
		return observedIndex("100"), nil
	})
	if oldErr == nil {
		t.Fatal("collector published after losing its lock")
	}
	var count int
	var price string
	if err := pool.QueryRow(ctx, `SELECT count(*),max(payload->>'close') FROM records WHERE provider_id=$1`, observedVendor).Scan(&count, &price); err != nil || count != 1 || price != "101" {
		t.Fatalf("unexpected surviving observation count=%d close=%q err=%v", count, price, err)
	}
}
