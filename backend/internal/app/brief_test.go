//go:build linux && cgo

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// Real native ABI, registry, S3 transport, PostgreSQL, task ledger and model
// gateway; data and model are synthetic here, never production acceptance.
func TestObservationBriefDelivery(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	lib := filepath.Join(t.TempDir(), "brief.so")
	if out, err := exec.Command("cc", "-shared", "-fPIC", "-o", lib, "../dataservice/emquant/testdata/brief.c").CombinedOutput(); err != nil {
		t.Fatalf("compile %s: %v", out, err)
	}
	objects := map[string][]byte{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			if _, ok := objects[r.URL.Path]; ok {
				w.WriteHeader(412)
				return
			}
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case "GET":
			v, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write(v)
		default:
			w.WriteHeader(405)
		}
	}))
	defer srv.Close()
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('brief-test','Synthetic','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	license := func(dataset, method string, fields []string) uuid.UUID {
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing) VALUES('brief-test','synthetic',$1,$2,$3,'listed',ARRAY['600000.SH'],'synthetic','test',now(),now(),true,true,true) RETURNING id`, dataset, method, fields).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	security := license("ref.security", "CSS", []string{"NAME", "LISTDATE", "DELISTDATE"})
	liquidity := license("mkt.daily_bar", "CSD", []string{"VOLUME", "AMOUNT", "TURN"})
	env := map[string]string{"EMQUANT_LIBRARY": lib, "EMQUANT_USER": "user", "EMQUANT_PASSWORD": "pass", "EMQUANT_ACCOUNT_SCOPE": "test", "EMQUANT_S3_ENDPOINT": srv.URL, "EMQUANT_S3_REGION": "test", "EMQUANT_S3_ACCESS_KEY": "synthetic", "EMQUANT_S3_SECRET_KEY": "synthetic", "EMQUANT_S3_BUCKET": "synthetic", "EMQUANT_PROVIDER_ID": "brief-test", "EMQUANT_PRODUCT": "synthetic", "EMQUANT_MIN_INTERVAL": "90ms", "EMQUANT_RATE_COOLDOWN": "1s", "EMQUANT_BRIEF_CODES": "600000.SH", "EMQUANT_SECURITY_LICENSE": security.String(), "EMQUANT_LIQUIDITY_LICENSE": liquidity.String()}
	for k, v := range env {
		t.Setenv(k, v)
	}
	c := &config.Config{DataProvider: "emquant", ContractsDir: "../../../contracts", PolicyPath: "../../../contracts/examples/policy.json", DailyCostLimit: decimal.NewFromInt(30), RealResearch: true, Profiles: map[string]config.ModelProfile{}}
	for _, name := range []string{"writer", "verifier", "router"} {
		c.Profiles[name] = config.ModelProfile{Name: name, Adapter: "sim"}
	}
	a := &App{Cfg: c, Pool: pool}
	if err := a.build(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Brief.Runtime.Close()
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Engine.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}
	var ws, user uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('brief-test','user') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, user).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	sub, err := a.Store.EntityByCode(ctx, "600000.SH")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.Engine.BuildPlan(ctx, ws, "stock_analysis", sub, map[string]any{})
	if err != nil || !plan.Executable {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	id, err := a.Engine.CreateTask(ctx, ws, plan.ID, plan.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.Engine.CreateTask(ctx, ws, plan.ID, plan.Hash, nil)
	if err != nil || again != id {
		t.Fatalf("confirm replay %v", err)
	}
	if err := a.Engine.Run(ctx, ws, id, 1); err != nil {
		t.Fatal(err)
	}
	var status string
	var body []byte
	var cutoff time.Time
	if err := pool.QueryRow(ctx, `SELECT t.status,t.snapshot_as_of,v.document FROM research_tasks t LEFT JOIN report_versions v ON v.task_id=t.id WHERE t.id=$1`, id).Scan(&status, &cutoff, &body); err != nil {
		var reason *string
		_ = pool.QueryRow(ctx, `SELECT reason_message FROM research_tasks WHERE id=$1`, id).Scan(&reason)
		t.Fatalf("delivery %s %v %v", status, reason, err)
	}
	var doc harness.Document
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" || doc.ResultKind != "limited" || !doc.Synthetic || len(doc.Claims) == 0 || cutoff.Before(plan.AsOf) || !strings.Contains(doc.DataAsOf, "非实时") {
		t.Fatalf("invalid brief: %s %+v", status, doc)
	}
	if err := a.Engine.Run(ctx, ws, id, 1); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM report_versions WHERE task_id=$1`, id).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate delivery")
	}
	if err := a.Brief.WithSnapshot(ctx, uuid.New(), id, dataservice.SourceDisplay, func(emquant.BriefObservation) error { t.Fatal("cross-workspace read"); return nil }); err == nil {
		t.Fatal("cross-workspace accepted")
	}

	// Exercise authenticated HTTP representations and cached responses against
	// the same delivered native-ABI brief, without bypassing session middleware.
	token := identity.Random(32)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,workspace_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 hour')`, identity.Hash(token), user, ws, identity.Hash(identity.CSRFToken(token))); err != nil {
		t.Fatal(err)
	}
	handler, err := a.HTTP()
	if err != nil {
		t.Fatal(err)
	}
	const media = "application/vnd.vera.observation.v1+json"
	request := func(method, path, body, accept, key string) *httptest.ResponseRecorder {
		t.Helper()
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(requestCtx)
		req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		req.Header.Set("X-CSRF-Token", identity.CSRFToken(token))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	planBody := fmt.Sprintf(`{"skill_id":"stock_analysis","subject_id":%q,"params":{}}`, sub.ID)
	if rec := request("POST", "/api/v1/research-plans", planBody, "application/json", uuid.NewString()); rec.Code != 406 {
		t.Fatalf("legacy plan accepted: %d %s", rec.Code, rec.Body)
	}
	key := uuid.NewString()
	if rec := request("POST", "/api/v1/research-plans", planBody, media, key); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), media) {
		t.Fatalf("observation plan: %d %s", rec.Code, rec.Body)
	}
	taskBody := fmt.Sprintf(`{"plan_id":%q,"plan_hash":%q}`, plan.ID, plan.Hash)
	if rec := request("POST", "/api/v1/research-tasks", taskBody, media, key); rec.Code != 202 {
		t.Fatalf("task confirm: %d %s", rec.Code, rec.Body)
	}
	if rec := request("POST", "/api/v1/research-tasks", taskBody, media, key); rec.Code != 202 || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("task replay: %d %s", rec.Code, rec.Body)
	}
	var observation emquant.BriefObservation
	if err := a.Brief.WithSnapshot(ctx, ws, id, dataservice.SourceDisplay, func(o emquant.BriefObservation) error { observation = o; return nil }); err != nil {
		t.Fatal(err)
	}
	reportPath := "/api/v1/reports/" + doc.ID.String() + "/versions/1"
	evidencePath := "/api/v1/evidence/" + observation.Manifest.Liquidity.ID.String()
	for _, path := range []string{reportPath, evidencePath, "/api/v1/reports", "/api/v1/research-tasks", "/api/v1/entities?q=600000"} {
		if rec := request("GET", path, "", media, ""); rec.Code != 200 {
			t.Fatalf("read %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	// Concurrent readers must not retain a connection while waiting for
	// another database operation; the API's final authorization only flushes.
	var readers sync.WaitGroup
	for range 12 {
		readers.Go(func() {
			if rec := request("GET", reportPath, "", media, ""); rec.Code != 200 {
				t.Errorf("concurrent read: %d", rec.Code)
			}
		})
	}
	readers.Wait()
	if rec := request("GET", evidencePath, "", media, ""); !strings.Contains(rec.Body.String(), "换手率") || !strings.Contains(rec.Body.String(), "Values") {
		t.Fatal("missing turnover provenance")
	}
	if rec := request("GET", reportPath+"/pdf", "", "application/pdf", ""); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "%PDF") {
		t.Fatalf("pdf: %d %s", rec.Code, rec.Body)
	}
	// Object digest verification must still reject tampering after delivery.
	mu.Lock()
	saved := map[string][]byte{}
	for k, v := range objects {
		saved[k] = v
		objects[k] = []byte("corrupted archive")
	}
	mu.Unlock()
	if rec := request("GET", reportPath, "", media, ""); rec.Code < 400 {
		t.Fatal("corrupt archive displayed")
	}
	mu.Lock()
	for k, v := range saved {
		objects[k] = v
	}
	mu.Unlock()
	// Configured scope must hide cached identities from all search mechanisms.
	a.Store.RestrictCodes([]string{"600001.SH"})
	if found, err := a.Store.Search(ctx, "600000", "stock", 20); err != nil || len(found) != 0 {
		t.Fatalf("scope search: %v %v", found, err)
	}
	if found, err := a.Store.Mentioned(ctx, sub.Name); err != nil || len(found) != 0 {
		t.Fatalf("scope mention: %v %v", found, err)
	}
	if rec := request("GET", "/api/v1/research-tasks/"+id.String(), "", media, ""); rec.Code != 404 {
		t.Fatalf("scope task: %d", rec.Code)
	}
	a.Store.RestrictCodes(a.Brief.Codes)
	cancelPlan, err := a.Engine.BuildPlan(ctx, ws, "stock_analysis", sub, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	cancelID, err := a.Engine.CreateTask(ctx, ws, cancelPlan.ID, cancelPlan.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM research_tasks WHERE id=$1`, cancelID).Scan(&revision); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, liquidity); err != nil {
		t.Fatal(err)
	}
	if err := a.Brief.WithSnapshot(ctx, ws, id, dataservice.SourceDisplay, func(emquant.BriefObservation) error { t.Fatal("revoked read"); return nil }); err == nil {
		t.Fatal("revocation bypass")
	}

	for _, path := range []string{reportPath, reportPath + "/pdf", evidencePath, "/api/v1/reports", "/api/v1/research-tasks", "/api/v1/entities?q=600000"} {
		if rec := request("GET", path, "", media, ""); rec.Code < 400 || strings.Contains(rec.Body.String(), sub.Name) {
			t.Fatalf("revoked display: %s %d", path, rec.Code)
		}
	}
	if rec := request("POST", "/api/v1/research-tasks", taskBody, media, key); rec.Code < 400 {
		t.Fatal("revoked idempotent plan displayed")
	}
	if rec := request("POST", "/api/v1/research-tasks/"+cancelID.String()+"/cancel", fmt.Sprintf(`{"expected_revision":%d}`, revision), media, "cancel-retry"); rec.Code != 204 {
		t.Fatalf("revoked permission blocked cancel: %d %s", rec.Code, rec.Body)
	}
	if rec := request("POST", "/api/v1/research-tasks/"+cancelID.String()+"/cancel", fmt.Sprintf(`{"expected_revision":%d}`, revision), media, "cancel-retry"); rec.Code != 204 || rec.Body.Len() != 0 || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("cancel replay: %d %s", rec.Code, rec.Body)
	}
	if err := a.Brief.Freeze(ctx, ws, cancelID, 1, observation.Manifest); err == nil {
		t.Fatal("cancelled task frozen")
	}
	// An operator cannot expose saved vendor names by switching this database
	// back to fixture configuration during rollback.
	fixtureCfg := *c
	fixtureCfg.DataProvider = "fixture"
	if err := (&App{Cfg: &fixtureCfg, Pool: pool}).build(ctx); err == nil {
		t.Fatal("fixture rollback accepted real database")
	}

}
