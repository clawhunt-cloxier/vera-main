package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

func prices() *PriceTable {
	p := &PriceTable{Version: "test-1"}
	p.Models = map[string]struct {
		Peak    Price `json:"peak"`
		OffPeak Price `json:"off_peak"`
	}{"flash": {Peak: Price{decimal.RequireFromString("0.01"), decimal.RequireFromString("0.30"), decimal.RequireFromString("1.20")},
		OffPeak: Price{decimal.RequireFromString("0.005"), decimal.RequireFromString("0.15"), decimal.RequireFromString("0.60")}}}
	return p
}

func newTaskRequest(t *testing.T, pool *pgxpool.Pool, pol *policy.Policy) Request {
	t.Helper()
	var task, workspace uuid.UUID
	if err := pool.QueryRow(t.Context(), `WITH u AS (
		INSERT INTO users(issuer,subject) VALUES('test',uuidv7()::text) RETURNING id
	), w AS (
		INSERT INTO workspaces(owner_user_id) SELECT id FROM u RETURNING id
	), e AS (
		INSERT INTO entities(kind,code,name) VALUES('stock',uuidv7()::text,'Test stock') RETURNING id
	), p AS (
		INSERT INTO research_plans(workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,expires_at)
		SELECT w.id,'stock_analysis','test',e.id,'{}','[]',now(),'test',true,now()+interval '1 minute' FROM w,e
		RETURNING id,workspace_id,subject_id
	) INSERT INTO research_tasks(workspace_id,plan_id,skill_id,skill_version,subject_id,params,sections,as_of,status,deadline,budget)
	SELECT workspace_id,id,'stock_analysis','test',subject_id,'{}','[]',now(),'ANALYZING',now()+interval '1 minute','{}' FROM p
	RETURNING id,workspace_id`).Scan(&task, &workspace); err != nil {
		t.Fatal(err)
	}
	return Request{Workspace: workspace, Task: &task, TaskEpoch: 1, StepKey: "summary", Profile: "writer",
		Messages: []Message{{"user", "写摘要"}}, MaxOutput: 500, Schema: "summary-output", Budget: pol.Budgets["stock_analysis"]}
}

func TestGatewayOpenAIRetryRepairReuseBudget(t *testing.T) {
	pool := dbtest.New(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		switch n {
		case 1:
			w.WriteHeader(503) // transient → retry
			return
		case 2:
			json.NewEncoder(w).Encode(map[string]any{"model": "flash-1017", "choices": []any{map[string]any{"message": map[string]any{"content": `{"points":"bad"}`}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "prompt_cache_hit_tokens": 0, "prompt_cache_miss_tokens": 100}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "flash-1017", "choices": []any{map[string]any{"message": map[string]any{"content": `{"points":[{"text":"a","claim_refs":["S2.1"]}]}`}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 20, "prompt_cache_hit_tokens": 100, "prompt_cache_miss_tokens": 20}})
	}))
	defer srv.Close()
	pol, err := policy.Load("../../../../contracts/examples/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := LoadSchemas("../../../../contracts/model")
	if err != nil {
		t.Fatal(err)
	}
	prof := Profile{Name: "writer", Adapter: "openai", BaseURL: srv.URL, ModelID: "flash", APIKey: "test-key"}
	g := NewGateway(pool, map[string]Profile{"writer": prof}, map[string]Adapter{"openai": OpenAI{}}, prices(), sc, pol, decimal.RequireFromString("30"))
	g.wait = func(context.Context, time.Duration) error { return nil }
	req := newTaskRequest(t, pool, pol)
	out, meta, err := g.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"points":[{"text":"a","claim_refs":["S2.1"]}]}` || meta.ObservedModel != "flash-1017" || hits.Load() != 3 {
		t.Fatalf("out=%s meta=%+v hits=%d", out, meta, hits.Load())
	}
	var statuses []string
	rows, _ := pool.Query(context.Background(), `SELECT status FROM model_calls ORDER BY attempt`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		statuses = append(statuses, s)
	}
	if len(statuses) != 3 || statuses[0] != "FAILED" || statuses[1] != "FAILED" || statuses[2] != "SUCCEEDED" {
		t.Fatalf("ledger %v", statuses)
	}
	if !meta.Cost.IsPositive() {
		t.Fatalf("cost not computed: %s", meta.Cost)
	}
	// Redelivery of the same step reuses the successful call. Repair changed the messages, so reuse keys on the original
	// request only when the first attempt succeeded; here we verify a fresh identical request on a succeeded hash.
	before := hits.Load()
	req2 := req
	req2.StepKey = "summary-2"
	g.Complete(context.Background(), req2)
	after := hits.Load()
	if _, m, err := g.Complete(context.Background(), req2); err != nil || !m.Reused || hits.Load() != after {
		t.Fatalf("reuse failed: %+v %v hits %d→%d", m, err, before, hits.Load())
	}
	stale := req2
	stale.TaskEpoch++
	if _, _, err := g.Complete(t.Context(), stale); !errors.Is(err, ErrStaleTask) || hits.Load() != after {
		t.Fatalf("wrong epoch reused or reserved a model call: %v", err)
	}
	denied := errors.New("cached research use revoked")
	req2.WithReuse = func(context.Context, func(pgx.Tx) error) error { return denied }
	if _, _, err := g.Complete(t.Context(), req2); !errors.Is(err, denied) || hits.Load() != after {
		t.Fatalf("cache lookup bypassed current rights: %v", err)
	}
	// Budget: max model calls exhausted.
	req3 := req
	req3.StepKey = "x"
	req3.Budget.MaxModelCalls = 1
	if _, _, err := g.Complete(context.Background(), req3); !errors.Is(err, ErrBudget) {
		t.Fatalf("want budget error, got %v", err)
	}
}

func TestGatewayOpenAIToolFormatRepair(t *testing.T) {
	pool := dbtest.New(t)
	pol, err := policy.Load("../../../../contracts/examples/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := LoadSchemas("../../../../contracts/model")
	if err != nil {
		t.Fatal(err)
	}
	var workspace uuid.UUID
	if err := pool.QueryRow(t.Context(), `WITH u AS (
		INSERT INTO users(issuer,subject) VALUES('test',uuidv7()::text) RETURNING id
	) INSERT INTO workspaces(owner_user_id) SELECT id FROM u RETURNING id`).Scan(&workspace); err != nil {
		t.Fatal(err)
	}

	messages := []Message{{"system", "你是 Vera。"}, {"user", "会话环境（数据）"}, {"user", "演示制造的成交额"}}
	original := slices.Clone(messages)
	const name = "inspect_daily_trading"
	const validArgs = `{"subject_mentions":["演示制造"],"subject_source":"message","trading_days":0,"advice":"none"}`
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		var req struct {
			Messages   []Message `json:"messages"`
			Tools      []Tool    `json:"tools"`
			ToolChoice string    `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			len(req.Messages) != len(original) || req.Messages[0].Role != "system" ||
			!slices.Equal(req.Messages[1:], original[1:]) || len(req.Tools) != 1 ||
			req.Tools[0].Function.Name != name || req.ToolChoice != "auto" {
			t.Error("request must keep a single system message, the user messages and the tool declaration")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		args := validArgs
		switch n {
		case 1:
			if !slices.Equal(req.Messages, original) {
				t.Error("the first request must use the original messages")
			}
			args = `{"subject_source":"message","trading_days":"invalid"}`
		case 2:
			if !strings.HasPrefix(req.Messages[0].Content, original[0].Content+"\n\n") ||
				!strings.Contains(req.Messages[0].Content, "JSON Schema") ||
				strings.Contains(req.Messages[0].Content, "invalid") {
				t.Error("repair must append a trusted format hint to the original system message")
			}
		default:
			t.Errorf("unexpected model call %d", n)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err := json.NewEncoder(w).Encode(map[string]any{
			"model": "flash",
			"choices": []any{map[string]any{
				"finish_reason": "tool_calls",
				"message": map[string]any{"content": nil, "tool_calls": []ToolCall{
					{ID: "native-call", Type: "function", Function: ToolArguments{Name: name, Arguments: args}},
				}},
			}},
			"usage": Usage{PromptTokens: 120, CompletionTokens: 30},
		})
		if err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	prof := Profile{Name: "router", Adapter: "openai", BaseURL: srv.URL, ModelID: "flash"}
	g := NewGateway(pool, map[string]Profile{"router": prof}, map[string]Adapter{"openai": OpenAI{}}, prices(), schemas, pol, decimal.NewFromInt(30))
	out, meta, err := g.Complete(t.Context(), Request{
		Workspace: workspace, StepKey: "conversation_agent", Profile: "router", Messages: messages,
		MaxOutput: 384, Schema: "conversation-answer-output", EraseContent: true,
		Tools: []ToolSpec{{Name: name, Schema: "conversation-window-tool"}},
	})
	if err != nil || len(out) != 0 || meta.ToolCall == nil || meta.ToolCall.Function.Arguments != validArgs || hits.Load() != 2 {
		t.Fatalf("out=%s meta=%+v err=%v calls=%d", out, meta, err, hits.Load())
	}
	if !slices.Equal(messages, original) {
		t.Fatal("repair mutated the caller's messages")
	}
}

func TestSimAndReplay(t *testing.T) {
	dir := t.TempDir()
	sim := Sim{}
	c := Call{Profile: Profile{Name: "writer"}, Messages: []Message{{"user", "x"}}, Hash: "abcdef0123", Simulate: func() (any, error) {
		return map[string]any{"points": []any{}}, nil
	}}
	rp := Replay{Dir: dir}
	if _, err := rp.Do(context.Background(), c); err == nil {
		t.Fatal("replay miss must fail")
	}
	rec := Replay{Dir: dir, Record: sim}
	if _, err := rec.Do(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	raw, err := rp.Do(context.Background(), c)
	if err != nil || raw.Content != `{"points":[]}` {
		t.Fatalf("replay %v %+v", err, raw)
	}
}
