package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity/devoidc"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/watchlist"
)

const root = "../../../"

type contract struct {
	c    *jsonschema.Compiler
	base string
}

func newContract(t *testing.T) *contract {
	p, _ := filepath.Abs(root + "contracts/openapi.json")
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource(p, doc); err != nil {
		t.Fatal(err)
	}
	return &contract{c: c, base: p}
}

func (c *contract) check(t *testing.T, ptr string, body []byte) {
	t.Helper()
	s, err := c.c.Compile(c.base + "#" + ptr)
	if err != nil {
		t.Fatalf("compile %s: %v", ptr, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: invalid json %s", ptr, body)
	}
	if err := s.Validate(v); err != nil {
		t.Fatalf("%s violates contract: %v\n%s", ptr, err, clip(string(body), 1200))
	}
}

func schema(name string) string { return "/components/schemas/" + name }
func resp(path, method, code string) string {
	return "/paths/" + strings.ReplaceAll(strings.ReplaceAll(path, "~", "~0"), "/", "~1") + "/" + method + "/responses/" + code + "/content/application~1json/schema"
}

type env struct {
	srv    *httptest.Server
	pool   *pgxpool.Pool
	engine *harness.Engine
}

func setup(t *testing.T) *env {
	pool := dbtest.New(t)
	ctx := context.Background()
	p, err := fixture.New(root + "tests/fixtures/demo-universe.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := dataservice.LoadCatalog(root + "contracts/datasets/catalog.json")
	st := dataservice.NewStore(pool, cat)
	if _, err := st.Sync(ctx, p); err != nil {
		t.Fatal(err)
	}
	pol, _ := policy.Load(root + "contracts/examples/policy.json")
	sc, _ := model.LoadSchemas(root + "contracts/model")
	profiles := map[string]model.Profile{}
	for _, n := range []string{"writer", "verifier", "router"} {
		profiles[n] = model.Profile{Name: n, Adapter: "sim", ModelID: "vera-sim-1"}
	}
	gw := model.NewGateway(pool, profiles, map[string]model.Adapter{"sim": model.Sim{}}, nil, sc, pol, decimal.RequireFromString("30"))
	eng := &harness.Engine{Pool: pool, Store: st, Gateway: gw, Policy: pol, Skills: harness.Skills(), Now: p.Now, Synthetic: true, Basis: p.Basis()}
	if _, err := eng.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	idp, _ := devoidc.New(srv.URL+"/dev-oidc", "vera-web", "dev-secret", srv.URL+"/auth/callback")
	id := identity.New(pool, identity.Options{Registration: true, Issuer: idp.Issuer, ClientID: "vera-web", ClientSecret: "dev-secret", PublicURL: srv.URL})
	s := &Server{Pool: pool, ID: id, Store: st, Engine: eng, Watch: &watchlist.Service{Pool: pool, Store: st, Now: p.Now}, Synthetic: true,
		Mounts: map[string]http.Handler{"/dev-oidc/": http.StripPrefix("/dev-oidc", idp.Handler())}}
	mux.Handle("/", s.Handler())
	return &env{srv, pool, eng}
}

type client struct {
	t    *testing.T
	e    *env
	http *http.Client
	csrf string
}

func (e *env) login(t *testing.T, user string) *client {
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, e: e, http: &http.Client{Jar: jar}}
	r, err := c.http.Get(e.srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	form := url.Values{}
	for k, v := range r.Request.URL.Query() {
		form.Set(k, v[0])
	}
	form.Set("username", user)
	r, err = c.http.PostForm(r.Request.URL.String(), form)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	c.json("GET", "/api/v1/me", nil, 200, &me)
	c.csrf = me.CSRF
	return c
}

func (c *client) do(method, path string, body any, hdr map[string]string) (int, []byte, http.Header) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		req.Header.Set("X-CSRF-Token", c.csrf)
		req.Header.Set("Origin", c.e.srv.URL)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b, r.Header
}

func (c *client) json(method, path string, body any, want int, out any) []byte {
	c.t.Helper()
	code, b, _ := c.do(method, path, body, nil)
	if code != want {
		c.t.Fatalf("%s %s: %d want %d: %s", method, path, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatal(err)
		}
	}
	return b
}

func TestAPIFlow(t *testing.T) {
	e := setup(t)
	k := newContract(t)
	ctx := context.Background()
	c := e.login(t, "alice")

	// Before onboarding: /me works, private API refused.
	b := c.json("GET", "/api/v1/me", nil, 200, nil)
	k.check(t, schema("Me"), b)
	if code, _, _ := c.do("GET", "/api/v1/watchlist", nil, nil); code != 403 {
		t.Fatalf("watchlist before onboarding: %d", code)
	}
	codes, _ := identity.CreateInvites(ctx, e.pool, 2, 1, time.Hour, "test", "test")
	b = c.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[0], "persona": "analyst"}, 200, nil)
	k.check(t, schema("Me"), b)

	b = c.json("GET", "/api/v1/skills", nil, 200, nil)
	k.check(t, resp("/api/v1/skills", "get", "200"), b)
	var ents struct {
		Items []struct {
			ID   uuid.UUID `json:"id"`
			Kind string    `json:"kind"`
		} `json:"items"`
	}
	b = c.json("GET", "/api/v1/entities?q="+url.QueryEscape("演示制造"), nil, 200, &ents)
	k.check(t, resp("/api/v1/entities", "get", "200"), b)
	if len(ents.Items) == 0 {
		t.Fatal("entity search empty")
	}
	// Free text → plan.
	var pr struct {
		Kind string `json:"kind"`
		Plan struct {
			ID       uuid.UUID `json:"id"`
			PlanHash string    `json:"plan_hash"`
		} `json:"plan"`
	}
	b = c.json("POST", "/api/v1/research-plans", map[string]string{"text": "分析演示制造"}, 200, &pr)
	k.check(t, schema("PlanResult"), b)
	if pr.Kind != "plan" {
		t.Fatalf("plan kind %s", pr.Kind)
	}
	b = c.json("POST", "/api/v1/research-plans", map[string]string{"text": "演示制造能买吗"}, 200, nil)
	k.check(t, schema("PlanResult"), b)
	b = c.json("GET", "/api/v1/research-plans/"+pr.Plan.ID.String(), nil, 200, nil)
	k.check(t, schema("ResearchPlan"), b)
	// Missing CSRF is refused.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/research-tasks", strings.NewReader(`{}`))
	if r, _ := c.http.Do(req); r.StatusCode != 403 {
		t.Fatalf("csrf: %d", r.StatusCode)
	}
	// Confirm with idempotency key; replay returns the same task.
	key := map[string]string{"Idempotency-Key": uuid.NewString()}
	body := map[string]any{"plan_id": pr.Plan.ID, "plan_hash": pr.Plan.PlanHash, "disabled_sections": []string{}}
	code, tb, _ := c.do("POST", "/api/v1/research-tasks", body, key)
	if code != 202 {
		t.Fatalf("create task %d %s", code, tb)
	}
	k.check(t, schema("Task"), tb)
	code2, tb2, h2 := c.do("POST", "/api/v1/research-tasks", body, key)
	var a1, a2 map[string]any
	_ = json.Unmarshal(tb, &a1)
	_ = json.Unmarshal(tb2, &a2)
	if code2 != 202 || a1["id"] != a2["id"] || h2.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("idempotent replay: %d %s", code2, h2)
	}
	var task struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(tb, &task)
	var ws uuid.UUID
	e.pool.QueryRow(ctx, `SELECT workspace_id FROM research_tasks WHERE id=$1`, task.ID).Scan(&ws)
	if err := e.engine.Run(ctx, ws, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	var tk struct {
		Status    string `json:"status"`
		ReportRef struct {
			ID      uuid.UUID `json:"id"`
			Version int       `json:"version"`
		} `json:"report_ref"`
		Steps []any `json:"steps"`
	}
	b = c.json("GET", "/api/v1/research-tasks/"+task.ID.String(), nil, 200, &tk)
	k.check(t, schema("Task"), b)
	if tk.Status != "COMPLETED" || len(tk.Steps) < 10 {
		t.Fatalf("task %s steps %d", tk.Status, len(tk.Steps))
	}
	b = c.json("GET", "/api/v1/research-tasks?page_size=1", nil, 200, nil)
	k.check(t, resp("/api/v1/research-tasks", "get", "200"), b)
	rp := fmt.Sprintf("/api/v1/reports/%s/versions/%d", tk.ReportRef.ID, tk.ReportRef.Version)
	var rep struct {
		Claims []struct {
			Ref   string      `json:"ref"`
			Ev    []uuid.UUID `json:"evidence_refs"`
			Calcs []uuid.UUID `json:"calculation_refs"`
		} `json:"claims"`
		Sections []struct {
			Charts []struct {
				SeriesID string `json:"series_id"`
			} `json:"charts"`
		} `json:"sections"`
	}
	b = c.json("GET", rp, nil, 200, &rep)
	k.check(t, schema("Report"), b)
	b = c.json("GET", "/api/v1/reports", nil, 200, nil)
	k.check(t, resp("/api/v1/reports", "get", "200"), b)
	// Evidence, calculation and series.
	seenCalc := false
	for _, cl := range rep.Claims {
		b = c.json("GET", "/api/v1/evidence/"+cl.Ev[0].String(), nil, 200, nil)
		k.check(t, schema("Evidence"), b)
		if len(cl.Calcs) > 0 && !seenCalc {
			b = c.json("GET", "/api/v1/calculations/"+cl.Calcs[0].String(), nil, 200, nil)
			k.check(t, schema("Calculation"), b)
			seenCalc = true
		}
	}
	if !seenCalc {
		t.Fatal("no calculation referenced")
	}
	charts := 0
	for _, s := range rep.Sections {
		for _, ch := range s.Charts {
			b = c.json("GET", rp+"/series/"+ch.SeriesID, nil, 200, nil)
			k.check(t, schema("ChartSeries"), b)
			charts++
		}
	}
	if charts == 0 {
		t.Fatal("report has no charts")
	}
	b = c.json("POST", rp+"/feedback", map[string]any{"kind": "useful", "claim_ref": rep.Claims[0].Ref}, 201, nil)
	k.check(t, schema("Feedback"), b)
	// Watchlist.
	var wi struct {
		ID      uuid.UUID `json:"id"`
		Version int64     `json:"version"`
	}
	b = c.json("POST", "/api/v1/watchlist", map[string]any{"entity_id": ents.Items[0].ID, "tags": []string{"演示"},
		"alerts": []map[string]any{{"kind": "price_above", "threshold": "1", "enabled": true}}}, 201, &wi)
	k.check(t, schema("WatchItem"), b)
	b = c.json("GET", "/api/v1/watchlist", nil, 200, nil)
	k.check(t, resp("/api/v1/watchlist", "get", "200"), b)
	if !strings.Contains(string(b), "高于设定的") || !strings.Contains(string(b), "last_report\":{") {
		t.Fatalf("watchlist missing alert or last report: %s", clip(string(b), 800))
	}
	c.json("PATCH", "/api/v1/watchlist/"+wi.ID.String(), map[string]any{"note": "x", "expected_version": wi.Version + 3}, 409, nil)
	c.json("PATCH", "/api/v1/watchlist/"+wi.ID.String(), map[string]any{"note": "x", "expected_version": wi.Version}, 200, nil)

	// Another user cannot see alice's report, evidence-by-task, plan or watch item.
	bob := e.login(t, "bob")
	bob.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[1], "persona": "individual"}, 200, nil)
	bob.json("GET", rp, nil, 404, nil)
	bob.json("GET", "/api/v1/research-tasks/"+task.ID.String(), nil, 404, nil)
	bob.json("GET", "/api/v1/evidence/"+rep.Claims[0].Ev[0].String(), nil, 404, nil)
	bob.json("DELETE", "/api/v1/watchlist/"+wi.ID.String(), nil, 404, nil)
	c.json("DELETE", "/api/v1/watchlist/"+wi.ID.String(), nil, 204, nil)

	b = c.json("GET", "/readyz", nil, 200, nil)
	k.check(t, resp("/readyz", "get", "200"), b)
}
