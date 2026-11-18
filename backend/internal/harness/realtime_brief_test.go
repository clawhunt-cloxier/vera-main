//go:build linux && cgo

package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
)

// The ABI, grants and prices are synthetic. This exercises the real
// registration/publication/freeze/model/delivery flow without vendor access.
func TestRealtimeBriefPipelineAndRecovery(t *testing.T) {
	ctx := context.Background()
	e, pool := testEngine(t)
	e.Now, e.RealResearch = func() time.Time { return time.Now().UTC() }, true
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, exists := objects[r.URL.Path]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case http.MethodGet:
			body, exists := objects[r.URL.Path]
			if !exists {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	library := filepath.Join(t.TempDir(), "synthetic-realtime.so")
	if out, err := exec.Command("cc", "-shared", "-fPIC", "-o", library, "../dataservice/emquant/testdata/realtime_brief.c").CombinedOutput(); err != nil {
		t.Fatalf("compile synthetic ABI: %s %v", out, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('realtime-brief-test','Synthetic realtime test','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"EMQUANT_LIBRARY": library, "EMQUANT_USER": "user", "EMQUANT_PASSWORD": "pass", "EMQUANT_ACCOUNT_SCOPE": "synthetic-realtime-test", "EMQUANT_S3_ENDPOINT": server.URL,
		"EMQUANT_S3_REGION": "test", "EMQUANT_S3_ACCESS_KEY": "synthetic", "EMQUANT_S3_SECRET_KEY": "synthetic", "EMQUANT_S3_BUCKET": "synthetic", "EMQUANT_PROVIDER_ID": "realtime-brief-test", "EMQUANT_PRODUCT": "synthetic", "EMQUANT_MIN_INTERVAL": "90ms", "EMQUANT_RATE_COOLDOWN": "1s"}
	brief, err := emquant.OpenBriefFromEnv(ctx, pool, e.Store.Catalog(), func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	defer brief.Runtime.Close()
	e.Brief, e.Skills["stock_analysis"] = brief, ObservationBriefSkill()
	source := BriefQuoteSource{ProviderID: brief.ProviderID, Product: brief.Product}
	var licenseID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
 VALUES($1,$2,'mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW','TESTSTATUS'],'all','{}','synthetic-grant','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute',true,true,true) RETURNING id`, source.ProviderID, source.Product).Scan(&licenseID); err != nil {
		t.Fatal(err)
	}
	policy := func(latency string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,latency_evidence_ref,max_age_ms,verified_by,verified_at,valid_from,status_indicator,status_evidence_ref,status_mapping)
  VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price',$2,'synthetic-latency',60000,'synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute','TESTSTATUS','synthetic-status','{"s:OPEN":"continuous","s:CLOSED":"closed"}') RETURNING id`, licenseID, latency).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	_ = policy("real_time")
	e.RealtimeSource = &source
	ws := workspace(t, pool)
	e.Store = dataservice.NewVendorStore(pool, e.Store.Catalog())
	subjects := map[string]dataservice.EntityRow{}
	for _, code := range []string{"600000.SH", "600001.SH", "600002.SH"} {
		subject, err := e.Store.UpsertSearchEntity(ctx, "stock", code, "Synthetic stock "+code, nil)
		if err != nil {
			t.Fatal(err)
		}
		subjects[code] = subject
	}
	create := func(code, requirement string) (*Plan, uuid.UUID) {
		t.Helper()
		p, err := e.BuildRealtimePlan(ctx, ws, "stock_analysis", subjects[code], map[string]any{}, requirement)
		if err != nil || !p.Executable {
			t.Fatalf("plan: %+v %v", p, err)
		}
		id, err := e.CreateTask(ctx, ws, p.ID, p.Hash, []string{"S3"})
		if err != nil {
			t.Fatal(err)
		}
		return p, id
	}
	run := func(id uuid.UUID) string {
		t.Helper()
		if err := e.Run(ctx, ws, id, 1); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := pool.QueryRow(ctx, `SELECT status FROM research_tasks WHERE id=$1`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	plan, id := create("600000.SH", "realtime_trade")
	again, err := e.CreateTask(ctx, ws, plan.ID, plan.Hash, nil)
	if err != nil || again != id {
		t.Fatalf("duplicate confirmation: %v %v", again, err)
	}
	if state := run(id); state != "COMPLETED" {
		t.Fatalf("real-time task state: %s", state)
	}
	var body []byte
	var hash string
	var cutoff time.Time
	if err := pool.QueryRow(ctx, `SELECT v.document,t.snapshot_as_of,t.snapshot_manifest_hash FROM research_tasks t JOIN report_versions v ON v.task_id=t.id WHERE t.id=$1`, id).Scan(&body, &cutoff, &hash); err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SkillVersion != RealtimeBriefVersion || doc.ResultKind != "limited" || len(doc.Claims) == 0 || len(doc.SourceFreshness) != 1 || cutoff.Before(plan.AsOf) || !doc.AsOf.Equal(plan.AsOf) {
		t.Fatalf("invalid realtime report: %+v", doc)
	}
	for _, claim := range doc.Claims {
		if len(claim.EvidenceRefs) == 0 {
			t.Fatal("report claim missing evidence")
		}
	}
	if err := brief.Runtime.WithTaskSnapshotQuote(ctx, uuid.New(), id, dataservice.SourceDisplay, func(emquant.QuoteView) error { t.Fatal("cross-workspace quote"); return nil }); err == nil {
		t.Fatal("cross-workspace access accepted")
	}
	if state := run(id); state != "COMPLETED" {
		t.Fatal("redelivery changed completed state")
	}
	var reports int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_versions WHERE task_id=$1`, id).Scan(&reports); err != nil || reports != 1 {
		t.Fatalf("duplicate report: %d %v", reports, err)
	}
	var replayHash string
	if err := pool.QueryRow(ctx, `SELECT snapshot_manifest_hash FROM research_tasks WHERE id=$1`, id).Scan(&replayHash); err != nil || replayHash != hash {
		t.Fatal("redelivery changed snapshot")
	}

	// Crash after binding but before completing the collect ledger. An archive
	// runtime cannot call the SDK: recovery proves the old snapshot is reused.
	_, recoveryID := create("600000.SH", "realtime_trade")
	tr, err := e.loadTask(ctx, ws, recoveryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.transition(ctx, tr, []string{"QUEUED"}, "COLLECTING", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := brief.Runtime.CollectTaskQuotesDirect(ctx, brief.Catalog, source.ProviderID, source.Product, ws, recoveryID, 1, tr.Revision); err != nil {
		t.Fatal(err)
	}
	frozen, err := brief.Runtime.FreezeLatestSnapshot(ctx, brief.Catalog, ws, recoveryID, 1, tr.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.transition(ctx, tr, []string{"COLLECTING"}, "ANALYZING", "", ""); err != nil {
		t.Fatal(err)
	}
	object := s3.New(s3.Options{Region: "test", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
	})})
	archive, err := emquant.OpenArchiveRuntime(pool, object, "synthetic", "")
	if err != nil {
		t.Fatal(err)
	}
	e.Brief = &emquant.BriefService{Runtime: archive, Catalog: brief.Catalog, ProviderID: brief.ProviderID, Product: brief.Product}
	if state := run(recoveryID); state != "COMPLETED" {
		t.Fatalf("snapshot recovery state %s", state)
	}
	if err := pool.QueryRow(ctx, `SELECT snapshot_manifest_hash FROM research_tasks WHERE id=$1`, recoveryID).Scan(&replayHash); err != nil || replayHash != frozen.Hash {
		t.Fatal("recovery selected a new snapshot")
	}
	e.Brief = brief
	_, closedLive := create("600001.SH", "realtime_trade")
	if state := run(closedLive); state != "ABSTAINED" {
		t.Fatalf("closed quote passed realtime: %s", state)
	}
	_, closedLatest := create("600001.SH", "latest_available")
	if state := run(closedLatest); state != "COMPLETED" {
		t.Fatalf("latest available rejected closed quote: %s", state)
	}
	if err := pool.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, closedLatest).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "非交易状态") {
		t.Fatal("closed quote limitation missing")
	}
	_, staleID := create("600002.SH", "realtime_trade")
	if state := run(staleID); state != "ABSTAINED" {
		t.Fatalf("expired quote passed: %s", state)
	}
	_ = policy("delayed")
	denied, err := e.BuildRealtimePlan(ctx, ws, "stock_analysis", subjects["600000.SH"], nil, "realtime_trade")
	if err != nil || denied.Executable {
		t.Fatalf("delayed policy offered realtime: %+v %v", denied, err)
	}
	_, delayedID := create("600000.SH", "latest_available")
	if state := run(delayedID); state != "COMPLETED" {
		t.Fatalf("delayed latest quote failed: %s", state)
	}
	if err := pool.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, delayedID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "延迟行情") {
		t.Fatal("delayed quote limitation missing")
	}
	// The initial MVP has no verified status indicator or latency declaration.
	if _, err := pool.Exec(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price','unknown',60000,'synthetic-operator',clock_timestamp(),clock_timestamp())`, licenseID); err != nil {
		t.Fatal(err)
	}
	_, latestID := create("600001.SH", "latest_available")
	if state := run(latestID); state != "COMPLETED" {
		t.Fatalf("two-field snapshot failed: %s", state)
	}
	if err := pool.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, latestID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "行情延迟性质未确认") || !strings.Contains(string(body), "交易状态未确认") {
		t.Fatal("report omitted unknown latency or market status")
	}
	_, cancelled := create("600000.SH", "latest_available")
	if _, _, err := e.CancelTask(ctx, ws, cancelled, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(ctx, ws, cancelled, 1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_versions WHERE task_id=$1`, cancelled).Scan(&reports); err != nil || reports != 0 {
		t.Fatal("cancelled task delivered")
	}
	unconfirmed, err := e.BuildRealtimePlan(ctx, ws, "stock_analysis", subjects["600000.SH"], nil, "latest_available")
	if err != nil || !unconfirmed.Executable {
		t.Fatalf("pending plan: %+v %v", unconfirmed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, licenseID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateTask(ctx, ws, unconfirmed.ID, unconfirmed.Hash, nil); err == nil {
		t.Fatal("revoked source confirmed a task")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM research_tasks WHERE plan_id=$1`, unconfirmed.ID).Scan(&reports); err != nil || reports != 0 {
		t.Fatal("failed confirmation retained a task")
	}
	if err := brief.Runtime.WithTaskSnapshotQuote(ctx, ws, id, dataservice.SourceDisplay, func(emquant.QuoteView) error { t.Fatal("revoked source displayed"); return nil }); err == nil {
		t.Fatal("source revocation ignored")
	}
}
