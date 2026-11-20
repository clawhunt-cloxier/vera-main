package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/httpapi"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

// A synthetic upstream responds on the official HTTP protocol. All ingestion,
// document extraction, persistence, authenticated API, worker and PDF code is real.
func TestTuShareThreeReportDelivery(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	objects := map[string][]byte{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "PUT" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if !strings.HasSuffix(r.URL.Path, fmt.Sprintf("%x", sha256.Sum256(body))) {
				t.Error("bad archive object digest")
			}
			if bytes.Contains(body, []byte("synthetic-token-for-tushare-tests")) {
				t.Error("archived token")
			}
			if _, ok := objects[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(412)
				_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			objects[r.URL.Path] = body
			return
		}
		body, ok := objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	for key, value := range map[string]string{"TUSHARE_TOKEN": "synthetic-token-for-tushare-tests", "TUSHARE_SUBJECTS": "600519.SH", "TUSHARE_S3_ENDPOINT": srv.URL, "TUSHARE_S3_REGION": "test", "TUSHARE_S3_ACCESS_KEY": "synthetic", "TUSHARE_S3_SECRET_KEY": "synthetic", "TUSHARE_S3_BUCKET": "archive", "TUSHARE_MIN_INTERVAL": "100ms", "TUSHARE_ALLOW_MODEL_PROCESSING": "true"} {
		t.Setenv(key, value)
	}
	previous := http.DefaultTransport
	upstream := newTuShareUpstream(t, time.Now())
	http.DefaultTransport = tuShareTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme == "http" && "http://"+r.URL.Host == srv.URL {
			return previous.RoundTrip(r)
		}
		return upstream.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	c := &config.Config{DataProvider: "tushare", ContractsDir: "../../../contracts", PolicyPath: "../../../contracts/examples/policy.json", DailyCostLimit: decimal.NewFromInt(30), RealResearch: true, Profiles: map[string]config.ModelProfile{}}
	for _, id := range []string{"writer", "verifier", "router"} {
		c.Profiles[id] = config.ModelProfile{Name: id, Adapter: "sim"}
	}
	a := &App{Cfg: c, Pool: pool}
	if err := a.build(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.TuShare.Probe(ctx); err != nil || len(objects) != 0 {
		t.Fatalf("read-only account probe: archived objects=%d, err=%v", len(objects), err)
	}
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var initial, after int
	count := func(out *int) {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE provider_id='tushare'`).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	count(&initial)
	if _, err := a.Engine.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}
	var ws, user uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('synthetic-tu','reader') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, user).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	session := identity.Random(32)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,workspace_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 hour')`, identity.Hash(session), user, ws, identity.Hash(identity.CSRFToken(session))); err != nil {
		t.Fatal(err)
	}
	handler, err := a.HTTP()
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: session})
		req.Header.Set("X-CSRF-Token", identity.CSRFToken(session))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s = %d: %.600s", method, path, rec.Code, rec.Body.Bytes())
		}
		return rec.Body.Bytes()
	}
	decode := func(body []byte, out any) {
		t.Helper()
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatal(err)
		}
	}
	var catalog struct{ Items []httpapi.Entity }
	decode(request("GET", "/api/v1/entities?q=600519", "", 200), &catalog)
	if len(catalog.Items) != 1 {
		t.Fatalf("search: %+v", catalog)
	}
	var lastReport, lastEvidence, lastCalc string
	var lastTask uuid.UUID
	var calculationSeen bool
	for _, skill := range []string{"stock_analysis", "capital_flow", "deep_research"} {
		params := "{}"
		if skill == "deep_research" {
			params = `{"mode":"company"}`
		}
		var plan httpapi.PlanResult
		decode(request("POST", "/api/v1/research-plans", fmt.Sprintf(`{"subject_id":%q,"skill_id":%q,"params":%s}`, catalog.Items[0].ID, skill, params), 200), &plan)
		if plan.Plan == nil || !plan.Plan.Executable {
			t.Fatalf("%s: %+v", skill, plan.Plan)
		}
		var task httpapi.Task
		decode(request("POST", "/api/v1/research-tasks", fmt.Sprintf(`{"plan_id":%q,"plan_hash":%q}`, plan.Plan.ID, plan.Plan.PlanHash), 202), &task)
		if err := a.Engine.Run(ctx, ws, task.ID, 1); err != nil {
			t.Fatal(err)
		}
		decode(request("GET", "/api/v1/research-tasks/"+task.ID.String(), "", 200), &task)
		if task.ReportRef == nil {
			t.Fatalf("%s %s: %v %v", skill, task.Status, task.ReasonCode, task.ReasonMessage)
		}
		lastReport = fmt.Sprintf("/api/v1/reports/%s/versions/%d", task.ReportRef.ID, task.ReportRef.Version)
		lastTask = task.ID
		var report httpapi.Report
		decode(request("GET", lastReport, "", 200), &report)
		expectedVersion := harness.CloseUnifiedVersion
		if plan.Plan.SkillVersion != expectedVersion || report.SkillVersion != expectedVersion {
			t.Fatalf("%s plan=%s report=%s want %s", skill, plan.Plan.SkillVersion, report.SkillVersion, expectedVersion)
		}
		want := "limited"
		if skill == "stock_analysis" {
			want = "full"
		}
		if report.ResultKind != want || len(report.Claims) < 8 || len(report.SummaryRefs) < 3 || !report.Synthetic {
			t.Fatalf("%s %s: claims=%d summary=%d limitations=%v sections=%+v", skill, report.ResultKind, len(report.Claims), len(report.SummaryRefs), report.Limitations, report.Sections)
		}
		var evidence uuid.UUID
		for _, claim := range report.Claims {
			if len(claim.EvidenceRefs)+len(claim.CalcRefs) == 0 {
				t.Fatalf("unbound claim %s", claim.Ref)
			}
			if len(claim.EvidenceRefs) > 0 {
				evidence = claim.EvidenceRefs[0]
			}
			if !calculationSeen && len(claim.CalcRefs) > 0 {
				var calc httpapi.Calculation
				lastCalc = "/api/v1/calculations/" + claim.CalcRefs[0].String()
				decode(request("GET", lastCalc, "", 200), &calc)
				calculationSeen = len(calc.Inputs) > 0
			}
		}
		var ev httpapi.Evidence
		lastEvidence = "/api/v1/evidence/" + evidence.String()
		decode(request("GET", lastEvidence, "", 200), &ev)
		if ev.Provider != "TuShare Pro" || ev.ContentHash == "" {
			t.Fatalf("missing TuShare evidence: %+v", ev)
		}
		charts := 0
		for _, section := range report.Sections {
			// A real reader can resolve the newly supported disclosures directly
			// from their section tables, even if the writer picked other facts.
			var requiredDatasets []string
			switch {
			case skill == "capital_flow" && section.ID == "S4", skill == "stock_analysis" && section.ID == "S7":
				requiredDatasets = []string{"flow.dragon_tiger_day", "flow.dragon_tiger"}
			case skill == "capital_flow" && section.ID == "S5":
				requiredDatasets = []string{"fund.disclosed_position"}
			case skill == "stock_analysis" && section.ID == "S8":
				requiredDatasets = []string{"corp.dividend"}
			}
			if len(requiredDatasets) > 0 {
				seen := map[string]bool{}
				for _, table := range section.Tables {
					if len(table.Rows) == 0 || len(table.Evidence) == 0 {
						continue
					}
					var row httpapi.Evidence
					decode(request("GET", "/api/v1/evidence/"+table.Evidence[0].String(), "", 200), &row)
					if row.Provider == "TuShare Pro" && row.ContentHash != "" && row.Locator != nil && *row.Locator != "" {
						seen[row.DatasetID] = true
					}
				}
				for _, dataset := range requiredDatasets {
					if !seen[dataset] {
						t.Fatalf("%s %s has no reachable table evidence for %s", skill, section.ID, dataset)
					}
				}
			}
			for _, chart := range section.Charts {
				series := request("GET", lastReport+"/series/"+chart.SeriesID, "", 200)
				if !bytes.Contains(series, []byte(`"points"`)) {
					t.Fatal("missing chart points")
				}
				charts++
			}
		}
		if charts == 0 {
			t.Fatalf("%s missing charts", skill)
		}
		pdf := request("GET", lastReport+"/pdf", "", 200)
		if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("%%EOF")) {
			t.Fatal("invalid PDF")
		}
		if dir := os.Getenv("VERA_TUSHARE_TEST_PDF_DIR"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, skill+".pdf"), pdf, 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s: %s, %d claims, %d charts, %d PDF bytes", skill, report.ResultKind, len(report.Claims), charts, len(pdf))
	}
	if !calculationSeen {
		t.Fatal("no reachable calculation inputs")
	}
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	count(&after)
	if initial != after {
		t.Fatalf("repeat sync created revisions: %d -> %d", initial, after)
	}
	request("GET", lastReport, "", 200)
	// A worker replay uses the same persisted snapshot and report version.
	if err := a.Engine.Run(ctx, ws, lastTask, 1); err != nil {
		t.Fatal(err)
	}
	request("GET", lastReport, "", 200)
	// Test both the local deployment ceiling and an independently revoked database grant.
	t.Setenv("TUSHARE_ALLOW_MODEL_PROCESSING", "false")
	restricted := &App{Cfg: c, Pool: pool}
	if err := restricted.build(ctx); err != nil {
		t.Fatal(err)
	}
	original := handler
	handler, err = restricted.HTTP()
	if err != nil {
		t.Fatal(err)
	}
	request("GET", lastReport+"/pdf", "", 503)
	handler = original
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='tushare'`); err != nil {
		t.Fatal(err)
	}
	request("GET", lastEvidence, "", 503)
	request("GET", lastCalc, "", 503)
	request("GET", lastReport+"/pdf", "", 503)
}

type tuShareTestTransport func(*http.Request) (*http.Response, error)

func (f tuShareTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
