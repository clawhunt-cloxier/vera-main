//go:build linux && cgo

package httpapi

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

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
)

// The SDK ABI, prices, source grants and model are synthetic. The HTTP paths
// exercise actual archive registration, publication, snapshot freeze and
// Harness delivery; no report or evidence document is fabricated by the test.
func TestRealtimeDeliveredReportEvidenceAndPDF(t *testing.T) {
	e, entity := realtimeAPIEnv(t)
	ctx := context.Background()
	var mu sync.Mutex
	objects := map[string][]byte{}
	archive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer archive.Close()
	library := filepath.Join(t.TempDir(), "synthetic-realtime-api.so")
	if out, err := exec.Command("cc", "-shared", "-fPIC", "-o", library, "../dataservice/emquant/testdata/realtime_brief.c").CombinedOutput(); err != nil {
		t.Fatalf("compile ABI: %s %v", out, err)
	}
	env := map[string]string{"EMQUANT_LIBRARY": library, "EMQUANT_USER": "user", "EMQUANT_PASSWORD": "pass", "EMQUANT_ACCOUNT_SCOPE": "synthetic-realtime-api", "EMQUANT_S3_ENDPOINT": archive.URL,
		"EMQUANT_S3_REGION": "test", "EMQUANT_S3_ACCESS_KEY": "synthetic", "EMQUANT_S3_SECRET_KEY": "synthetic", "EMQUANT_S3_BUCKET": "synthetic", "EMQUANT_PROVIDER_ID": "api-test", "EMQUANT_PRODUCT": "synthetic-product", "EMQUANT_MIN_INTERVAL": "90ms", "EMQUANT_RATE_COOLDOWN": "1s"}
	brief, err := emquant.OpenBriefFromEnv(ctx, e.pool, e.engine.Store.Catalog(), func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	defer brief.Runtime.Close()
	e.engine.Brief = brief
	// A new policy identity makes the test ABI's status field explicit; the
	// earlier protocol-test policy is immutable and is never overwritten.
	source := *e.engine.RealtimeSource
	if err := e.pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
	VALUES('api-test','synthetic-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW','TESTSTATUS'],'listed',ARRAY['600000.SH'],'synthetic-abi-grant','synthetic-test',clock_timestamp(),clock_timestamp()-interval '1 minute',true,true,true) RETURNING id`).Scan(&source.LicenseID); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,latency_evidence_ref,max_age_ms,verified_by,verified_at,valid_from,status_indicator,status_evidence_ref,status_mapping)
	VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price','real_time','synthetic-latency',60000,'synthetic-test',clock_timestamp(),clock_timestamp()-interval '1 minute','TESTSTATUS','synthetic-status','{"s:OPEN":"continuous"}') RETURNING id`, source.LicenseID).Scan(&source.PolicyID); err != nil {
		t.Fatal(err)
	}
	e.engine.RealtimeSource = &source
	c := onboardAPIClient(t, e, "realtime-delivery")
	k := newContract(t)
	request := func(c *client, method, path string, body any, media string, want int) []byte {
		t.Helper()
		code, data, headers := c.do(method, path, body, apiHeaders(media))
		if code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, code, want, data)
		}
		if want < 300 && strings.Split(headers.Get("Content-Type"), ";")[0] != media {
			t.Fatalf("wrong media %s: %s", path, headers.Get("Content-Type"))
		}
		return data
	}
	var me Me
	if err := json.Unmarshal(request(c, "GET", "/api/v1/me", nil, realtimeMedia, 200), &me); err != nil || me.WorkspaceID == nil {
		t.Fatalf("workspace: %v", err)
	}
	var plan PlanResult
	if err := json.Unmarshal(request(c, "POST", "/api/v1/research-plans", map[string]any{"skill_id": "stock_analysis", "subject_id": entity, "params": map[string]any{}, "quote_requirement": "realtime_trade"}, realtimeMedia, 200), &plan); err != nil || plan.Plan == nil || !plan.Plan.Executable {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	var task Task
	confirm := map[string]any{"plan_id": plan.Plan.ID, "plan_hash": plan.Plan.PlanHash, "disabled_sections": []string{"S3"}}
	if err := json.Unmarshal(request(c, "POST", "/api/v1/research-tasks", confirm, realtimeMedia, 202), &task); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.Run(ctx, *me.WorkspaceID, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request(c, "GET", "/api/v1/research-tasks/"+task.ID.String(), nil, realtimeMedia, 200), &task); err != nil || task.Status != "COMPLETED" || task.ReportRef == nil {
		t.Fatalf("delivery: %+v %v", task, err)
	}
	reportPath := "/api/v1/reports/" + task.ReportRef.ID.String() + "/versions/1"
	data := request(c, "GET", reportPath, nil, realtimeMedia, 200)
	k.check(t, schema("ReportV04"), data)
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.SnapshotMode != "collect_latest" || report.SnapshotAsOf == nil || report.SnapshotAsOf.Before(plan.Plan.AsOf) || !report.AsOf.Equal(task.AsOf) || !report.AsOf.Equal(plan.Plan.AsOf) || !report.SnapshotAsOf.Equal(*task.SnapshotAsOf) || len(report.SourceFreshness) != 1 || len(report.Claims) == 0 {
		t.Fatalf("invalid source metadata: %+v", report)
	}
	if len(report.Sources) != 1 || report.Sources[0].Provider != "Synthetic API test provider" {
		t.Fatalf("report named a provider outside the frozen source: %+v", report.Sources)
	}
	fresh := report.SourceFreshness[0]
	if fresh.EventAt == nil || fresh.ReceivedAt == nil || fresh.AvailableAt == nil || fresh.EventAt.After(*fresh.ReceivedAt) || fresh.ReceivedAt.After(*fresh.AvailableAt) || fresh.AvailableAt.After(*report.SnapshotAsOf) || fresh.LatencyMode != "real_time" || fresh.Status != "fresh" {
		t.Fatalf("wrong source chronology: %+v", fresh)
	}
	if len(report.Claims[0].EvidenceRefs) == 0 {
		t.Fatal("missing evidence binding")
	}
	evidenceID := report.Claims[0].EvidenceRefs[0]
	evidencePath := "/api/v1/evidence/" + evidenceID.String()
	data = request(c, "GET", evidencePath, nil, realtimeMedia, 200)
	k.check(t, schema("EvidenceV04"), data)
	var evidence Evidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.EventAt == nil || evidence.ReceivedAt == nil || !evidence.EventAt.Equal(*fresh.EventAt) || !evidence.ReceivedAt.Equal(*fresh.ReceivedAt) || !evidence.AvailableAt.Equal(*fresh.AvailableAt) || evidence.LatencyMode != "real_time" || evidence.ID != evidenceID || evidence.Value == nil || *evidence.Value != "10.125" {
		t.Fatalf("evidence not from frozen quote: %+v", evidence)
	}
	if data := request(c, "GET", reportPath+"/pdf", nil, "application/pdf", 200); !strings.HasPrefix(string(data), "%PDF") {
		t.Fatal("invalid PDF")
	}
	// Older representations may read delivered facts while receiving only
	// their known fields. Snapshot mode is retained for observation.v1.
	for _, media := range []string{legacyMedia, observationMedia} {
		body := request(c, "GET", reportPath, nil, media, 200)
		k.check(t, schema("Report"), body)
		if strings.Contains(string(body), `"source_freshness"`) || (media == legacyMedia && strings.Contains(string(body), `"snapshot_mode"`)) {
			t.Fatalf("versioned report fields leaked to %s", media)
		}
		body = request(c, "GET", evidencePath, nil, media, 200)
		k.check(t, schema("Evidence"), body)
		if media == legacyMedia && (strings.Contains(string(body), `"event_at"`) || strings.Contains(string(body), `"latency_mode"`)) {
			t.Fatal("0.4 evidence fields leaked to legacy JSON")
		}
	}
	other := onboardAPIClient(t, e, "realtime-delivery-other")
	for _, path := range []string{reportPath, evidencePath, reportPath + "/pdf"} {
		request(other, "GET", path, nil, realtimeMedia, 404)
	}
	var firstHash string
	var firstCutoff time.Time
	if err := e.pool.QueryRow(ctx, `SELECT snapshot_manifest_hash,snapshot_as_of FROM research_tasks WHERE id=$1`, task.ID).Scan(&firstHash, &firstCutoff); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.Run(ctx, *me.WorkspaceID, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	var replayHash string
	var replayCutoff time.Time
	var reports int
	if err := e.pool.QueryRow(ctx, `SELECT snapshot_manifest_hash,snapshot_as_of,(SELECT count(*) FROM report_versions WHERE task_id=$1) FROM research_tasks WHERE id=$1`, task.ID).Scan(&replayHash, &replayCutoff, &reports); err != nil || replayHash != firstHash || !replayCutoff.Equal(firstCutoff) || reports != 1 {
		t.Fatalf("retry changed snapshot: %s %s %d %v", firstHash, replayHash, reports, err)
	}
	data = request(c, "GET", reportPath, nil, realtimeMedia, 200)
	var replayReport Report
	if err := json.Unmarshal(data, &replayReport); err != nil || !replayReport.SnapshotAsOf.Equal(firstCutoff) || !replayReport.SourceFreshness[0].EventAt.Equal(*fresh.EventAt) {
		t.Fatal("API reread changed frozen provenance")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, source.LicenseID); err != nil {
		t.Fatal(err)
	}
	// Feedback is a workspace command and does not redisplay revoked source
	// data. It must have one consistent outcome rather than a hidden insert
	// followed by a display-authorization error.
	request(c, "POST", reportPath+"/feedback", map[string]any{"kind": "incorrect", "comment": "synthetic review", "claim_ref": report.Claims[0].Ref}, realtimeMedia, 201)
	var feedbackCount int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM feedback WHERE report_id=$1`, task.ReportRef.ID).Scan(&feedbackCount); err != nil || feedbackCount != 1 {
		t.Fatalf("feedback outcome inconsistent: %d %v", feedbackCount, err)
	}
	for _, path := range []string{reportPath, evidencePath, reportPath + "/pdf"} {
		for _, media := range []string{realtimeMedia, legacyMedia, observationMedia} {
			status, body, _ := c.do("GET", path, nil, apiHeaders(media))
			if status < 400 || strings.HasPrefix(string(body), "%PDF") || strings.Contains(string(body), "10.125") {
				t.Fatalf("revoked source leaked %s (%s): %d %s", path, media, status, body)
			}
		}
	}
}
