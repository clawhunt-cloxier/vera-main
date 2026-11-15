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
		(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
		VALUES($1,'synthetic-product',$2,$3,$4,'listed',ARRAY['A','B'],'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 day',true,true,true) RETURNING id`, provider, dataset, method, fields).Scan(&id)
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
	var legacyID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO raw_objects(provider_id,dataset_id,object_key,content_hash,size_bytes,stored_path,fetched_at)
		VALUES('choice-test','mkt.daily_bar','legacy','synthetic',0,'synthetic-path',clock_timestamp()) RETURNING id`).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE raw_objects SET request_fingerprint=$2,archive_format='emquant-sdk-decoded-v2',registered_at=clock_timestamp() WHERE id=$1`, legacyID, digest([]byte("synthetic"))); err == nil {
		t.Fatal("legacy object was promoted to verified lineage")
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
	originalCube := f.cube
	f.cube = Cube{Codes: []string{"A"}, Indicators: []string{"IncomeStatement_61", "IncomeStatement_9"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 11, Value: -20.0}, {Type: 8, Value: int64(1000)}}}
	statementCollector := registeredCollector
	statementCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "fin.statement", "CSS", []string{"INCOMESTATEMENT_9", "INCOMESTATEMENT_61"})
	before = f.calls
	if _, _, err := registeredCollector.CollectStatementFields(ctx, "A", "2025-12-31", 1); err == nil || f.calls != before {
		t.Fatal("financial fields were requested under a different product scope")
	}
	statement, statementRef, err := statementCollector.CollectStatementFields(ctx, "A", "2025-12-31", 1)
	if err != nil || statementRef.ID == uuid.Nil || statementRef.LicenseID != statementCollector.LicenseID || statement.Source != statementRef.Source || statement.RevenueCNY == nil || *statement.RevenueCNY != "1000" || statement.NetProfitCNY == nil || *statement.NetProfitCNY != "-20" || statement.RequestedPeriod != "2025-12-31" || statement.RequestedType != 1 {
		t.Fatalf("registered statement fields: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT source_license_id,vendor_method FROM raw_objects WHERE id=$1`, statementRef.ID).Scan(&savedLicense, &savedMethod); err != nil || savedLicense != statementCollector.LicenseID || savedMethod != "CSS" {
		t.Fatalf("statement source lost its licensed CSS scope: %v", err)
	}
	var statements int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE dataset_id='fin.statement'`).Scan(&statements); err != nil || statements != 0 {
		t.Fatalf("partial financial fields became a complete research statement: %d %v", statements, err)
	}
	before = f.calls
	if _, _, err := statementCollector.CollectStatementFields(ctx, "A", "2025-11-30", 1); err == nil || f.calls != before {
		t.Fatal("invalid report period reached vendor")
	}
	f.cube.Indicators[0] = "UNEXPECTED"
	badStatement, badSource, err := statementCollector.CollectStatementFields(ctx, "A", "2025-12-31", 1)
	if err == nil || badStatement.RevenueCNY != nil || badStatement.NetProfitCNY != nil || badSource.ID != uuid.Nil {
		t.Fatal("unrequested financial field was returned")
	}
	f.cube.Indicators[0] = "IncomeStatement_61"
	revokedStatement := statementCollector
	revokedStatement.Collector.Gate = testGateFunc(func(ctx context.Context, call func() error) error {
		if err := call(); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, statementCollector.LicenseID)
		return err
	})
	badStatement, badSource, err = revokedStatement.CollectStatementFields(ctx, "A", "2025-12-31", 1)
	if err == nil || badStatement.RevenueCNY != nil || badStatement.NetProfitCNY != nil || badSource.ID != uuid.Nil {
		t.Fatal("financial source revoked during collection was returned")
	}
	f.cube = Cube{Codes: []string{"A"}, Indicators: []string{"NAME", "LISTDATE", "DELISTDATE"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 13, Value: "Synthetic"}, {Type: 13, Value: "2010-01-02"}, {Type: 0}}}
	securityCollector := registeredCollector
	securityCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "ref.security", "CSS", []string{"NAME", "LISTDATE", "DELISTDATE"})
	security, securityRef, err := securityCollector.CollectSecurityBasics(ctx, "A")
	if err != nil || securityRef.ID == uuid.Nil || security.Source != securityRef.Source || security.ShortName != "Synthetic" || security.FirstListingDate != "2010-01-02" || security.DelistingDate != nil {
		t.Fatalf("registered partial security basics: %v", err)
	}
	before = f.calls
	if _, _, err := securityCollector.CollectSecurityBasics(ctx, "A,B"); err == nil || f.calls != before {
		t.Fatal("ambiguous security basics request reached vendor")
	}
	f.cube = Cube{Codes: []string{"A"}, Indicators: []string{"HISSW2021CODE", "HISSW2021"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 13, Value: "synthetic-code"}, {Type: 13, Value: "Synthetic industry"}}}
	industryCollector := registeredCollector
	industryCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "ref.industry", "CSS", []string{"HISSW2021", "HISSW2021CODE"})
	industry, industryRef, err := industryCollector.CollectIndustryLevel(ctx, "A", "2025-12-31", 2)
	if err != nil || industryRef.ID == uuid.Nil || industry.Source != industryRef.Source || industry.IndustryCode != "synthetic-code" || industry.RequestedLevel != 2 {
		t.Fatalf("registered industry observation: %v", err)
	}
	f.cube = Cube{Codes: []string{"A"}, Indicators: []string{"STMTACTDATE", "CORRECTANNCNUM", "CORRECTANNCDATE"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 13, Value: "2026-04-01"}, {Type: 8, Value: int64(1)}, {Type: 0}}}
	disclosureCollector := registeredCollector
	disclosureCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "fin.statement", "CSS", []string{"STMTACTDATE", "CORRECTANNCNUM", "CORRECTANNCDATE"})
	disclosure, disclosureRef, err := disclosureCollector.CollectDisclosureObservation(ctx, "A", "2025-12-31")
	if err != nil || disclosureRef.ID == uuid.Nil || disclosure.Source != disclosureRef.Source || disclosure.Cells["CORRECTANNCNUM"].Value != int64(1) || disclosure.RequestedPeriod != "2025-12-31" {
		t.Fatalf("registered disclosure observation: %v", err)
	}
	var partialRecords int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE dataset_id IN ('ref.security','ref.industry','fin.statement')`).Scan(&partialRecords); err != nil || partialRecords != 0 {
		t.Fatalf("partial observations entered canonical research records: %d %v", partialRecords, err)
	}
	var calendarLicense uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage)
		VALUES('choice-test','synthetic-product','ref.trading_calendar','TRADEDATES',ARRAY['TRADEDATE'],'listed',ARRAY['CNSESH'],'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 day',true) RETURNING id`).Scan(&calendarLicense); err != nil {
		t.Fatal(err)
	}
	f.cube = Cube{Codes: []string{""}, Indicators: []string{"TRADEDATE"}, Dates: []string{"2026/11/9", "2026/11/10"}, Values: []Cell{{Type: 13, Value: "2026/11/9"}, {Type: 13, Value: "2026/11/10"}}}
	calendarCollector := registeredCollector
	calendarCollector.LicenseID = calendarLicense
	before = f.calls
	if _, _, err := calendarCollector.CollectOpenDates(ctx, "CNSESZ", "2026-11-09", "2026-11-15"); err == nil || f.calls != before {
		t.Fatal("unlicensed market calendar reached vendor")
	}
	if _, _, err := calendarCollector.CollectOpenDates(ctx, "CNSESH", "2026-11-15", "2026-11-09"); err == nil || f.calls != before {
		t.Fatal("invalid calendar range reached vendor")
	}
	days, calendarRef, err := calendarCollector.CollectOpenDates(ctx, "CNSESH", "2026-11-09", "2026-11-15")
	if err != nil || calendarRef.ID == uuid.Nil || len(days) != 2 || days[0] != "2026-11-09" || days[1] != "2026-11-10" {
		t.Fatalf("registered calendar collection: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT source_license_id,vendor_method,vendor_fields,vendor_codes FROM raw_objects WHERE id=$1`, calendarRef.ID).Scan(&savedLicense, &savedMethod, &savedFields, &savedCodes); err != nil || savedLicense != calendarLicense || savedMethod != "TRADEDATES" || len(savedFields) != 1 || savedFields[0] != "TRADEDATE" || len(savedCodes) != 1 || savedCodes[0] != "CNSESH" {
		t.Fatalf("calendar source lost market-specific license: %v", err)
	}
	calendarBody, err := store.Get(ctx, calendarRef.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	calendarRecords, err := MapOpenDates(calendarBody, calendarRef.Source, "CNSESH", "2026-11-09", "2026-11-15")
	if err != nil || len(calendarRecords) != 2 || calendarRecords[0].Source != calendarRef.Source {
		t.Fatalf("registered calendar source could not be replayed: %v", err)
	}
	revokedCalendar := calendarCollector
	revokedCalendar.Collector.Gate = testGateFunc(func(ctx context.Context, call func() error) error {
		if err := call(); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, calendarLicense)
		return err
	})
	badDays, badCalendarRef, err := revokedCalendar.CollectOpenDates(ctx, "CNSESH", "2026-11-09", "2026-11-15")
	if err == nil || len(badDays) != 0 || badCalendarRef.ID != uuid.Nil {
		t.Fatal("calendar source revoked during collection was returned")
	}
	f.cube = Cube{Codes: []string{"A"}, Indicators: []string{"TURN", "VOLUME", "AMOUNT"}, Dates: []string{"2026-11-09"}, Values: []Cell{{Type: 11, Value: 1.25}, {Type: 8, Value: int64(10000)}, {Type: 11, Value: 123400.5}}}
	liquidityCollector := registeredCollector
	liquidityCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "mkt.daily_bar", "CSD", []string{"VOLUME", "AMOUNT", "TURN"})
	liquidity, liquidityRef, err := liquidityCollector.CollectDailyLiquidity(ctx, "A", "2026-11-09", "2026-11-10")
	if err != nil || liquidityRef.ID == uuid.Nil || len(liquidity) != 1 || liquidity[0].VolumeShares != "10000" || liquidity[0].AmountCNY != "123400.5" || liquidity[0].TurnoverRatio == nil || *liquidity[0].TurnoverRatio != "0.0125" || liquidity[0].Source != liquidityRef.Source {
		t.Fatalf("registered daily liquidity mapping: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT source_license_id,vendor_method FROM raw_objects WHERE id=$1`, liquidityRef.ID).Scan(&savedLicense, &savedMethod); err != nil || savedLicense != liquidityCollector.LicenseID || savedMethod != "CSD" {
		t.Fatalf("daily liquidity source lost its licensed CSD scope: %v", err)
	}
	before = f.calls
	if _, _, err := liquidityCollector.CollectDailyLiquidity(ctx, "A,B", "2026-11-09", "2026-11-10"); err == nil || f.calls != before {
		t.Fatal("ambiguous daily liquidity request reached vendor")
	}
	f.cube.Values[1].Value = int64(-1)
	badLiquidity, badLiquidityRef, err := liquidityCollector.CollectDailyLiquidity(ctx, "A", "2026-11-09", "2026-11-10")
	if err == nil || len(badLiquidity) != 0 || badLiquidityRef.ID != uuid.Nil {
		t.Fatal("invalid daily liquidity observation was returned")
	}
	var dailyBars int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE dataset_id='mkt.daily_bar'`).Scan(&dailyBars); err != nil || dailyBars != 0 {
		t.Fatalf("partial liquidity became a complete daily bar: %d %v", dailyBars, err)
	}
	f.cube = Cube{Codes: []string{"A"}, Indicators: []string{"SECLENDINGBALANCEM", "TRADINGBALANCEM", "PURCHASEWITHBORROWEDMONEYM", "REPAYMENTTOBROKERM", "SECLENDINGBALANCEVOLUMEM"}, Dates: []string{"2026-11-09"}, Values: []Cell{{Type: 0}, {Type: 8, Value: int64(123)}, {Type: 11, Value: 3.5}, {Type: 0}, {Type: 8, Value: int64(45)}}}
	marginCollector := registeredCollector
	marginCollector.LicenseID = syntheticStorageLicense(t, pool, "choice-test", "flow.margin", "CSD", []string{"TRADINGBALANCEM", "PURCHASEWITHBORROWEDMONEYM", "REPAYMENTTOBROKERM", "SECLENDINGBALANCEVOLUMEM", "SECLENDINGBALANCEM"})
	margin, marginRef, err := marginCollector.CollectMarginObservations(ctx, "A", "2026-11-09", "2026-11-10")
	if err != nil || marginRef.ID == uuid.Nil || len(margin.Rows) != 1 || margin.Source != marginRef.Source || margin.Rows[0].Cells["TRADINGBALANCEM"].Value != int64(123) || margin.Rows[0].Locators["TRADINGBALANCEM"] != "$.data.Values[1]" {
		t.Fatalf("registered margin observations: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT source_license_id,vendor_method FROM raw_objects WHERE id=$1`, marginRef.ID).Scan(&savedLicense, &savedMethod); err != nil || savedLicense != marginCollector.LicenseID || savedMethod != "CSD" {
		t.Fatalf("margin source lost its licensed CSD scope: %v", err)
	}
	before = f.calls
	if _, _, err := marginCollector.CollectMarginObservations(ctx, "C", "2026-11-09", "2026-11-10"); err == nil || f.calls != before {
		t.Fatal("unlicensed margin security reached vendor")
	}
	revokedMargin := marginCollector
	revokedMargin.Collector.Gate = testGateFunc(func(ctx context.Context, call func() error) error {
		if err := call(); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, marginCollector.LicenseID)
		return err
	})
	badMargin, badMarginRef, err := revokedMargin.CollectMarginObservations(ctx, "A", "2026-11-09", "2026-11-10")
	if err == nil || len(badMargin.Rows) != 0 || badMarginRef.ID != uuid.Nil {
		t.Fatal("margin source revoked during collection was returned")
	}
	var marginRecords int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE dataset_id='flow.margin'`).Scan(&marginRecords); err != nil || marginRecords != 0 {
		t.Fatalf("unverified margin observations became records: %d %v", marginRecords, err)
	}
	f.cube = originalCube
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
