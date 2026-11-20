package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/httpapi"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

// The upstream and model are synthetic. Ingestion, authenticated report
// requests, evidence, calculations, persistence and PDF rendering are real.
func TestUnifiedCloseReportsWithoutAnnouncements(t *testing.T) {
	testUnifiedCloseReportsWithoutAnnouncements(t, "")
}

func testUnifiedCloseReportsWithoutAnnouncements(t *testing.T, choiceLibrary string) {
	ctx := context.Background()
	pool := dbtest.New(t)
	var archive sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if _, exists := archive.LoadOrStore(r.URL.Path, body); exists {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
			}
		case http.MethodGet:
			body, ok := archive.Load(r.URL.Path)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body.([]byte))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	for key, value := range map[string]string{
		"TUSHARE_TOKEN": "synthetic-tushare-token", "TUSHARE_SUBJECTS": tsTestSubject, "TUSHARE_S3_ENDPOINT": srv.URL,
		"TUSHARE_S3_REGION": "test", "TUSHARE_S3_ACCESS_KEY": "synthetic", "TUSHARE_S3_SECRET_KEY": "synthetic",
		"TUSHARE_S3_BUCKET": "archive", "TUSHARE_MIN_INTERVAL": "100ms", "TUSHARE_ALLOW_MODEL_PROCESSING": "true",
	} {
		t.Setenv(key, value)
	}
	previous := http.DefaultTransport
	upstream := newTuShareUpstream(t, time.Now())
	var denied atomic.Int32
	http.DefaultTransport = tuShareTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme == "http" && "http://"+r.URL.Host == srv.URL {
			return previous.RoundTrip(r)
		}
		if r.URL.Host == "api.tushare.pro" {
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				return nil, err
			}
			var query struct {
				API string `json:"api_name"`
			}
			if err := json.Unmarshal(body, &query); err != nil {
				return nil, err
			}
			if query.API == "anns_d" {
				denied.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"code":40203,"msg":"synthetic permission denial","data":null}`)), Request: r,
				}, nil
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if r.URL.Host == "static.cninfo.com.cn" {
			t.Error("must not attempt to retrieve an unindexed disclosure")
		}
		return upstream.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = previous })

	cfg := &config.Config{DataProvider: "tushare", ContractsDir: "../../../contracts", PolicyPath: "../../../contracts/examples/policy.json",
		DailyCostLimit: decimal.NewFromInt(30), RealResearch: true, Profiles: map[string]config.ModelProfile{}}
	const choiceProvider = "choice-supplement-e2e"
	if choiceLibrary != "" {
		cfg.DataProvider = "emquant+tushare"
		for key, value := range map[string]string{
			"EMQUANT_LIBRARY": choiceLibrary, "EMQUANT_USER": "user", "EMQUANT_PASSWORD": "pass", "EMQUANT_ACCOUNT_SCOPE": "synthetic-combined",
			"EMQUANT_S3_ENDPOINT": srv.URL, "EMQUANT_S3_REGION": "test", "EMQUANT_S3_ACCESS_KEY": "synthetic", "EMQUANT_S3_SECRET_KEY": "synthetic",
			"EMQUANT_S3_BUCKET": "archive", "EMQUANT_PROVIDER_ID": choiceProvider, "EMQUANT_PRODUCT": "synthetic", "EMQUANT_MIN_INTERVAL": "90ms", "EMQUANT_RATE_COOLDOWN": "1s",
		} {
			t.Setenv(key, value)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES($1,'Synthetic Choice','vendor',true)`, choiceProvider); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"writer", "verifier", "router"} {
		cfg.Profiles[name] = config.ModelProfile{Name: name, Adapter: "sim"}
	}
	a := &App{Cfg: cfg, Pool: pool}
	if err := a.build(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Brief != nil {
		t.Cleanup(func() { _ = a.Brief.Runtime.Close() })
	}
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if denied.Load() == 0 {
		t.Fatal("test did not exercise an announcement permission denial")
	}
	if _, err := a.Engine.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}
	newSession := func(t *testing.T, subject string) (workspace uuid.UUID, session, csrf string) {
		t.Helper()
		var user uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('synthetic-tu-no-announcements',$1) RETURNING id`, subject).Scan(&user); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, user).Scan(&workspace); err != nil {
			t.Fatal(err)
		}
		session = identity.Random(32)
		csrf = identity.CSRFToken(session)
		if _, err := pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,workspace_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 hour')`, identity.Hash(session), user, workspace, identity.Hash(csrf)); err != nil {
			t.Fatal(err)
		}
		return workspace, session, csrf
	}
	ws, session, csrf := newSession(t, "reader")
	handler, err := a.HTTP()
	if err != nil {
		t.Fatal(err)
	}
	requestAs := func(t *testing.T, session, csrf, method, path, body string, status int, out any, accept ...string) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: session})
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Content-Type", "application/json")
		for _, media := range accept {
			req.Header.Add("Accept", media)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s = %d: %.600s", method, path, rec.Code, rec.Body.Bytes())
		}
		if out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Body.Bytes()
	}
	request := func(t *testing.T, method, path, body string, status int, out any) []byte {
		t.Helper()
		return requestAs(t, session, csrf, method, path, body, status, out)
	}
	var catalog struct{ Items []httpapi.Entity }
	request(t, http.MethodGet, "/api/v1/entities?q=600519", "", http.StatusOK, &catalog)
	if len(catalog.Items) != 1 {
		t.Fatalf("search: %+v", catalog)
	}
	for _, tc := range []struct {
		skill, params, marketSection string
		omitted                      []string
	}{
		{"stock_analysis", `{}`, "S6", []string{"S3", "S4"}},
		{"capital_flow", `{}`, "S2", nil},
		{"deep_research", `{"mode":"company"}`, "S11", []string{"S4", "S8"}},
	} {
		if choiceLibrary != "" && tc.skill != "stock_analysis" {
			continue
		}
		t.Run(tc.skill, func(t *testing.T) {
			var plan httpapi.PlanResult
			request(t, http.MethodPost, "/api/v1/research-plans", fmt.Sprintf(`{"subject_id":%q,"skill_id":%q,"params":%s}`, catalog.Items[0].ID, tc.skill, tc.params), http.StatusOK, &plan)
			if plan.Plan == nil || !plan.Plan.Executable || plan.Plan.SkillVersion != harness.CloseUnifiedVersion {
				t.Fatalf("expected an executable %s plan: %+v", harness.CloseUnifiedVersion, plan.Plan)
			}
			var plannedRequirement string
			if err := pool.QueryRow(ctx, `SELECT quote_requirement FROM research_plans WHERE workspace_id=$1 AND id=$2`, ws, plan.Plan.ID).Scan(&plannedRequirement); err != nil {
				t.Fatal(err)
			}
			if plan.Plan.QuoteRequirement != nil || plannedRequirement != "latest_available" {
				t.Fatalf("legacy response quote requirement=%v, persisted requirement=%q", plan.Plan.QuoteRequirement, plannedRequirement)
			}
			var task httpapi.Task
			request(t, http.MethodPost, "/api/v1/research-tasks", fmt.Sprintf(`{"plan_id":%q,"plan_hash":%q}`, plan.Plan.ID, plan.Plan.PlanHash), http.StatusAccepted, &task)
			if err := a.Engine.Run(ctx, ws, task.ID, 1); err != nil {
				t.Fatal(err)
			}
			request(t, http.MethodGet, "/api/v1/research-tasks/"+task.ID.String(), "", http.StatusOK, &task)
			var taskRequirement string
			if err := pool.QueryRow(ctx, `SELECT p.quote_requirement FROM research_tasks t
				JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
				WHERE t.workspace_id=$1 AND t.id=$2`, ws, task.ID).Scan(&taskRequirement); err != nil {
				t.Fatal(err)
			}
			if task.QuoteRequirement != nil || taskRequirement != "latest_available" {
				t.Fatalf("legacy response task quote requirement=%v, persisted requirement=%q", task.QuoteRequirement, taskRequirement)
			}
			if task.ReportRef == nil {
				t.Fatalf("task did not deliver: status=%s reason=%v", task.Status, task.ReasonMessage)
			}
			base := fmt.Sprintf("/api/v1/reports/%s/versions/%d", task.ReportRef.ID, task.ReportRef.Version)
			var report httpapi.Report
			request(t, http.MethodGet, base, "", http.StatusOK, &report)
			if report.SkillVersion != harness.CloseUnifiedVersion || !report.Synthetic || len(report.Claims) == 0 {
				t.Fatalf("unexpected report version=%s synthetic=%t claims=%d", report.SkillVersion, report.Synthetic, len(report.Claims))
			}
			if len(tc.omitted) != 0 && report.ResultKind != "limited" {
				t.Fatalf("unavailable disclosure sections must downgrade the report: %s", report.ResultKind)
			}
			sections := map[string]harness.DocSection{}
			for _, section := range report.Sections {
				sections[section.ID] = section
			}
			for _, id := range tc.omitted {
				section := sections[id]
				if section.Status != "omitted" || section.Note == nil || *section.Note == "" || len(section.ClaimRefs)+len(section.Tables)+len(section.Charts) != 0 {
					t.Fatalf("section %s must explicitly omit unverified conclusions: %+v", id, section)
				}
			}
			readEvidence := func(id uuid.UUID) httpapi.Evidence {
				t.Helper()
				var ev httpapi.Evidence
				request(t, http.MethodGet, "/api/v1/evidence/"+id.String(), "", http.StatusOK, &ev)
				if (choiceLibrary == "" && ev.Provider != "TuShare Pro") || ev.ContentHash == "" {
					t.Fatalf("missing provider provenance: %+v", ev)
				}
				return ev
			}
			claims := map[string]harness.DocClaim{}
			for _, claim := range report.Claims {
				if len(claim.EvidenceRefs)+len(claim.CalcRefs) == 0 {
					t.Fatalf("unbound conclusion %s", claim.Ref)
				}
				claims[claim.Ref] = claim
			}
			for _, id := range []string{"S2", tc.marketSection} {
				if section := sections[id]; (section.Status != "ok" && section.Status != "limited") || len(section.ClaimRefs) == 0 {
					t.Fatalf("section %s unavailable: %+v", id, section)
				}
			}
			datasets := func(section string) map[string]bool {
				seen := map[string]bool{}
				for _, ref := range sections[section].ClaimRefs {
					for _, id := range claims[ref].EvidenceRefs {
						if ev := readEvidence(id); ev.Provider == "TuShare Pro" {
							seen[ev.DatasetID] = true
						}
					}
				}
				return seen
			}
			if tc.skill != "capital_flow" {
				if got := datasets("S2"); !got["ref.security"] || !got["ref.industry"] {
					t.Fatalf("company section lacks verifiable security and industry facts: %v", got)
				}
			}
			if got := datasets(tc.marketSection); !got["mkt.daily_bar"] {
				t.Fatalf("market section lacks verifiable market facts: %v", got)
			}
			if tc.skill == "capital_flow" {
				var balance, change bool
				for _, ref := range sections["S3"].ClaimRefs {
					claim := claims[ref]
					for _, id := range claim.EvidenceRefs {
						ev := readEvidence(id)
						balance = balance || (ev.DatasetID == "flow.margin" && strings.Contains(claim.Text, "融资余额"))
					}
					for _, id := range claim.CalcRefs {
						var calc httpapi.Calculation
						request(t, http.MethodGet, "/api/v1/calculations/"+id.String(), "", http.StatusOK, &calc)
						if calc.FormulaID != "F18.balance_change" || calc.Result == nil || len(calc.Inputs) != 2 {
							continue
						}
						change = true
						for _, in := range calc.Inputs {
							if ev := readEvidence(in.EvidenceID); ev.DatasetID != "flow.margin" || in.Unit != "CNY" || in.Value == "" {
								t.Fatalf("invalid financing input %v: %+v", in, ev)
							}
						}
					}
				}
				if !balance || !change {
					t.Fatalf("missing financing balance or calculation: balance=%t change=%t", balance, change)
				}
				var series facts.Series
				request(t, http.MethodGet, base+"/series/margin_balance", "", http.StatusOK, &series)
				if len(series.Evidence) < 2 || len(series.Lines) != 1 || len(series.Lines[0].Points) < 2 {
					t.Fatalf("financing series lacks verifiable points: %+v", series)
				}
			}
			pdf := request(t, http.MethodGet, base+"/pdf", "", http.StatusOK, nil)
			if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("%%EOF")) {
				t.Fatal("invalid PDF")
			}
			if choiceLibrary != "" {
				var evidenceID uuid.UUID
				var hash, choiceName string
				if err := pool.QueryRow(ctx, `SELECT b.evidence_id,o.content_hash,p.name FROM research_source_supplements b
					JOIN raw_objects o ON o.id=b.evidence_id AND o.provider_id=b.provider_id JOIN providers p ON p.id=b.provider_id
					WHERE b.workspace_id=$1 AND b.task_id=$2 AND b.provider_id=$3`, ws, task.ID, choiceProvider).Scan(&evidenceID, &hash, &choiceName); err != nil {
					t.Fatal(err)
				}
				bound := slices.ContainsFunc(sections[tc.marketSection].Tables, func(table facts.Table) bool {
					return slices.Contains(table.Evidence, evidenceID)
				})
				if !bound || !slices.ContainsFunc(report.Sources, func(source harness.DocSource) bool { return source.Provider == choiceName }) {
					t.Fatalf("Choice evidence absent from report: %s", evidenceID)
				}
				evidencePath := "/api/v1/evidence/" + evidenceID.String()
				var evidence httpapi.Evidence
				body := requestAs(t, session, csrf, http.MethodGet, evidencePath, "", http.StatusOK, &evidence, "application/vnd.vera.v0.4+json")
				if evidence.ID != evidenceID || evidence.Provider != choiceName || evidence.ContentHash != hash ||
					evidence.ReceivedAt == nil || evidence.Period == nil || evidence.Locator == nil || *evidence.Locator != "$.data.Values" ||
					evidence.EventAt != nil || bytes.Contains(body, []byte("s3://")) {
					t.Fatalf("unverified or excessive Choice evidence metadata: %+v", evidence)
				}
				_, otherSession, otherCSRF := newSession(t, "other-reader")
				requestAs(t, otherSession, otherCSRF, http.MethodGet, evidencePath, "", http.StatusNotFound, nil)
				if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id=$1`, choiceProvider); err != nil {
					t.Fatal(err)
				}
				request(t, http.MethodGet, base, "", http.StatusServiceUnavailable, nil)
				request(t, http.MethodGet, evidencePath, "", http.StatusServiceUnavailable, nil)
			}
		})
	}
}
