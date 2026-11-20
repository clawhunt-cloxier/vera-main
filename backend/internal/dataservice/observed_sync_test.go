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
