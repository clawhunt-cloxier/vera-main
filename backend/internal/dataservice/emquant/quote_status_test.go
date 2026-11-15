package emquant

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

// All states and policies here are synthetic. They establish no Choice field
// semantics or account rights. Exercise the registered production code path.
func TestQuoteStatusPublicationAndSnapshot(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('status-test','Synthetic status test','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	var entity uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic) VALUES('stock','A','Synthetic',false) RETURNING id`).Scan(&entity); err != nil {
		t.Fatal(err)
	}
	fields := []string{"TIME", "NOW", "TESTSTATUS"}
	license := syntheticStorageLicense(t, pool, "status-test", "mkt.intraday_quote", "CSQSNAPSHOT", fields)
	denied := syntheticStorageLicense(t, pool, "status-test", "mkt.intraday_quote", "CSQSNAPSHOT", fields[:2])
	policy := func(l uuid.UUID, latency string) uuid.UUID {
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,latency_evidence_ref,max_age_ms,verified_by,verified_at,valid_from,status_indicator,status_evidence_ref,status_mapping)
  VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price',$2,'synthetic-latency',60000,'synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute','TESTSTATUS','synthetic-co-temporal-status','{"s:trading":"continuous","i:2":"suspended","u:3":"closed"}') RETURNING id`, l, latency).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	realPolicy, delayedPolicy, deniedPolicy := policy(license, "real_time"), policy(license, "delayed"), policy(denied, "real_time")
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, ok := objects[r.URL.Path]; ok {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case http.MethodGet:
			b, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	store, err := NewS3RawStore(testS3Client(srv.URL, "synthetic", "synthetic"), "synthetic", "status")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	f := &testFetcher{}
	runtime := &Runtime{client: &Client{}, store: store, database: pool, collector: Collector{Fetcher: f, Store: store, Gate: testGateFunc(func(_ context.Context, call func() error) error { return call() })}}
	setQuote := func(cell Cell) {
		now := time.Now().UTC()
		local := now.In(time.FixedZone("synthetic-CST", 8*3600))
		f.quote = Quote{ObservedAt: now, Data: Cube{Codes: []string{"A"}, Indicators: []string{"TESTSTATUS", "NOW", "TIME"}, Dates: []string{local.Format("2006-01-02")}, Values: []Cell{cell, {Type: 11, Value: 10.25}, {Type: 6, Value: int64(local.Hour()*10000 + local.Minute()*100 + local.Second())}}}}
	}
	setQuote(Cell{Type: 13, Value: "trading"})
	if _, err := runtime.CollectAndPublishQuote(ctx, cat, "status-test", "synthetic-product", denied, deniedPolicy, entity, "A"); err == nil || f.calls != 0 {
		t.Fatalf("unauthorized status reached SDK: calls=%d err=%v", f.calls, err)
	}
	published, err := runtime.CollectAndPublishQuote(ctx, cat, "status-test", "synthetic-product", license, realPolicy, entity, "A")
	if err != nil {
		t.Fatal(err)
	}
	again, err := runtime.PublishStagedQuote(ctx, published.StageID)
	if err != nil || !again.Equal(published.AvailableAt) {
		t.Fatalf("publication replay: %v", err)
	}
	ws, task := syntheticQuoteTask(t, pool, entity)
	if _, err := pool.Exec(ctx, `INSERT INTO quote_task_intents(workspace_id,task_id,epoch,plan_hash,protocol,requirement,entity_ids) VALUES($1,$2,1,'synthetic-plan-hash','0.4','realtime_trade',ARRAY[$3]::uuid[])`, ws, task, entity); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO task_snapshot_requirements(workspace_id,task_id,epoch,dataset_ids,required_ids) VALUES($1,$2,1,ARRAY['mkt.intraday_quote'],ARRAY['mkt.intraday_quote'])`, ws, task); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeLatestSnapshot(ctx, cat, ws, task, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Datasets) != 1 || snapshot.Datasets[0].State != "available" {
		t.Fatalf("positive realtime snapshot unavailable: %+v", snapshot)
	}
	read := func(use dataservice.SourceUse) error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		view, err := runtime.ReadBoundSnapshotQuote(ctx, tx, ws, task, 1, 1, snapshot.Hash, use)
		if err == nil && (len(view.Quotes) != 1 || view.Quotes[0].StageID != published.StageID || view.Quotes[0].MarketState != "continuous" || !slices.Equal(view.Quotes[0].Fields, fields) || view.Quotes[0].Locators["TESTSTATUS"] != "$.data.Values[0]") {
			t.Fatalf("snapshot lost frozen status lineage: %+v", view)
		}
		return err
	}
	if err := read(dataservice.SourceModel); err != nil {
		t.Fatal(err)
	}
	if err := read(dataservice.SourceDisplay); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		cell   Cell
		policy uuid.UUID
		state  string
	}{
		{"suspended", Cell{Type: 6, Value: int64(2)}, realPolicy, "suspended"},
		{"closed", Cell{Type: 7, Value: uint64(3)}, realPolicy, "closed"},
		{"unknown", Cell{Type: 13, Value: "new-state"}, realPolicy, "unknown"},
		{"null", Cell{Type: 0}, realPolicy, "unknown"},
		{"delayed", Cell{Type: 13, Value: "trading"}, delayedPolicy, "continuous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setQuote(tc.cell)
			p, err := runtime.CollectAndPublishQuote(ctx, cat, "status-test", "synthetic-product", license, tc.policy, entity, "A")
			if err != nil {
				t.Fatal(err)
			}
			var state string
			if err := pool.QueryRow(ctx, `SELECT payload->>'market_status' FROM staged_quotes WHERE id=$1`, p.StageID).Scan(&state); err != nil || state != tc.state {
				t.Fatalf("state %s err=%v", state, err)
			}
			// A newer non-realtime observation must not change the bound snapshot.
			if err := read(dataservice.SourceModel); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			view, err := runtime.SelectPublishedQuotes(ctx, tx, []uuid.UUID{entity}, QuoteRealtimeTrade, dataservice.SourceModel)
			if err != nil || len(view.Quotes) != 0 || len(view.Unavailable) != 1 {
				t.Fatalf("new task fell back to an older continuous quote after %s: %+v err=%v", tc.name, view, err)
			}
		})
	}
	if _, err := pool.Exec(ctx, `UPDATE quote_mapping_policies SET revoked_at=clock_timestamp() WHERE id=$1`, realPolicy); err != nil {
		t.Fatal(err)
	}
	if err := read(dataservice.SourceDisplay); err == nil {
		t.Fatal("revoked policy still displayed")
	}
	if err := read(dataservice.SourceModel); err == nil {
		t.Fatal("revoked policy still sent to model")
	}
}
