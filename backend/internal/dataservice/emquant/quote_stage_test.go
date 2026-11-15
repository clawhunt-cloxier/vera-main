package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func syntheticQuoteTask(t *testing.T, pool *pgxpool.Pool, entity uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var user, workspace, plan, task uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('synthetic',$1) RETURNING id`, uuid.NewString()).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, user).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO research_plans(workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,expires_at)
		VALUES($1,'synthetic-skill','0.4',$2,'{}','[]',clock_timestamp(),'synthetic-plan-hash',true,clock_timestamp()+interval '1 hour') RETURNING id`, workspace, entity).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO research_tasks(workspace_id,plan_id,skill_id,skill_version,subject_id,params,sections,as_of,status,deadline,budget)
		VALUES($1,$2,'synthetic-skill','0.4',$3,'{}','[]',clock_timestamp(),'COLLECTING',clock_timestamp()+interval '1 hour','{}') RETURNING id`, workspace, plan, entity).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE research_plans SET consumed_task_id=$2 WHERE id=$1`, plan, task); err != nil {
		t.Fatal(err)
	}
	return workspace, task
}

func syntheticQuoteIntent(t *testing.T, pool *pgxpool.Pool, workspace, task, entity uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO quote_task_intents(workspace_id,task_id,epoch,plan_hash,protocol,requirement,entity_ids)
		VALUES($1,$2,1,'synthetic-plan-hash','0.4','latest_available',ARRAY[$3]::uuid[])`, workspace, task, entity); err != nil {
		t.Fatal(err)
	}
}

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
	var blockNextGet bool
	getStarted := make(chan struct{}, 1)
	releaseGet := make(chan struct{})
	var releaseOnce sync.Once
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
			if blockNextGet {
				blockNextGet = false
				getStarted <- struct{}{}
				<-releaseGet
			}
			_, _ = w.Write(body)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(releaseGet) })
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
	workspace, task := syntheticQuoteTask(t, pool, entityID)
	noIntent, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer noIntent.Rollback(context.Background())
	noIntentBoundary, err := BeginQuoteBoundary(ctx, noIntent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FreezeQuoteComponent(ctx, noIntent, noIntentBoundary, workspace, task, 1, 1); err == nil {
		t.Fatal("legacy task without confirmed 0.4 quote intent was accepted")
	}
	_ = noIntent.Rollback(ctx)
	syntheticQuoteIntent(t, pool, workspace, task, entityID)
	if _, err := pool.Exec(ctx, `UPDATE research_plans SET plan_hash='changed-plan' WHERE id=(SELECT plan_id FROM research_tasks WHERE id=$1)`, task); err != nil {
		t.Fatal(err)
	}
	changedPlan, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer changedPlan.Rollback(context.Background())
	changedBoundary, err := BeginQuoteBoundary(ctx, changedPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FreezeQuoteComponent(ctx, changedPlan, changedBoundary, workspace, task, 1, 1); err == nil {
		t.Fatal("changed plan was accepted by the original quote intent")
	}
	_ = changedPlan.Rollback(ctx)
	if _, err := pool.Exec(ctx, `UPDATE research_plans SET plan_hash='synthetic-plan-hash' WHERE id=(SELECT plan_id FROM research_tasks WHERE id=$1)`, task); err != nil {
		t.Fatal(err)
	}
	aborted, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer aborted.Rollback(context.Background())
	abortedBoundary, err := BeginQuoteBoundary(ctx, aborted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FreezeQuoteComponent(ctx, aborted, abortedBoundary, workspace, task, 2, 1); err == nil {
		t.Fatal("stale epoch was accepted")
	}
	if _, err := runtime.FreezeQuoteComponent(ctx, aborted, abortedBoundary, workspace, task, 1, 2); err == nil {
		t.Fatal("stale revision was accepted")
	}
	if _, err := runtime.FreezeQuoteComponent(ctx, aborted, abortedBoundary, uuid.New(), task, 1, 1); err == nil {
		t.Fatal("cross-workspace component was accepted")
	}
	provisional, err := runtime.FreezeQuoteComponent(ctx, aborted, abortedBoundary, workspace, task, 1, 1)
	if err != nil || provisional.ContentHash == "" {
		t.Fatalf("freeze quote component: %v", err)
	}
	repeated, err := runtime.FreezeQuoteComponent(ctx, aborted, abortedBoundary, workspace, task, 1, 1)
	if err != nil || repeated.ContentHash != provisional.ContentHash || !repeated.Cutoff.Equal(provisional.Cutoff) {
		t.Fatalf("same-view retry changed quote component: %v", err)
	}
	var components int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_task_components WHERE task_id=$1`, task).Scan(&components); err != nil || components != 0 {
		t.Fatalf("uncommitted component became visible: %d %v", components, err)
	}
	if err := aborted.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_task_components WHERE task_id=$1`, task).Scan(&components); err != nil || components != 0 {
		t.Fatalf("rolled-back component persisted: %d %v", components, err)
	}
	binding, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Rollback(context.Background())
	bindingBoundary, err := BeginQuoteBoundary(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := runtime.FreezeQuoteComponent(ctx, binding, bindingBoundary, workspace, task, 1, 1)
	if err != nil || bound.ContentHash == "" {
		t.Fatalf("bind quote component: %v", err)
	}
	if err := binding.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	authorization, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer authorization.Rollback(context.Background())
	if err := runtime.ReauthorizeQuoteComponent(ctx, authorization, uuid.New(), task, 1, bound.ContentHash, dataservice.SourceDisplay); err == nil {
		t.Fatal("quote component authorization crossed workspaces")
	}
	for _, use := range []dataservice.SourceUse{dataservice.SourceDisplay, dataservice.SourceModel} {
		if err := runtime.ReauthorizeQuoteComponent(ctx, authorization, workspace, task, 1, bound.ContentHash, use); err != nil {
			t.Fatalf("frozen component %s authorization: %v", use, err)
		}
	}
	if err := authorization.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='stage-test'`); err != nil {
		t.Fatal(err)
	}
	modelDenied, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReauthorizeQuoteComponent(ctx, modelDenied, workspace, task, 1, bound.ContentHash, dataservice.SourceModel); err == nil {
		t.Fatal("frozen component bypassed current model denial")
	}
	if err := runtime.ReauthorizeQuoteComponent(ctx, modelDenied, workspace, task, 1, bound.ContentHash, dataservice.SourceDisplay); err != nil {
		t.Fatalf("model denial incorrectly revoked display: %v", err)
	}
	_ = modelDenied.Rollback(ctx)
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=true WHERE id='stage-test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE quote_task_components SET manifest='tampered'::bytea WHERE task_id=$1`, task); err == nil {
		t.Fatal("frozen quote component was modified")
	}
	newView, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer newView.Rollback(context.Background())
	newBoundary, err := BeginQuoteBoundary(ctx, newView)
	if err != nil {
		t.Fatal(err)
	}
	freshPart, err := runtime.FreezeQuoteComponent(ctx, newView, newBoundary, workspace, task, 1, 1)
	if err != nil || freshPart.ContentHash == bound.ContentHash || freshPart.Cutoff.Equal(bound.Cutoff) {
		t.Fatalf("a new MVCC view reused an old unbound component: %v", err)
	}
	_ = newView.Rollback(ctx)
	staleWS, staleTask := syntheticQuoteTask(t, pool, entityID)
	syntheticQuoteIntent(t, pool, staleWS, staleTask, entityID)
	staleTaskView, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer staleTaskView.Rollback(context.Background())
	staleBoundary, err := BeginQuoteBoundary(ctx, staleTaskView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE research_tasks SET epoch=epoch+1,revision=revision+1,status='CANCEL_REQUESTED' WHERE id=$1`, staleTask); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FreezeQuoteComponent(ctx, staleTaskView, staleBoundary, staleWS, staleTask, 1, 1); err == nil {
		t.Fatal("stale MVCC view froze a cancelled epoch")
	}
	_ = staleTaskView.Rollback(ctx)
	cancelWS, cancelTask := syntheticQuoteTask(t, pool, entityID)
	syntheticQuoteIntent(t, pool, cancelWS, cancelTask, entityID)
	cancelCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cancelView, err := pool.BeginTx(cancelCtx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer cancelView.Rollback(context.Background())
	defer releaseOnce.Do(func() { close(releaseGet) })
	cancelBoundary, err := BeginQuoteBoundary(cancelCtx, cancelView)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	blockNextGet = true
	mu.Unlock()
	freezeDone := make(chan error, 1)
	go func() {
		_, err := runtime.FreezeQuoteComponent(cancelCtx, cancelView, cancelBoundary, cancelWS, cancelTask, 1, 1)
		freezeDone <- err
	}()
	select {
	case <-getStarted:
	case err := <-freezeDone:
		t.Fatalf("freeze did not reach object read: %v", err)
	case <-cancelCtx.Done():
		t.Fatal(cancelCtx.Err())
	}
	cancelDone := make(chan error, 1)
	go func() {
		_, err := pool.Exec(cancelCtx, `UPDATE research_tasks SET epoch=epoch+1,revision=revision+1,status='CANCEL_REQUESTED' WHERE id=$1`, cancelTask)
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-cancelCtx.Done():
		t.Fatal("cancellation was blocked by the object read")
	}
	releaseOnce.Do(func() { close(releaseGet) })
	select {
	case err := <-freezeDone:
		if err == nil {
			t.Fatal("in-flight freeze committed a cancelled epoch")
		}
	case <-cancelCtx.Done():
		t.Fatal(cancelCtx.Err())
	}
	_ = cancelView.Rollback(context.Background())
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_task_components WHERE task_id=$1`, cancelTask).Scan(&components); err != nil || components != 0 {
		t.Fatalf("cancelled in-flight freeze stored a component: %d %v", components, err)
	}
	var appCanRead, appCanInsert bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('vera_app','quote_task_components','SELECT'),has_table_privilege('vera_app','quote_task_intents','INSERT')`).Scan(&appCanRead, &appCanInsert); err != nil || appCanRead || appCanInsert {
		t.Fatalf("workspace role can bypass quote component controls: %v", err)
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
	corruptAuthorization, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReauthorizeQuoteComponent(ctx, corruptAuthorization, workspace, task, 1, bound.ContentHash, dataservice.SourceDisplay); err == nil {
		t.Fatal("component authorization accepted a corrupted source")
	}
	_ = corruptAuthorization.Rollback(ctx)
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
		SELECT source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,15000,verified_by,verified_at,valid_from FROM quote_mapping_policies WHERE id=$1 RETURNING id`, policyID).Scan(&recoveryPolicy); err != nil {
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
	deadline := time.Now().Add(18 * time.Second)
	for {
		var stale bool
		if err := pool.QueryRow(ctx, `SELECT clock_timestamp() > event_at + interval '15 seconds' FROM staged_quotes WHERE id=$1`, recoveryStage).Scan(&stale); err != nil {
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
	historicalAuthorization, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReauthorizeQuoteComponent(ctx, historicalAuthorization, workspace, task, 1, bound.ContentHash, dataservice.SourceDisplay); err != nil {
		t.Fatalf("frozen quote was reselected using current freshness: %v", err)
	}
	_ = historicalAuthorization.Rollback(ctx)
	// Freeze a component under a short-lived mapping policy. Its historical
	// validity must not authorize display after that policy has expired.
	var expiringPolicy uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from,valid_until)
		SELECT source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from,clock_timestamp()+interval '5 seconds' FROM quote_mapping_policies WHERE id=$1 RETURNING id`, policyID).Scan(&expiringPolicy); err != nil {
		t.Fatal(err)
	}
	expiringStage, err := runtime.StageQuote(ctx, cat, source, expiringPolicy, entityID, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PublishStagedQuote(ctx, expiringStage); err != nil {
		t.Fatal(err)
	}
	expiringWS, expiringTask := syntheticQuoteTask(t, pool, entityID)
	syntheticQuoteIntent(t, pool, expiringWS, expiringTask, entityID)
	expiringTx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	expiringBoundary, err := BeginQuoteBoundary(ctx, expiringTx)
	if err != nil {
		t.Fatal(err)
	}
	expiringComponent, err := runtime.FreezeQuoteComponent(ctx, expiringTx, expiringBoundary, expiringWS, expiringTask, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := expiringTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Verify the newly published policy is the one frozen in the component.
	var manifest []byte
	if err := pool.QueryRow(ctx, `SELECT manifest FROM quote_task_components WHERE task_id=$1 AND content_hash=$2`, expiringTask, expiringComponent.ContentHash).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	var expiringDoc quoteComponentDocument
	if err := json.Unmarshal(manifest, &expiringDoc); err != nil || len(expiringDoc.View.Quotes) != 1 || expiringDoc.View.Quotes[0].PolicyID != expiringPolicy {
		t.Fatal("component did not select expiring policy")
	}
	expiryCtx, expiryCancel := context.WithTimeout(ctx, 12*time.Second)
	defer expiryCancel()
	inFlightTx, err := pool.BeginTx(expiryCtx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer inFlightTx.Rollback(context.Background())
	mu.Lock()
	releaseGet = make(chan struct{})
	blockNextGet = true
	mu.Unlock()
	defer func() {
		select {
		case <-releaseGet:
		default:
			close(releaseGet)
		}
	}()
	inFlightDone := make(chan error, 1)
	go func() {
		inFlightDone <- runtime.ReauthorizeQuoteComponent(expiryCtx, inFlightTx, expiringWS, expiringTask, 1, expiringComponent.ContentHash, dataservice.SourceDisplay)
	}()
	select {
	case <-getStarted:
	case err := <-inFlightDone:
		t.Fatalf("authorization did not reach archive read: %v", err)
	case <-expiryCtx.Done():
		t.Fatal(expiryCtx.Err())
	}
	if _, err := pool.Exec(expiryCtx, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM (SELECT valid_until-clock_timestamp() FROM quote_mapping_policies WHERE id=$1)))+0.05)`, expiringPolicy); err != nil {
		t.Fatal(err)
	}
	close(releaseGet)
	select {
	case err := <-inFlightDone:
		if err == nil {
			t.Fatal("mapping expired during archive read but authorization succeeded")
		}
	case <-expiryCtx.Done():
		t.Fatal(expiryCtx.Err())
	}
	_ = inFlightTx.Rollback(ctx)
	expiredTx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReauthorizeQuoteComponent(ctx, expiredTx, expiringWS, expiringTask, 1, expiringComponent.ContentHash, dataservice.SourceDisplay); err == nil {
		t.Fatal("expired mapping authorized frozen quote")
	}
	_ = expiredTx.Rollback(ctx)
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
	// Exercise the complete ingestion path with a synthetic SDK; the result is
	// still only a published candidate and cannot stand in for a task snapshot.
	runtime.client = &Client{}
	runtime.collector = collector.Collector
	fresh := time.Now().UTC()
	freshLocal := fresh.In(time.FixedZone("CST", 8*60*60))
	f.quote.ObservedAt = fresh
	f.quote.Data.Dates = []string{freshLocal.Format("2006-01-02")}
	f.quote.Data.Values[1].Value = int64(freshLocal.Hour()*10000 + freshLocal.Minute()*100 + freshLocal.Second())
	beforeCalls := f.calls
	if _, err := runtime.CollectAndPublishQuote(ctx, cat, "stage-test", "synthetic-product", license, uuid.New(), entityID, "A"); err == nil || f.calls != beforeCalls {
		t.Fatal("invalid policy consumed a vendor request")
	}
	if _, err := runtime.CollectAndPublishQuote(ctx, cat, "stage-test", "synthetic-product", license, secondPolicy, entityID, "B"); err == nil || f.calls != beforeCalls {
		t.Fatal("mismatched entity consumed a vendor request")
	}
	ingested, err := runtime.CollectAndPublishQuote(ctx, cat, "stage-test", "synthetic-product", license, secondPolicy, entityID, "A")
	if err != nil || ingested.StageID == uuid.Nil || ingested.AvailableAt.IsZero() || f.calls != beforeCalls+1 {
		t.Fatalf("collect and publish quote: %v", err)
	}
	var sourceLicense uuid.UUID
	var availableAt time.Time
	if err := pool.QueryRow(ctx, `SELECT o.source_license_id,p.available_at FROM quote_publications p JOIN staged_quotes s ON s.id=p.staged_quote_id JOIN raw_objects o ON o.id=s.raw_object_id WHERE s.id=$1`, ingested.StageID).Scan(&sourceLicense, &availableAt); err != nil || sourceLicense != license || !availableAt.Equal(ingested.AvailableAt) {
		t.Fatalf("published quote lost its licensed archive: %v", err)
	}
	viewTx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	ingestedView, err := runtime.SelectPublishedQuotes(ctx, viewTx, []uuid.UUID{entityID}, QuoteLatestAvailable, dataservice.SourceDisplay)
	if err != nil || len(ingestedView.Quotes) != 1 || ingestedView.Quotes[0].StageID != ingested.StageID {
		t.Fatalf("newly ingested quote unavailable in fixed view: %v", err)
	}
	_ = viewTx.Rollback(ctx)
	f.err = errors.New("synthetic upstream failure")
	failed, err := runtime.CollectAndPublishQuote(ctx, cat, "stage-test", "synthetic-product", license, secondPolicy, entityID, "A")
	if err == nil || failed.StageID != uuid.Nil || !failed.AvailableAt.IsZero() {
		t.Fatal("upstream failure was presented as a published quote")
	}
	f.err = nil
	if _, err := pool.Exec(ctx, `UPDATE quote_mapping_policies SET revoked_at=clock_timestamp() WHERE id=$1`, policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.StageQuote(ctx, cat, source, policyID, entityID, "A"); err == nil {
		t.Fatal("revoked policy was accepted")
	}
	if _, err := runtime.PublishStagedQuote(ctx, id); err == nil {
		t.Fatal("publication retry ignored revoked policy")
	}
	revokedAuthorization, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReauthorizeQuoteComponent(ctx, revokedAuthorization, workspace, task, 1, bound.ContentHash, dataservice.SourceDisplay); err == nil {
		t.Fatal("component authorization ignored policy revocation")
	}
	_ = revokedAuthorization.Rollback(ctx)
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
