package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/tushare"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/httpapi"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

// The catalog candidates are already discovered. Exercise the API, River queue,
// TuShare HTTP adapter, publication, and both fixed-cutoff plans against test data.
func TestPrepareSubjectHTTPPublishesBeforeClosingPlans(t *testing.T) {
	ctx := t.Context()
	pool := dbtest.New(t)
	archive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected archive method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
	}))
	defer archive.Close()
	upstream := newTuShareUpstream(t, time.Now()).(*tsTestUpstream)
	for _, row := range upstream.rows["stock_basic"] {
		row["curr_type"] = "CNY"
	}
	transport := http.DefaultTransport
	http.DefaultTransport = tuShareTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme == "http" && "http://"+r.URL.Host == archive.URL {
			return transport.RoundTrip(r)
		}
		return upstream.RoundTrip(r)
	})
	defer func() { http.DefaultTransport = transport }()
	settings := map[string]string{
		"TUSHARE_SUBJECTS": "000001.SZ,600999.SH", "TUSHARE_TOKEN": "synthetic", "TUSHARE_ALLOW_MODEL_PROCESSING": "true",
		"TUSHARE_S3_ENDPOINT": archive.URL, "TUSHARE_S3_BUCKET": "test", "TUSHARE_S3_REGION": "test",
		"TUSHARE_S3_ACCESS_KEY": "synthetic", "TUSHARE_S3_SECRET_KEY": "synthetic", "TUSHARE_MIN_INTERVAL": "100ms",
	}
	provider, err := tushare.Open(pool, func(key string) string { return settings[key] })
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load("../../../contracts/examples/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	store := dataservice.NewOnDemandResearchStore(pool, cat, provider.SubjectCodes(), true)
	choice := dataservice.NewVendorStore(pool, cat)
	brief := &emquant.BriefService{Runtime: &emquant.Runtime{}, ProviderID: "synthetic-choice", Product: "test"}
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('tushare','test TuShare','vendor',true),('synthetic-choice','test Choice','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: &config.Config{RealResearch: true}, Pool: pool, Store: store, Brief: brief, TuShare: provider}
	engine := &harness.Engine{Pool: pool, Store: store, ChoiceCatalog: choice, Skills: harness.UnifiedCloseSkills(), Policy: pol,
		Brief: brief, Now: time.Now, RealResearch: true, PrepareTuShareSubject: a.PrepareTuShareSubject,
		WithCloseResearchUse: store.WithResearchUse, CloseResearchCutoff: store.PublicationCutoff}
	worker, err := engine.NewRiver(pool, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	candidate := func(code string) dataservice.EntityRow {
		t.Helper()
		item, err := choice.UpsertSearchEntity(ctx, "stock", code, "模拟候选"+code, nil)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	target := candidate(tsTestSubject)
	// Configured for prewarm, but previously seen only in the Choice directory.
	uncovered := candidate("600999.SH")
	var uid, ws uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('synthetic','prepare') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, uid).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	token := identity.Random(32)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,workspace_id,csrf_hash,expires_at)
		VALUES($1,$2,$3,$4,now()+interval '1 hour')`, identity.Hash(token), uid, ws, identity.Hash(identity.CSRFToken(token))); err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{Pool: pool, Store: store, Engine: engine, FullVendorResearch: true, ID: identity.New(pool, identity.Options{})}).Handler()
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		req.Header.Set("X-CSRF-Token", identity.CSRFToken(token))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	prepare := func(id uuid.UUID) *httptest.ResponseRecorder {
		return post("/api/v1/research-subjects/"+id.String()+"/prepare", "{}")
	}
	checkPreparation := func(rec *httptest.ResponseRecorder, code int, status string) harness.SubjectPreparation {
		t.Helper()
		var result harness.SubjectPreparation
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != code || result.Status != status {
			t.Fatalf("prepare = %d %s, want %d %s", rec.Code, rec.Body, code, status)
		}
		return result
	}
	plan := func(id uuid.UUID, skill string) *httptest.ResponseRecorder {
		return post("/api/v1/research-plans", fmt.Sprintf(`{"subject_id":%q,"skill_id":%q,"params":{}}`, id, skill))
	}
	for _, skill := range []string{"capital_flow", "deep_research"} {
		if rec := plan(target.ID, skill); rec.Code != 422 || !strings.Contains(rec.Body.String(), "SUBJECT_NOT_SUPPORTED") {
			t.Fatalf("unprepared %s admitted: %d %s", skill, rec.Code, rec.Body)
		}
	}
	pending := make(chan *httptest.ResponseRecorder, 8)
	for range cap(pending) {
		go func() { pending <- prepare(target.ID) }()
	}
	for range cap(pending) {
		checkPreparation(<-pending, 202, "pending")
	}
	checkPreparation(prepare(uncovered.ID), 202, "pending")
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='tushare_subject_prepare'`).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatalf("jobs for two distinct candidates = %d: %v", jobs, err)
	}

	events, unsubscribe := worker.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	defer unsubscribe()
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := worker.StopAndCancel(stop); err != nil {
			t.Error(err)
		}
	}()
	deadline, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for range 2 {
		select {
		case <-events:
		case <-deadline.Done():
			t.Fatal("worker did not finish both preparations", deadline.Err())
		}
	}
	checkPreparation(prepare(target.ID), 200, "ready")
	if failed := checkPreparation(prepare(uncovered.ID), 200, "failed"); !strings.Contains(failed.Message, "覆盖") {
		t.Fatalf("missing coverage explanation: %q", failed.Message)
	}
	var published time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at FROM tushare_requested_subjects WHERE code=$1`, target.Code).Scan(&published); err != nil {
		t.Fatal(err)
	}
	for _, skill := range []string{"capital_flow", "deep_research"} {
		rec := plan(target.ID, skill)
		var result httpapi.PlanResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 || result.Plan == nil {
			t.Fatalf("%s = %d %s", skill, rec.Code, rec.Body)
		}
		p := result.Plan
		if !p.Executable || p.SkillID != skill || p.SkillVersion != harness.CloseUnifiedVersion || p.Subject.ID != target.ID || p.AsOf.Before(published) {
			t.Fatalf("%s resolved unexpected plan %+v", skill, p)
		}
		if rec := plan(uncovered.ID, skill); rec.Code != 422 {
			t.Fatalf("failed subject admitted to %s: %d %s", skill, rec.Code, rec.Body)
		}
	}
}
