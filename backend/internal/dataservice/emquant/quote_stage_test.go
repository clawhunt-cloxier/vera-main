package emquant

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestStageQuoteRequiresRegisteredSourceAndCurrentPolicy(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('stage-test','Synthetic stage test','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	var entityID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic) VALUES('stock','A','Synthetic stock',false) RETURNING id`).Scan(&entityID); err != nil {
		t.Fatal(err)
	}
	license := syntheticStorageLicense(t, pool, "stage-test", "mkt.intraday_quote", "CSQSNAPSHOT", []string{"TIME", "NOW"})
	var policyID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-unit','unknown',60000,'synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute') RETURNING id`, license).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, ok := objects[r.URL.Path]; ok {
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	store, err := NewS3RawStore(testS3Client(server.URL, "test", "test"), "test-bucket", "stage")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	local := now.In(time.FixedZone("CST", 8*60*60))
	hhmmss := int64(local.Hour()*10000 + local.Minute()*100 + local.Second())
	f := &testFetcher{quote: Quote{ObservedAt: now, Data: Cube{Codes: []string{"A"}, Indicators: []string{"NOW", "TIME"}, Dates: []string{local.Format("2006-01-02")}, Values: []Cell{{Type: 11, Value: 10.25}, {Type: 6, Value: hhmmss}}}}}
	collector := RegisteredCollector{Collector: Collector{Fetcher: f, Store: store, Gate: PostgreSQLRateGate{Pool: pool, Scope: "stage-test", MinInterval: 90 * time.Millisecond, RateLimitCooldown: 200 * time.Millisecond}}, Registry: ArchiveRegistry{Pool: pool, Store: store, Catalog: cat, ProviderID: "stage-test"}, LicenseID: license, Product: "synthetic-product"}
	_, source, err := collector.CollectQuote(ctx, "mkt.intraday_quote", "A", "TIME,NOW", "")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{store: store, database: pool}
	id, err := runtime.StageQuote(ctx, cat, source, policyID, entityID, "A")
	if err != nil || id == uuid.Nil {
		t.Fatalf("stage quote: %v", err)
	}
	id2, err := runtime.StageQuote(ctx, cat, source, policyID, entityID, "A")
	if err != nil || id2 != id {
		t.Fatalf("idempotent quote stage: %v", err)
	}
	var otherEntity uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic) VALUES('stock','B','Synthetic second stock',false) RETURNING id`).Scan(&otherEntity); err != nil {
		t.Fatal(err)
	}
	pending, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Rollback(context.Background())
	var pendingID uuid.UUID
	if err := pending.QueryRow(ctx, `INSERT INTO staged_quotes(raw_object_id,policy_id,entity_id,security_code,event_at,received_at,payload,field_locators)
		SELECT raw_object_id,policy_id,$2,'B',event_at,received_at,payload,field_locators FROM staged_quotes WHERE id=$1 RETURNING id`, id, otherEntity).Scan(&pendingID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PublishStagedQuote(ctx, pendingID); err == nil {
		t.Fatal("uncommitted staged quote was published")
	}
	if err := pending.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var price, status, latency, nowLocator, timeLocator string
	if err := pool.QueryRow(ctx, `SELECT payload->>'last_price',payload->>'market_status',payload->>'latency_mode',field_locators->>'NOW',field_locators->>'TIME' FROM staged_quotes WHERE id=$1`, id).Scan(&price, &status, &latency, &nowLocator, &timeLocator); err != nil {
		t.Fatal(err)
	}
	if price != "10.25" || status != "unknown" || latency != "unknown" || nowLocator != "$.data.Values[0]" || timeLocator != "$.data.Values[1]" {
		t.Fatal("staged quote lost content, uncertainty, or source locators")
	}
	mu.Lock()
	var objectKey string
	var saved []byte
	for key, body := range objects {
		objectKey = key
		saved = append([]byte(nil), body...)
		objects[key] = []byte("corrupt")
	}
	mu.Unlock()
	if _, err := runtime.PublishStagedQuote(ctx, id); err == nil {
		t.Fatal("quote with corrupt source was published")
	}
	mu.Lock()
	objects[objectKey] = saved
	mu.Unlock()
	view, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Rollback(context.Background())
	var visible int
	if err := view.QueryRow(ctx, `SELECT count(*) FROM quote_publications WHERE staged_quote_id=$1`, id).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("publication unexpectedly visible in earlier view: %d %v", visible, err)
	}
	earlierBoundary, err := BeginQuoteBoundary(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	available, err := runtime.PublishStagedQuote(ctx, id)
	if err != nil || available.IsZero() {
		t.Fatalf("publish staged quote: %v", err)
	}
	if err := view.QueryRow(ctx, `SELECT count(*) FROM quote_publications WHERE staged_quote_id=$1`, id).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("later publication leaked into established MVCC view: %d %v", visible, err)
	}
	oldView, err := runtime.SelectPublishedQuotesAt(ctx, view, earlierBoundary, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceDisplay)
	if err != nil || len(oldView.Quotes) != 0 || len(oldView.Unavailable) != 1 || oldView.Unavailable[0] != entityID {
		t.Fatalf("older view selected a later publication: %+v %v", oldView, err)
	}
	if err := view.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_publications WHERE staged_quote_id=$1`, id).Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("committed publication absent from later view: %d %v", visible, err)
	}
	ordinary, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SelectPublishedQuotes(ctx, ordinary, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceDisplay); err == nil {
		t.Fatal("read-committed transaction accepted as a fixed view")
	}
	_ = ordinary.Rollback(ctx)
	latest, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := BeginQuoteBoundary(ctx, latest)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SelectPublishedQuotesAt(ctx, foreign, boundary, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceDisplay); err == nil {
		t.Fatal("a quote boundary was reused in a different transaction")
	}
	_ = foreign.Rollback(ctx)
	selected, err := runtime.SelectPublishedQuotesAt(ctx, latest, boundary, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceDisplay)
	if err != nil || len(selected.Quotes) != 1 || len(selected.Unavailable) != 0 || selected.Quotes[0].Price != "10.25" || selected.Quotes[0].StageID != id || selected.Quotes[0].MarketState != "unknown" || selected.Quotes[0].LatencyMode != "unknown" {
		t.Fatalf("latest view did not preserve eligible quote and uncertainty: %+v %v", selected, err)
	}
	partial, err := runtime.SelectPublishedQuotesAt(ctx, latest, boundary, []uuid.UUID{otherEntity, entityID}, QuoteLatestAvailable, dataservice.SourceModel)
	if err != nil || !partial.Cutoff.Equal(selected.Cutoff) || len(partial.Quotes) != 1 || len(partial.Unavailable) != 1 || partial.Unavailable[0] != otherEntity {
		t.Fatalf("model view did not preserve explicit missing entity: %+v %v", partial, err)
	}
	realtime, err := runtime.SelectPublishedQuotesAt(ctx, latest, boundary, []uuid.UUID{entityID}, QuoteRealtimeTrade, dataservice.SourceDisplay)
	if err != nil || len(realtime.Quotes) != 0 || len(realtime.Unavailable) != 1 {
		t.Fatalf("unknown status or delay was treated as realtime trade: %+v %v", realtime, err)
	}
	if err := latest.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	objects[objectKey] = []byte("corrupt")
	mu.Unlock()
	corruptView, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SelectPublishedQuotes(ctx, corruptView, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceDisplay); err == nil {
		t.Fatal("view accepted a corrupted source object")
	}
	_ = corruptView.Rollback(ctx)
	mu.Lock()
	objects[objectKey] = saved
	mu.Unlock()
	again, err := runtime.PublishStagedQuote(ctx, id)
	if err != nil || !again.Equal(available) {
		t.Fatalf("publication retry changed its time: %v", err)
	}
	var stagedAt, eventAt time.Time
	if err := pool.QueryRow(ctx, `SELECT staged_at,event_at FROM staged_quotes WHERE id=$1`, id).Scan(&stagedAt, &eventAt); err != nil || available.Before(stagedAt) || available.Before(eventAt) {
		t.Fatalf("publication precedes its source: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE quote_publications SET available_at=clock_timestamp() WHERE staged_quote_id=$1`, id); err == nil {
		t.Fatal("publication timestamp was silently rewritten")
	}
	var recoveryPolicy uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		SELECT source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,5000,verified_by,verified_at,valid_from FROM quote_mapping_policies WHERE id=$1 RETURNING id`, policyID).Scan(&recoveryPolicy); err != nil {
		t.Fatal(err)
	}
	recoveryStage, err := runtime.StageQuote(ctx, cat, source, recoveryPolicy, entityID, "A")
	if err != nil {
		t.Fatalf("stage recovery quote: %v", err)
	}
	firstPublication, err := runtime.PublishStagedQuote(ctx, recoveryStage)
	if err != nil {
		t.Fatalf("publish recovery quote: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		var stale bool
		if err := pool.QueryRow(ctx, `SELECT clock_timestamp() > event_at + interval '5 seconds' FROM staged_quotes WHERE id=$1`, recoveryStage).Scan(&stale); err != nil {
			t.Fatal(err)
		}
		if stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quote did not become stale")
		}
		time.Sleep(25 * time.Millisecond)
	}
	recovered, err := runtime.PublishStagedQuote(ctx, recoveryStage)
	if err != nil || !recovered.Equal(firstPublication) {
		t.Fatalf("committed publication could not be recovered after becoming stale: %v", err)
	}
	var records int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM records`).Scan(&records); err != nil || records != 0 {
		t.Fatalf("unpublished quote entered research records: %d %v", records, err)
	}
	forged := source
	forged.Product = "other-product"
	if _, err := runtime.StageQuote(ctx, cat, forged, policyID, entityID, "A"); err == nil {
		t.Fatal("quote staged with the wrong product")
	}
	forged = source
	forged.Source.RequestFingerprint = digest([]byte("other"))
	if _, err := runtime.StageQuote(ctx, cat, forged, policyID, entityID, "A"); err == nil {
		t.Fatal("quote staged with a forged source")
	}
	if _, err := runtime.StageQuote(ctx, cat, source, policyID, uuid.New(), "A"); err == nil {
		t.Fatal("quote staged for a missing entity")
	}
	if _, err := pool.Exec(ctx, `UPDATE staged_quotes SET payload='{}' WHERE id=$1`, id); err == nil {
		t.Fatal("staged quote was rewritten")
	}
	if _, err := pool.Exec(ctx, `UPDATE raw_objects SET content_hash=repeat('0',64) WHERE id=$1`, source.ID); err == nil {
		t.Fatal("registered source was silently rewritten")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM raw_objects WHERE id=$1`, source.ID); err == nil {
		t.Fatal("registered source was deleted")
	}
	if _, err := pool.Exec(ctx, `UPDATE quote_mapping_policies SET latency_mode='real_time',latency_evidence_ref='unverified' WHERE id=$1`, policyID); err == nil {
		t.Fatal("mapping policy was silently modified")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-unit','real_time',60000,'synthetic-operator',clock_timestamp(),clock_timestamp())`, license); err == nil {
		t.Fatal("realtime policy without latency evidence was accepted")
	}
	var secondPolicy uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		SELECT source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from FROM quote_mapping_policies WHERE id=$1 RETURNING id`, policyID).Scan(&secondPolicy); err != nil {
		t.Fatal(err)
	}
	secondStage, err := runtime.StageQuote(ctx, cat, source, secondPolicy, entityID, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PublishStagedQuote(ctx, secondStage); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE quote_mapping_policies SET revoked_at=clock_timestamp() WHERE id=$1`, policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.StageQuote(ctx, cat, source, policyID, entityID, "A"); err == nil {
		t.Fatal("revoked policy was accepted")
	}
	if _, err := runtime.PublishStagedQuote(ctx, id); err == nil {
		t.Fatal("publication retry ignored revoked policy")
	}
	staleView, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := staleView.QueryRow(ctx, `SELECT count(*) FROM quote_publications`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, license); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SelectPublishedQuotes(ctx, staleView, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceModel); err == nil {
		t.Fatal("a view established before revocation exposed a now-revoked grant")
	}
	_ = staleView.Rollback(ctx)
	if _, err := runtime.StageQuote(ctx, cat, source, secondPolicy, entityID, "A"); err == nil {
		t.Fatal("revoked source license was accepted")
	}
	revokedView, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := runtime.SelectPublishedQuotes(ctx, revokedView, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceModel)
	if err != nil || len(denied.Quotes) != 0 || len(denied.Unavailable) != 1 {
		t.Fatalf("view exposed a revoked source: %+v %v", denied, err)
	}
	_ = revokedView.Rollback(ctx)
}
