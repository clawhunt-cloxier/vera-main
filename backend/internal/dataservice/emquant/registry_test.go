package emquant

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestArchiveRegistryVerifiesAndPreservesProvenance(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic) VALUES('choice-test','Choice test','vendor',false)`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			stored, _ = io.ReadAll(r.Body)
		case http.MethodGet:
			_, _ = w.Write(stored)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	store, err := NewS3RawStore(testS3Client(server.URL, "test", "test"), "test-bucket", "registry")
	if err != nil {
		t.Fatal(err)
	}
	f := &testFetcher{cube: Cube{Codes: []string{"A"}, Indicators: []string{"X"}, Dates: []string{"2026-11-12"}, Values: []Cell{{Type: 11, Value: 12.5}}}}
	_, ref, err := (Collector{Fetcher: f, Store: store}).CollectCSD(ctx, "A", "X", "2026-11-12", "2026-11-12", "")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := ArchiveRegistry{Pool: pool, Store: store, Catalog: cat, ProviderID: "choice-test"}
	if _, err := registry.RegisterCube(ctx, "unknown.dataset", ref); err == nil {
		t.Fatal("unknown dataset registered")
	}
	id, err := registry.RegisterCube(ctx, "mkt.daily_bar", ref)
	if err != nil || id == uuid.Nil {
		t.Fatalf("register: %v", err)
	}
	id2, err := registry.RegisterCube(ctx, "mkt.daily_bar", ref)
	if err != nil || id2 != id {
		t.Fatalf("idempotent registration: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := registry.RegisterCube(ctx, "mkt.daily_bar", ref)
			if err == nil && got != id {
				err = fmt.Errorf("registration returned another identity")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent registration: %v", err)
		}
	}
	var fingerprint, hash, path, format string
	var received, archived, registered time.Time
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint,content_hash,stored_path,archive_format,fetched_at,archived_at,registered_at FROM raw_objects WHERE id=$1`, id).Scan(&fingerprint, &hash, &path, &format, &received, &archived, &registered); err != nil {
		t.Fatal(err)
	}
	if fingerprint != ref.RequestFingerprint || hash != ref.BodySHA256 || path != ref.BodyRef || format != "emquant-sdk-decoded-v2" || !received.Equal(ref.ReceivedAt.Truncate(time.Microsecond)) || !archived.Equal(ref.ArchivedAt.Truncate(time.Microsecond)) || registered.IsZero() {
		t.Fatal("persisted raw lineage differs from archived response")
	}
	if _, err := pool.Exec(ctx, `UPDATE raw_objects SET archive_format=NULL WHERE id=$1`, id); err == nil {
		t.Fatal("partial request metadata accepted")
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../../platform/db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("rollback discarded registered request lineage")
	}
	bad := ref
	bad.RequestFingerprint = digest([]byte("another request"))
	if _, err := registry.RegisterCube(ctx, "mkt.daily_bar", bad); err == nil {
		t.Fatal("wrong request fingerprint was registered")
	}
	bad = ref
	bad.ArchivedAt = ref.ArchivedAt.Add(time.Second)
	if _, err := registry.RegisterCube(ctx, "mkt.daily_bar", bad); err == nil {
		t.Fatal("conflicting archive completion metadata accepted")
	}
	bad = ref
	bad.BodyRef = "s3://other-bucket/other"
	if _, err := registry.RegisterCube(ctx, "mkt.daily_bar", bad); err == nil {
		t.Fatal("different object reference was registered")
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET enabled=false WHERE id='choice-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterCube(ctx, "mkt.daily_bar", ref); err == nil {
		t.Fatal("disabled provider registered an archive")
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET enabled=true, synthetic=true WHERE id='choice-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterCube(ctx, "mkt.daily_bar", ref); err == nil {
		t.Fatal("synthetic provider registered an archive")
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET synthetic=false WHERE id='choice-test'`); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	stored = []byte("corrupt")
	mu.Unlock()
	if _, err := registry.RegisterCube(ctx, "mkt.daily_bar", ref); err == nil {
		t.Fatal("corrupted archive was registered")
	}
	registeredCollector := RegisteredCollector{
		Collector: Collector{Fetcher: f, Store: store, Gate: PostgreSQLRateGate{Pool: pool, Scope: "registry-test", MinInterval: 90 * time.Millisecond, RateLimitCooldown: 200 * time.Millisecond}},
		Registry:  registry,
	}
	ungated := registeredCollector
	ungated.Collector.Gate = nil
	before := f.calls
	if _, _, err := ungated.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", ""); err == nil || f.calls != before {
		t.Fatal("ungated registered collector reached vendor")
	}
	cube, registeredRef, err := registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err != nil || len(cube.Values) != 1 || registeredRef.ID == uuid.Nil || registeredRef.DatasetID != "mkt.daily_bar" {
		t.Fatalf("collect and register: %v", err)
	}
	f.quote = Quote{Data: f.cube, ObservedAt: time.Now().UTC()}
	quote, quoteRef, err := registeredCollector.CollectQuote(ctx, "mkt.intraday_quote", "A", "X", "")
	if err != nil || len(quote.Data.Values) != 1 || quoteRef.ID == uuid.Nil {
		t.Fatalf("collect and register quote: %v", err)
	}
	before = f.calls
	if _, _, err := registeredCollector.CollectQuote(ctx, "fin.statement", "A", "X", ""); err == nil || f.calls != before {
		t.Fatal("non-intraday quote reached vendor")
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET enabled=false WHERE id='choice-test'`); err != nil {
		t.Fatal(err)
	}
	cube, failedRef, err := registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err == nil || len(cube.Values) != 0 || failedRef.ID != uuid.Nil {
		t.Fatal("failed registration exposed collected data")
	}
}
