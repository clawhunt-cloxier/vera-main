//go:build linux && cgo

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/httpapi"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
)

// Use the same delivered SDK/archived records as the Harness replay checks
// and a real signed-in workspace session, with both providers routed by HTTP.
func checkMixedHTTP(t *testing.T, a *App, e *harness.Engine, store *dataservice.Store, subject dataservice.EntityRow, historical uuid.UUID, doc harness.Document, bar uuid.UUID, token string) {
	t.Helper()
	ctx := t.Context()
	handler := (&httpapi.Server{Pool: a.Pool, ID: identity.New(a.Pool, identity.Options{}), Engine: e, Store: store, FullVendorResearch: true}).Handler()
	const obs = "application/vnd.vera.observation.v1+json"
	const jsonMedia = "application/json"
	request := func(method, path, body, media, key string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
		req.Header.Set("Accept", media)
		req.Header.Set("Content-Type", media)
		req.Header.Set("X-CSRF-Token", identity.CSRFToken(token))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("%s %s = %d want %d: %.500s", method, path, resp.Code, want, resp.Body)
		}
		return resp
	}
	planBody := fmt.Sprintf(`{"skill_id":"stock_analysis","subject_id":%q,"params":{}}`, subject.ID)
	var mixedPlan httpapi.PlanResult
	if err := json.Unmarshal(request("POST", "/api/v1/research-plans", planBody, obs, "", 200).Body.Bytes(), &mixedPlan); err != nil || mixedPlan.Plan == nil || mixedPlan.Plan.SkillVersion != harness.MixedObservationVersion {
		t.Fatalf("wrong observation plan: %+v %v", mixedPlan, err)
	}
	taskBody := fmt.Sprintf(`{"plan_id":%q,"plan_hash":%q}`, mixedPlan.Plan.ID, mixedPlan.Plan.PlanHash)
	key := uuid.NewString()
	var pending httpapi.Task
	if err := json.Unmarshal(request("POST", "/api/v1/research-tasks", taskBody, obs, key, 202).Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if request("POST", "/api/v1/research-tasks", taskBody, obs, key, 202).Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("expected idempotent mixed task response")
	}

	// Both frontend protocols expose their registered skills and distinct
	// versions; the bare JSON route can preview a closing-data research plan.
	closeSkills := request("GET", "/api/v1/skills", "", jsonMedia, "", 200).Body.String()
	if !strings.Contains(closeSkills, harness.CloseCompanyVersion) || !strings.Contains(closeSkills, harness.CloseResearchVersion) {
		t.Fatal("missing closing research skills")
	}
	if _, err := a.Pool.Exec(ctx, `UPDATE entities SET attrs=attrs || '{"tushare_research_subject":true}'::jsonb WHERE id=$1`, subject.ID); err != nil {
		t.Fatal(err)
	}
	var closePlan httpapi.PlanResult
	if err := json.Unmarshal(request("POST", "/api/v1/research-plans", planBody, jsonMedia, "", 200).Body.Bytes(), &closePlan); err != nil || closePlan.Plan == nil || !harness.IsCloseResearchVersion(closePlan.Plan.SkillVersion) {
		t.Fatalf("expected closing plan: %+v %v", closePlan, err)
	}
	var oldReport, ws uuid.UUID
	if err := a.Pool.QueryRow(ctx, `SELECT report_id,workspace_id FROM report_versions WHERE task_id=$1`, historical).Scan(&oldReport, &ws); err != nil {
		t.Fatal(err)
	}
	oldPath := "/api/v1/reports/" + oldReport.String() + "/versions/1"
	reportPath := "/api/v1/reports/" + doc.ID.String() + "/versions/1"
	choiceID := uuid.Nil
	if err := e.WithMixedDisplay(ctx, ws, doc.TaskID, func(o emquant.BriefObservation, _ []dataservice.StoredRecord) error {
		choiceID = o.Manifest.Liquidity.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	evidencePath := "/api/v1/evidence/"
	paths := []string{reportPath, reportPath + "/pdf", evidencePath + bar.String(), evidencePath + choiceID.String()}
	for _, path := range paths {
		rec := request("GET", path, "", obs, "", 200)
		if strings.HasSuffix(path, "/pdf") && !strings.HasPrefix(rec.Body.String(), "%PDF") {
			t.Fatal("invalid mixed PDF")
		}
	}
	if body := request("GET", evidencePath+bar.String(), "", obs, "", 200).Body.String(); !strings.Contains(body, "TuShare") || !strings.Contains(body, "12.34") {
		t.Fatalf("incorrect frozen closing evidence: %.500s", body)
	}
	// Each protected reader has a single DB connection available. A reader
	// that holds a grant and tries to borrow another connection deadlocks.
	config := a.Pool.Config()
	config.MaxConns, config.MinConns = 1, 0
	smallPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer smallPool.Close()
	smallEngine := *e
	smallEngine.Pool = smallPool
	smallEngine.Store = dataservice.NewResearchStore(smallPool, store.Catalog(), "tushare", []string{subject.Code}, true)
	small := (&httpapi.Server{Pool: smallPool, ID: identity.New(smallPool, identity.Options{}), Engine: &smallEngine, Store: smallEngine.Store, FullVendorResearch: true}).Handler()
	var readers sync.WaitGroup
	for _, path := range []string{oldPath, reportPath, evidencePath + bar.String(), evidencePath + choiceID.String()} {
		readers.Go(func() {
			bounded, cancel := context.WithTimeout(ctx, 7*time.Second)
			defer cancel()
			req := httptest.NewRequest("GET", path, nil).WithContext(bounded)
			req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: token})
			req.Header.Set("Accept", obs)
			rec := httptest.NewRecorder()
			small.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("concurrent read %s = %d: %.300s", path, rec.Code, rec.Body)
			}
		})
	}
	readers.Wait()
	for _, disabled := range []string{"tushare", a.Brief.ProviderID} {
		if _, err := a.Pool.Exec(ctx, `UPDATE providers SET enabled=false WHERE id=$1`, disabled); err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			request("GET", path, "", obs, "", 503)
		}
		request("POST", "/api/v1/research-tasks", taskBody, obs, key, 503)
		request("GET", "/api/v1/research-plans/"+mixedPlan.Plan.ID.String(), "", obs, "", 503)
		var me httpapi.Me
		if err := json.Unmarshal(request("GET", "/api/v1/me", "", jsonMedia, "", 200).Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
		list := request("GET", "/api/v1/reports?page_size=1", "", obs, "", 200).Body.String()
		if strings.Contains(list, doc.ID.String()) {
			t.Fatal("mixed report remained visible after revocation")
		}
		if disabled == "tushare" {
			request("GET", oldPath, "", obs, "", 200)
			if !strings.Contains(list, oldReport.String()) || me.ResearchProtocol == "" || me.ClosingResearchAvailable {
				t.Fatal("Choice-only history/capability unavailable after closing-source revocation")
			}
			request("POST", "/api/v1/research-tasks/"+pending.ID.String()+"/cancel", fmt.Sprintf(`{"expected_revision":%d}`, pending.Revision), obs, uuid.NewString(), 204)
		} else {
			request("GET", oldPath, "", obs, "", 503)
			request("GET", "/api/v1/research-plans/"+closePlan.Plan.ID.String(), "", jsonMedia, "", 200)
			if strings.Contains(list, oldReport.String()) || me.ResearchProtocol != "" || !me.ClosingResearchAvailable {
				t.Fatal("incorrect source capabilities after Choice revocation")
			}
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE providers SET enabled=true WHERE id=$1`, disabled); err != nil {
			t.Fatal(err)
		}
	}
}
