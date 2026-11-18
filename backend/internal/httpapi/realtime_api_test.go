package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
)

func realtimeAPIEnv(t *testing.T) (*env, uuid.UUID) {
	t.Helper()
	var entity uuid.UUID
	e := setupEngine(t, func(e *harness.Engine) {
		ctx := context.Background()
		subject, err := e.Store.UpsertSearchEntity(ctx, "stock", "600000.SH", "API synthetic stock", nil)
		if err != nil {
			t.Fatal(err)
		}
		entity = subject.ID
		if _, err := e.Pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic,allow_model_processing) VALUES('api-test','Synthetic API test provider','vendor',false,true)`); err != nil {
			t.Fatal(err)
		}
		source := harness.BriefQuoteSource{ProviderID: "api-test", Product: "synthetic-product"}
		var licenseID, policyID uuid.UUID
		if err := e.Pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
		VALUES('api-test','synthetic-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW','TRADESTATUS'],'all','{}','synthetic-evidence','synthetic-test',clock_timestamp(),clock_timestamp()-interval '1 minute',true,true,true) RETURNING id`).Scan(&licenseID); err != nil {
			t.Fatal(err)
		}
		if err := e.Pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,latency_evidence_ref,max_age_ms,verified_by,verified_at,valid_from,status_indicator,status_evidence_ref,status_mapping)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price','real_time','synthetic-latency',60000,'synthetic-test',clock_timestamp(),clock_timestamp()-interval '1 minute','TRADESTATUS','synthetic-status','{"s:OPEN":"continuous"}') RETURNING id`, licenseID).Scan(&policyID); err != nil {
			t.Fatal(err)
		}
		// These protocol tests never collect data or call a vendor. Runtime
		// presence enables the HTTP capability; actual grants are tested in DB.
		e.Brief = &emquant.BriefService{Runtime: &emquant.Runtime{}, ProviderID: "api-test", Product: "synthetic-product"}
		e.RealtimeSource = &source
		e.RealResearch = true
		e.Skills["stock_analysis"] = harness.ObservationBriefSkill()
		e.Now = time.Now
	})
	return e, entity
}

func onboardAPIClient(t *testing.T, e *env, name string) *client {
	t.Helper()
	c := e.login(t, name)
	codes, err := identity.CreateInvites(context.Background(), e.pool, 1, 1, time.Hour, "synthetic-test", "synthetic-test")
	if err != nil {
		t.Fatal(err)
	}
	c.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[0], "persona": "analyst"}, 200, nil)
	return c
}

func apiHeaders(media string) map[string]string {
	return map[string]string{"Accept": media, "Content-Type": media}
}

func TestRealtimeAPIPlanConfirmationAndHistoricalReads(t *testing.T) {
	e, entity := realtimeAPIEnv(t)
	c := onboardAPIClient(t, e, "realtime-api")
	k := newContract(t)
	request := func(method, path string, body any, media, schemaName string, status int) []byte {
		t.Helper()
		got, data, headers := c.do(method, path, body, apiHeaders(media))
		if got != status {
			t.Fatalf("%s %s (%s) = %d, want %d: %s", method, path, media, got, status, data)
		}
		if status < 300 && strings.Split(headers.Get("Content-Type"), ";")[0] != media {
			t.Fatalf("wrong media: %s", headers.Get("Content-Type"))
		}
		if schemaName != "" {
			k.check(t, schema(schemaName), data)
		}
		return data
	}
	var me Me
	if err := json.Unmarshal(request("GET", "/api/v1/me", nil, realtimeMedia, "Me", 200), &me); err != nil || !me.RealtimeTradeAvailable {
		t.Fatalf("verified continuous-trading policy missing from /me: %v", err)
	}
	request("GET", "/api/v1/entities?q="+url.QueryEscape("API synthetic stock"), nil, realtimeMedia, "", 200)
	var skills struct {
		Items []Skill `json:"items"`
	}
	if err := json.Unmarshal(request("GET", "/api/v1/skills", nil, realtimeMedia, "", 200), &skills); err != nil || len(skills.Items) == 0 || skills.Items[0].Version != harness.RealtimeBriefVersion {
		t.Fatalf("0.4 skill catalog differs from its plan version: %+v %v", skills, err)
	}
	var plans [2]PlanResult
	for i, requirement := range []string{"realtime_trade", "latest_available"} {
		body := map[string]any{"skill_id": "stock_analysis", "subject_id": entity, "params": map[string]any{}, "quote_requirement": requirement}
		if i == 1 {
			body = map[string]any{"text": "分析API synthetic stock", "quote_requirement": requirement}
		}
		data := request("POST", "/api/v1/research-plans", body, realtimeMedia, "PlanResultV04", 200)
		if err := json.Unmarshal(data, &plans[i]); err != nil {
			t.Fatal(err)
		}
		if plans[i].Plan == nil || !plans[i].Plan.Executable || plans[i].Plan.QuoteRequirement == nil || *plans[i].Plan.QuoteRequirement != requirement {
			t.Fatalf("incorrect plan: %s", data)
		}
		request("GET", "/api/v1/research-plans/"+plans[i].Plan.ID.String(), nil, realtimeMedia, "ResearchPlanV04", 200)
		request("GET", "/api/v1/research-plans/"+plans[i].Plan.ID.String(), nil, observationMedia, "Error", 406)
	}
	confirm := map[string]any{"plan_id": plans[0].Plan.ID, "plan_hash": plans[0].Plan.PlanHash, "disabled_sections": []string{}}
	request("POST", "/api/v1/research-tasks", confirm, observationMedia, "Error", 409)
	data := request("POST", "/api/v1/research-tasks", confirm, realtimeMedia, "TaskV04", 202)
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	if task.SnapshotAsOf != nil || task.QuoteRequirement == nil || *task.QuoteRequirement != "realtime_trade" {
		t.Fatalf("task not pending correct freeze: %s", data)
	}
	request("GET", "/api/v1/research-tasks/"+task.ID.String(), nil, realtimeMedia, "TaskV04", 200)
	request("GET", "/api/v1/research-tasks", nil, realtimeMedia, "", 200)
	request("GET", "/api/v1/research-tasks/"+task.ID.String(), nil, legacyMedia, "Error", 406)
	var requirement string
	if err := e.pool.QueryRow(context.Background(), `SELECT requirement FROM quote_task_intents WHERE task_id=$1`, task.ID).Scan(&requirement); err != nil || requirement != "realtime_trade" {
		t.Fatalf("missing deterministic intent: %q %v", requirement, err)
	}
	// Historical observation plans remain readable in 0.4 but must be
	// confirmed using observation.v1, preserving their non-realtime intent.
	var observation PlanResult
	data = request("POST", "/api/v1/research-plans", map[string]any{"skill_id": "stock_analysis", "subject_id": entity, "params": map[string]any{}}, observationMedia, "PlanResult", 200)
	if err := json.Unmarshal(data, &observation); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/v1/research-plans/"+observation.Plan.ID.String(), nil, realtimeMedia, "ResearchPlanV04", 200)
	oldConfirm := map[string]any{"plan_id": observation.Plan.ID, "plan_hash": observation.Plan.PlanHash, "disabled_sections": []string{}}
	request("POST", "/api/v1/research-tasks", oldConfirm, realtimeMedia, "Error", 409)
	data = request("POST", "/api/v1/research-tasks", oldConfirm, observationMedia, "Task", 202)
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/v1/research-tasks/"+task.ID.String(), nil, realtimeMedia, "TaskV04", 200)
	// Workspace isolation applies before any privileged source reads.
	other := onboardAPIClient(t, e, "realtime-api-other")
	code, _, _ := other.do(http.MethodGet, "/api/v1/research-plans/"+plans[0].Plan.ID.String(), nil, apiHeaders(realtimeMedia))
	if code != 404 {
		t.Fatalf("cross-workspace plan access: %d", code)
	}
}

func TestRealtimeAPIRejectsMalformedOrMixedPlanInputs(t *testing.T) {
	e, entity := realtimeAPIEnv(t)
	c := onboardAPIClient(t, e, "realtime-input")
	for _, tc := range []struct {
		body   map[string]any
		media  string
		status int
	}{
		{map[string]any{"text": "分析API synthetic stock"}, realtimeMedia, 400},
		{map[string]any{"text": "分析API synthetic stock", "quote_requirement": nil}, realtimeMedia, 422},
		{map[string]any{"text": "分析API synthetic stock", "quote_requirement": "guess"}, realtimeMedia, 422},
		{map[string]any{"text": "分析API synthetic stock", "params": map[string]any{}, "quote_requirement": "latest_available"}, realtimeMedia, 400},
		{map[string]any{"skill_id": "stock_analysis", "subject_id": entity, "quote_requirement": "latest_available"}, realtimeMedia, 400},
		{map[string]any{"skill_id": "stock_analysis", "subject_id": entity, "params": nil, "quote_requirement": "latest_available"}, realtimeMedia, 400},
		{map[string]any{"text": "分析API synthetic stock", "quote_requirement": nil}, observationMedia, 400},
	} {
		status, body, _ := c.do("POST", "/api/v1/research-plans", tc.body, apiHeaders(tc.media))
		if status != tc.status {
			t.Fatalf("body %+v: %d want %d: %s", tc.body, status, tc.status, body)
		}
	}
}
