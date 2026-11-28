//go:build linux && cgo

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation/conversationtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/httpapi"
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
	env := map[string]string{"EMQUANT_LIBRARY": lib, "EMQUANT_USER": "user", "EMQUANT_PASSWORD": "pass", "EMQUANT_ACCOUNT_SCOPE": "test", "EMQUANT_S3_ENDPOINT": srv.URL, "EMQUANT_S3_REGION": "test", "EMQUANT_S3_ACCESS_KEY": "synthetic", "EMQUANT_S3_SECRET_KEY": "synthetic", "EMQUANT_S3_BUCKET": "synthetic", "EMQUANT_PROVIDER_ID": "brief-test", "EMQUANT_PRODUCT": "synthetic", "EMQUANT_MIN_INTERVAL": "90ms", "EMQUANT_RATE_COOLDOWN": "1s"}
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
	sub, err := a.Store.UpsertSearchEntity(ctx, "stock", "600000.SH", "Synthetic Test Stock", nil)
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
	schemaPath, err := filepath.Abs("../../../contracts/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	schemaBody, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(schemaBody)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(schemaPath, schemaDoc); err != nil {
		t.Fatal(err)
	}
	checkResponse := func(name string, body []byte) {
		t.Helper()
		schema, err := compiler.Compile(schemaPath + "#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s response contract: %v", name, err)
		}
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
	checkResponse("PlanResult", request("POST", "/api/v1/research-plans", planBody, media, "").Body.Bytes())
	checkResponse("Task", request("GET", "/api/v1/research-tasks/"+id.String(), "", media, "").Body.Bytes())
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
	reportBody := request("GET", reportPath, "", media, "").Body.Bytes()
	checkResponse("Report", reportBody)
	var displayed harness.Document
	if err := json.Unmarshal(reportBody, &displayed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sec := range displayed.Sections {
		for _, table := range sec.Tables {
			if table.Title != "Choice 日成交观察（非实时）" {
				continue
			}
			found = true
			if !slices.Equal(table.Columns, []string{"厂商返回日期", "成交量（手）", "成交额（万元）", "换手率（%）"}) || len(table.Rows) != 1 {
				t.Fatalf("unexpected daily table: %+v", table)
			}
			row := table.Rows[0]
			if len(row) != 4 || row[1] == nil || *row[1] != "1200.00" || row[2] == nil || *row[2] != "180.00" || row[3] == nil || *row[3] != "1.25" {
				t.Fatalf("unexpected displayed row: %s", reportBody)
			}
		}
	}
	if !found {
		t.Fatal("missing daily observation table")
	}
	evidenceBody := request("GET", evidencePath, "", media, "").Body.Bytes()
	checkResponse("Evidence", evidenceBody)
	var evidence httpapi.Evidence
	if err := json.Unmarshal(evidenceBody, &evidence); err != nil {
		t.Fatal(err)
	}
	wantExcerpt := observation.Rows[0].TradeDate + " 成交量 1200.00 手，成交额 180.00 万元，换手率 1.25%"
	if evidence.Excerpt == nil || *evidence.Excerpt != wantExcerpt {
		t.Fatalf("unexpected displayed evidence: %s", evidenceBody)
	}
	if evidence.ContentHash != observation.Manifest.Liquidity.Source.BodySHA256 || evidence.Locator == nil || !strings.Contains(*evidence.Locator, "$.data.Values") {
		t.Fatal("missing archive provenance")
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
	a.Store.RestrictCodes([]string{observation.Manifest.Code})
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

	for _, path := range []string{reportPath, reportPath + "/pdf", evidencePath, "/api/v1/reports", "/api/v1/research-tasks", "/api/v1/entities?q=600000"} {
		if rec := request("GET", path, "", media, ""); rec.Code >= 400 {
			t.Fatalf("display blocked unexpectedly: %s %d", path, rec.Code)
		}
	}
	if rec := request("POST", "/api/v1/research-tasks", taskBody, media, key); rec.Code >= 400 {
		t.Fatal("idempotent plan unexpectedly rejected")
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
	t.Run("conversation_chart_and_retention", func(t *testing.T) {
		checkChoiceConversation(t, a, id, doc.ID, reportPath, request, checkResponse)
	})
	// An operator cannot expose saved vendor names by switching this database
	// back to fixture configuration during rollback.
	fixtureCfg := *c
	fixtureCfg.DataProvider = "fixture"
	if err := (&App{Cfg: &fixtureCfg, Pool: pool}).build(ctx); err == nil {
		t.Fatal("fixture rollback accepted real database")
	}
	t.Run("mixed closing snapshot", func(t *testing.T) { checkMixedObservation(t, a, ws, sub, id, token) })
}

type choiceRetentionProbe struct{}

func (choiceRetentionProbe) Kind() string { return "conversation.retention" }

func checkChoiceConversation(t *testing.T, a *App, formalTask, formalReport uuid.UUID, formalPath string,
	request func(method, path, body, accept, key string) *httptest.ResponseRecorder, checkResponse func(string, []byte)) {
	t.Helper()
	ctx := context.Background()
	conversationtest.UseRouter(a.Engine.Gateway, map[string]model.ToolArguments{
		"600000.SH 的日成交": {Name: "inspect_daily_trading",
			Arguments: `{"subject_mentions":["600000.SH"],"subject_source":"message","trading_days":0,"advice":"none"}`},
	})
	worker, err := a.Engine.NewRiver(a.Pool, true, 1, conversation.Register(a.Engine))
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := worker.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := request(method, path, body, "application/json", "")
		if rec.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, want, rec.Body)
		}
		return rec
	}
	accepted := call("POST", "/api/v1/conversations", fmt.Sprintf(`{"client_message_id":%q,"text":"600000.SH 的日成交","automatic":true}`, uuid.NewString()), 202)
	var ids conversation.Accepted
	if err := json.Unmarshal(accepted.Body.Bytes(), &ids); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/conversations/" + ids.ConversationID.String()
	turnPath := path + "/turns/" + ids.TurnID.String()
	if accepted.Header().Get("Location") != turnPath {
		t.Fatalf("turn location: %s", accepted.Header().Get("Location"))
	}
	type turn struct {
		State       string `json:"state"`
		Revision    int64  `json:"revision"`
		TaskStatus  string `json:"task_status"`
		ResultReady bool   `json:"result_ready"`
	}
	awaitTurn := func(ready func(turn) bool) turn {
		t.Helper()
		var latest turn
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			latest = turn{}
			if err := json.Unmarshal(call("GET", turnPath, "", 200).Body.Bytes(), &latest); err != nil {
				t.Fatal(err)
			}
			if ready(latest) {
				return latest
			}
		}
		t.Fatalf("last Choice turn: %+v", latest)
		return turn{}
	}
	completed := awaitTurn(func(v turn) bool { return harness.IsTerminal(v.TaskStatus) })
	if completed.State != "research" || !completed.ResultReady {
		t.Fatalf("Choice result: %+v", completed)
	}
	content := call("GET", turnPath+"/result", "", 200).Body.Bytes()
	checkResponse("ConversationResult", content)
	var result struct {
		ReportID uuid.UUID      `json:"report_id"`
		Tables   []facts.Table  `json:"tables"`
		Series   []facts.Series `json:"series"`
	}
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tables) != 1 || len(result.Series) != 1 || len(result.Series[0].Evidence) != 1 || len(result.Series[0].Lines) != 1 {
		t.Fatalf("missing table, chart or evidence: %s", content)
	}
	table, chart := result.Tables[0], result.Series[0]
	points := chart.Lines[0].Points
	if len(table.Rows) != 1 || len(table.Rows[0]) < 3 || table.Rows[0][2] == nil || *table.Rows[0][2] != "180.00" ||
		chart.Unit != "万元" || len(points) != 1 || points[0][1] == nil || *points[0][1] != "180.00" ||
		!slices.Equal(chart.Evidence, table.Evidence) {
		t.Fatalf("Choice table and chart differ: %s", content)
	}
	evidence := call("GET", "/api/v1/evidence/"+chart.Evidence[0].String(), "", 200)
	if !strings.Contains(evidence.Body.String(), "180.00 万元") {
		t.Fatalf("Choice evidence differs: %s", evidence.Body.String())
	}
	var shortTask uuid.UUID
	var shortVersion string
	if err := a.Pool.QueryRow(ctx, "SELECT task_id, r.skill_version FROM conversation_turns t JOIN research_tasks r ON r.id=t.task_id WHERE t.id=$1", ids.TurnID).Scan(&shortTask, &shortVersion); err != nil || shortVersion != emquant.ConversationObservationVersion {
		t.Fatalf("unexpected Choice version %s: %v", shortVersion, err)
	}
	const deleteSnapshot = `DELETE FROM observation_brief_snapshots WHERE task_id=$1`
	if _, err := a.Pool.Exec(ctx, deleteSnapshot, shortTask); err == nil {
		t.Fatal("visible Choice snapshot could be deleted")
	}
	if _, err := a.Pool.Exec(ctx, "UPDATE observation_brief_snapshots SET manifest=manifest WHERE task_id=$1", shortTask); err == nil {
		t.Fatal("Choice snapshot could be modified")
	}
	if list := request("GET", "/api/v1/reports", "", "application/vnd.vera.observation.v1+json", ""); list.Code != 200 ||
		!strings.Contains(list.Body.String(), formalReport.String()) || strings.Contains(list.Body.String(), result.ReportID.String()) {
		t.Fatalf("formal history: %d %s", list.Code, list.Body)
	}
	call("DELETE", path, "", 204)
	call("GET", turnPath+"/result", "", 404)
	if _, err := a.Pool.Exec(ctx, `UPDATE conversation_turns SET created_at=now()-interval '2 minutes' WHERE conversation_id=$1`, ids.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Insert(ctx, choiceRetentionProbe{}, nil); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(8 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var retained bool
		if err := a.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM conversations WHERE id=$1)", ids.ConversationID).Scan(&retained); err != nil {
			t.Fatal(err)
		}
		if !retained {
			break
		}
		if time.Now().After(end) {
			t.Fatal("Choice conversation was not purged")
		}
	}
	var shortRetained, formalRetained bool
	if err := a.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM observation_brief_snapshots WHERE task_id=$1),
		EXISTS (SELECT 1 FROM observation_brief_snapshots WHERE task_id=$2)`, shortTask, formalTask).Scan(&shortRetained, &formalRetained); err != nil || shortRetained || !formalRetained {
		t.Fatalf("retention short=%t formal=%t: %v", shortRetained, formalRetained, err)
	}
	if _, err := a.Pool.Exec(ctx, deleteSnapshot, formalTask); err == nil {
		t.Fatal("ordinary Choice snapshot could be deleted")
	}
	if pdf := request("GET", formalPath+"/pdf", "", "application/pdf", ""); pdf.Code != 200 || !strings.HasPrefix(pdf.Body.String(), "%PDF") {
		t.Fatalf("retained formal PDF: %d", pdf.Code)
	}
}

func checkMixedObservation(t *testing.T, a *App, ws uuid.UUID, subject dataservice.EntityRow, historical uuid.UUID, token string) {
	ctx := t.Context()
	store := dataservice.NewResearchStore(a.Pool, a.Store.Catalog(), "tushare", []string{subject.Code}, true)
	e := *a.Engine
	e.Store, e.ChoiceCatalog, e.Skills = store, a.Store, harness.TuShareSkills()
	e.WithCloseResearchUse, e.CloseResearchCutoff = store.WithResearchUse, store.PublicationCutoff
	if _, err := a.Pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('tushare','TuShare Test','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	put := func(entity uuid.UUID, period, price string) uuid.UUID {
		t.Helper()
		rec := dataservice.Record{DatasetID: "mkt.daily_bar", RecordKey: entity.String() + period, Payload: map[string]any{"trade_date": period, "close": price}}
		var id uuid.UUID
		err := a.Pool.QueryRow(ctx, `INSERT INTO records(dataset_id,entity_id,record_key,logical_key,period,available_at,provider_id,origin,source_tier,payload,content_hash,raw_ref,raw_sha256,raw_locator,mapping_version)
			VALUES('mkt.daily_bar',$1,$2,$2,$3,clock_timestamp(),'tushare','exchange','L2',$4,$5,'synthetic-object',repeat('1',64),'row=1',1) RETURNING id`, entity, rec.RecordKey, period, rec.Payload, rec.Hash()).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	other, err := a.Store.UpsertSearchEntity(ctx, "stock", "600001.SH", "Synthetic Different Stock", nil)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().AddDate(0, 0, -3)
	day0, day1, day2 := day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly), day.AddDate(0, 0, 2).Format(time.DateOnly)
	put(other.ID, day2, "99.99")
	classic, err := e.BuildObservationPlan(ctx, ws, "stock_analysis", subject, nil)
	if err != nil || classic.SkillVersion != emquant.ObservationBriefVersion {
		t.Fatalf("missing current subject did not yield a Choice plan: %+v %v", classic, err)
	}
	bar := put(subject.ID, day0, "12.34")
	plan, err := e.BuildObservationPlan(ctx, ws, "stock_analysis", subject, nil)
	if err != nil || plan.SkillVersion != harness.MixedObservationVersion || !plan.Executable {
		t.Fatalf("mixed plan: %+v %v", plan, err)
	}
	put(subject.ID, day1, "56.78")
	task, err := e.CreateTask(ctx, ws, plan.ID, plan.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Run(ctx, ws, task, 1); err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err := a.Pool.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, task).Scan(&body); err != nil {
		var status string
		var reason *string
		_ = a.Pool.QueryRow(ctx, `SELECT status,reason_message FROM research_tasks WHERE id=$1`, task).Scan(&status, &reason)
		t.Fatalf("mixed delivery %s: %v / %v", status, reason, err)
	}
	var doc harness.Document
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.AsOf.Equal(plan.AsOf) || doc.ResultKind != "limited" || len(doc.Sources) != 2 || len(doc.SourceFreshness) != 2 || doc.SourceFreshness[1].Provider != "tushare" || *doc.SourceFreshness[1].Period != day0 {
		t.Fatalf("mixed source freshness: %+v %+v", doc.Sources, doc.SourceFreshness)
	}
	put(subject.ID, day2, "78.90")
	read := func() error {
		return e.WithMixedDisplay(ctx, ws, task, func(o emquant.BriefObservation, records []dataservice.StoredRecord) error {
			if len(records) != 1 || records[0].ID != bar || records[0].Payload["close"] != "12.34" || len(o.Manifest.Supplement) != 1 {
				t.Fatal("frozen row changed")
			}
			proofs := map[uuid.UUID]bool{}
			for _, c := range doc.Claims {
				for _, id := range c.EvidenceRefs {
					proofs[id] = true
				}
			}
			if !proofs[bar] || !proofs[o.Manifest.Liquidity.ID] {
				t.Fatalf("both vendors must contribute claims: %+v", doc.Claims)
			}
			return nil
		})
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	if err := a.Brief.WithSnapshot(ctx, ws, task, dataservice.SourceDisplay, func(emquant.BriefObservation) error { return nil }); err == nil {
		t.Fatal("mixed snapshot read as single-source")
	}
	if err := e.WithMixedDisplay(ctx, ws, historical, func(emquant.BriefObservation, []dataservice.StoredRecord) error { return nil }); err == nil {
		t.Fatal("historical snapshot read as mixed")
	}
	if err := e.Run(ctx, ws, task, 1); err != nil {
		t.Fatal(err)
	}
	checkMixedHTTP(t, a, &e, store, subject, historical, doc, bar, token)
	deniedRead := func(emquant.BriefObservation, []dataservice.StoredRecord) error {
		t.Fatal("unauthorized mixed snapshot read")
		return nil
	}
	if err := e.WithMixedDisplay(ctx, uuid.New(), task, deniedRead); err == nil {
		t.Fatal("cross-workspace read")
	}
	next, err := e.BuildObservationPlan(ctx, ws, "stock_analysis", subject, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := e.CreateTask(ctx, ws, next.ID, next.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rev int64
	if err := a.Pool.QueryRow(ctx, `SELECT revision FROM research_tasks WHERE id=$1`, cancelled).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if _, status, err := e.CancelTask(ctx, ws, cancelled, rev); err != nil || status != "CANCELLED" {
		t.Fatalf("cancelled mixed task: %s %v", status, err)
	}
	if err := e.Run(ctx, ws, cancelled, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.WithMixedDisplay(ctx, ws, cancelled, deniedRead); err == nil {
		t.Fatal("cancelled mixed snapshot available")
	}
	for _, provider := range []string{"tushare", a.Brief.ProviderID} {
		if _, err := a.Pool.Exec(ctx, `UPDATE providers SET enabled=false WHERE id=$1`, provider); err != nil {
			t.Fatal(err)
		}
		if err := read(); err == nil {
			t.Fatalf("display after revocation of %s", provider)
		}
		if err := e.WithMixedResearchUse(ctx, func() error { t.Fatal("metadata after revocation"); return nil }); err == nil {
			t.Fatalf("model use after revocation of %s", provider)
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE providers SET enabled=true WHERE id=$1`, provider); err != nil {
			t.Fatal(err)
		}
	}
}
