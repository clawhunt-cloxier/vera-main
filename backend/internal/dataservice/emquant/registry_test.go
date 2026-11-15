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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func syntheticStorageLicense(t *testing.T, pool *pgxpool.Pool, provider, dataset, method string, fields []string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `INSERT INTO source_licenses
		(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage)
		VALUES($1,'synthetic-product',$2,$3,$4,'listed',ARRAY['A','B'],'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 day',true) RETURNING id`, provider, dataset, method, fields).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type testGateFunc func(context.Context, func() error) error

func (f testGateFunc) Do(ctx context.Context, call func() error) error { return f(ctx, call) }

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
	if _, err := provider.DownTo(ctx, 10); err == nil {
		t.Fatal("rollback discarded registered request lineage")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("restore later migrations after rollback check: %v", err)
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
		LicenseID: syntheticStorageLicense(t, pool, "choice-test", "mkt.daily_bar", "CSD", []string{"X"}),
		Product:   "synthetic-product",
	}
	ungated := registeredCollector
	ungated.Collector.Gate = nil
	before := f.calls
	if _, _, err := ungated.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", ""); err == nil || f.calls != before {
		t.Fatal("ungated registered collector reached vendor")
	}
	unauthorized := registeredCollector
	unauthorized.LicenseID = uuid.New()
	if _, _, err := unauthorized.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", ""); err == nil || f.calls != before {
		t.Fatal("unlicensed registered collector reached vendor")
	}
	if _, _, err := registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "UNKNOWN", "2026-11-12", "2026-11-12", ""); err == nil || f.calls != before {
		t.Fatal("unlicensed indicator reached vendor")
	}
	if _, _, err := registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "OTHER", "X", "2026-11-12", "2026-11-12", ""); err == nil || f.calls != before {
		t.Fatal("unlicensed instrument reached vendor")
	}
	cube, registeredRef, err := registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err != nil || len(cube.Values) != 1 || registeredRef.ID == uuid.Nil || registeredRef.DatasetID != "mkt.daily_bar" {
		t.Fatalf("collect and register: %v", err)
	}
	var savedLicense uuid.UUID
	var savedMethod string
	var savedFields, savedCodes []string
	if err := pool.QueryRow(ctx, `SELECT source_license_id,vendor_method,vendor_fields,vendor_codes FROM raw_objects WHERE id=$1`, registeredRef.ID).Scan(&savedLicense, &savedMethod, &savedFields, &savedCodes); err != nil || savedLicense != registeredCollector.LicenseID || savedMethod != "CSD" || len(savedFields) != 1 || savedFields[0] != "X" || len(savedCodes) != 1 || savedCodes[0] != "A" {
		t.Fatalf("registered raw scope does not match grant: %v", err)
	}
	f.quote = Quote{Data: f.cube, ObservedAt: time.Now().UTC()}
	quoteCollector := registeredCollector
	quoteCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "mkt.intraday_quote", "CSQSNAPSHOT", []string{"X"})
	quote, quoteRef, err := quoteCollector.CollectQuote(ctx, "mkt.intraday_quote", "A", "X", "")
	if err != nil || len(quote.Data.Values) != 1 || quoteRef.ID == uuid.Nil {
		t.Fatalf("collect and register quote: %v", err)
	}
	before = f.calls
	if _, _, err := quoteCollector.CollectQuote(ctx, "fin.statement", "A", "X", ""); err == nil || f.calls != before {
		t.Fatal("non-intraday quote reached vendor")
	}
	f.cube.Indicators = []string{"Y"}
	cube, failedRef, err := registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err == nil || len(cube.Values) != 0 || failedRef.ID != uuid.Nil {
		t.Fatal("unrequested response indicator was registered")
	}
	f.cube.Indicators = []string{"X"}
	f.cube.Codes = []string{"OTHER"}
	cube, failedRef, err = registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err == nil || len(cube.Values) != 0 || failedRef.ID != uuid.Nil {
		t.Fatal("unrequested response instrument was registered")
	}
	f.cube.Codes = []string{"A"}
	revokedDuringCall := registeredCollector
	revokedDuringCall.Collector.Gate = testGateFunc(func(ctx context.Context, call func() error) error {
		if err := call(); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, registeredCollector.LicenseID)
		return err
	})
	cube, failedRef, err = revokedDuringCall.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err == nil || len(cube.Values) != 0 || failedRef.ID != uuid.Nil {
		t.Fatal("license revoked during vendor call allowed registration")
	}
	registeredCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "mkt.daily_bar", "CSD", []string{"X"})
	if _, err := pool.Exec(ctx, `UPDATE providers SET enabled=false WHERE id='choice-test'`); err != nil {
		t.Fatal(err)
	}
	cube, failedRef, err = registeredCollector.CollectCSD(ctx, "mkt.daily_bar", "A", "X", "2026-11-12", "2026-11-12", "")
	if err == nil || len(cube.Values) != 0 || failedRef.ID != uuid.Nil {
		t.Fatal("failed registration exposed collected data")
	}
}
