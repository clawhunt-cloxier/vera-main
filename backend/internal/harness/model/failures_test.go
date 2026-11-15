package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

const okSummary = `{"points":[{"text":"a","claim_refs":["S2.1"]}]}`

func reply(w http.ResponseWriter, content, finish string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "flash-1017",
		"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "prompt_cache_hit_tokens": 40, "prompt_cache_miss_tokens": 60}})
}

type harnessT struct {
	pool  *pgxpool.Pool
	g     *Gateway
	slept []time.Duration
	pol   *policy.Policy
}

func newGW(t *testing.T, h http.HandlerFunc, prof Profile) *harnessT {
	t.Helper()
	pool := dbtest.New(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	pol, err := policy.Load("../../../../contracts/examples/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := LoadSchemas("../../../../contracts/model")
	if err != nil {
		t.Fatal(err)
	}
	prof.Name, prof.BaseURL, prof.ModelID, prof.APIKey = "writer", srv.URL, "flash", "test-key"
	if prof.Adapter == "" {
		prof.Adapter = "openai"
	}
	x := &harnessT{pool: pool, pol: pol}
	x.g = NewGateway(pool, map[string]Profile{"writer": prof}, map[string]Adapter{"openai": OpenAI{}}, prices(), sc, pol, decimal.RequireFromString("30"))
	x.g.sleep = func(d time.Duration) { x.slept = append(x.slept, d) }
	return x
}

func (x *harnessT) req() Request {
	task := uuid.New()
	return Request{Workspace: uuid.New(), Task: &task, StepKey: "summary", Profile: "writer", Messages: []Message{{"user", "写摘要"}},
		MaxOutput: 500, Schema: "summary-output", Budget: x.pol.Budgets["stock_analysis"]}
}

func (x *harnessT) ledger(t *testing.T) []string {
	t.Helper()
	rows, err := x.pool.Query(context.Background(), `SELECT status FROM model_calls ORDER BY attempt, sent_at`)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func TestRateLimitHonoursRetryAfter(t *testing.T) {
	var n atomic.Int32
	x := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			return
		}
		reply(w, okSummary, "stop")
	}, Profile{})
	if _, _, err := x.g.Complete(context.Background(), x.req()); err != nil {
		t.Fatal(err)
	}
	if len(x.slept) != 1 || x.slept[0] != 7*time.Second {
		t.Fatalf("waits %v, want [7s]", x.slept)
	}
	if l := x.ledger(t); len(l) != 2 || l[0] != "FAILED" || l[1] != "SUCCEEDED" {
		t.Fatalf("ledger %v", l)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	var n atomic.Int32
	x := newGW(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(400) }, Profile{})
	if _, _, err := x.g.Complete(context.Background(), x.req()); err == nil || n.Load() != 1 {
		t.Fatalf("4xx: err=%v hits=%d", err, n.Load())
	}
}

func TestTimeoutIsUnknownAndHoldsReservation(t *testing.T) {
	var n atomic.Int32
	x := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}, Profile{Timeout: 50 * time.Millisecond})
	_, _, err := x.g.Complete(context.Background(), x.req())
	var me *Error
	if !errors.As(err, &me) || !me.Unknown {
		t.Fatalf("want unknown error, got %v", err)
	}
	l := x.ledger(t)
	if len(l) != 1+x.pol.ModelCall.TransientRetries || n.Load() != int32(len(l)) {
		t.Fatalf("ledger %v hits %d", l, n.Load())
	}
	for _, s := range l {
		if s != "UNKNOWN" {
			t.Fatalf("ledger %v", l)
		}
	}
	ctx := context.Background()
	var held, perCall decimal.Decimal
	_ = x.pool.QueryRow(ctx, `SELECT sum(reserved_usd), min(reserved_usd) FROM model_calls WHERE status='UNKNOWN' AND cost_usd IS NULL`).Scan(&held, &perCall)
	if !perCall.IsPositive() {
		t.Fatal("UNKNOWN call holds no reservation")
	}
	// Not yet 24h old: nothing released.
	if ids, err := x.g.ReleaseUnknown(ctx, time.Now().Add(-UnknownReleaseAfter)); err != nil || len(ids) != 0 {
		t.Fatalf("early release %v %v", ids, err)
	}
	_, _ = x.pool.Exec(ctx, `UPDATE model_calls SET sent_at = now() - interval '25 hours'`)
	ids, err := x.g.ReleaseUnknown(ctx, time.Now().Add(-UnknownReleaseAfter))
	if err != nil || len(ids) != len(l) {
		t.Fatalf("release %v %v", ids, err)
	}
	var left int
	var cost decimal.Decimal
	_ = x.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status<>'RECONCILED'), coalesce(sum(cost_usd),0) FROM model_calls`).Scan(&left, &cost)
	if left != 0 || !cost.IsZero() {
		t.Fatalf("after release: %d not reconciled, cost %s", left, cost)
	}
}

func TestEmptyAndTruncatedOutputAreFormatErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"empty":     func(w http.ResponseWriter, r *http.Request) { reply(w, "", "stop") },
		"truncated": func(w http.ResponseWriter, r *http.Request) { reply(w, `{"points":[{"te`, "length") },
	} {
		t.Run(name, func(t *testing.T) {
			x := newGW(t, h, Profile{})
			_, _, err := x.g.Complete(context.Background(), x.req())
			var fe *FormatError
			if !errors.As(err, &fe) {
				t.Fatalf("want format error, got %v", err)
			}
			// One repair attempt, both charged at actual usage (the vendor answered).
			l := x.ledger(t)
			if len(l) != 1+x.pol.ModelCall.FormatRepairs {
				t.Fatalf("ledger %v", l)
			}
			var n int
			_ = x.pool.QueryRow(context.Background(), `SELECT count(*) FROM model_calls WHERE cost_usd > 0`).Scan(&n)
			if n != len(l) {
				t.Fatalf("%d of %d failed calls charged", n, len(l))
			}
		})
	}
}

func TestRequestBodyAndProfileCaps(t *testing.T) {
	var got map[string]any
	x := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		reply(w, okSummary, "stop")
	}, Profile{Thinking: "disabled", Temperature: 0.3, MaxOutput: 200, ExtraBody: map[string]any{"top_p": 0.9, "model": "evil"}})
	if _, _, err := x.g.Complete(context.Background(), x.req()); err != nil {
		t.Fatal(err)
	}
	th, _ := got["thinking"].(map[string]any)
	if th["type"] != "disabled" || got["temperature"] != 0.3 || got["max_tokens"] != float64(200) || got["top_p"] != 0.9 || got["model"] != "flash" {
		t.Fatalf("request body %v", got)
	}
	var reserved decimal.Decimal
	_ = x.pool.QueryRow(context.Background(), `SELECT reserved_usd FROM model_calls`).Scan(&reserved)
	want, _ := prices().Reserve("flash", EstimateTokens(x.req().Messages), 200)
	if !reserved.Equal(want.Round(6)) {
		t.Fatalf("reservation %s, want %s (capped output)", reserved, want)
	}
	// Explicit thinking in ExtraBody wins over the profile switch.
	p := Profile{Thinking: "enabled", ExtraBody: map[string]any{"thinking": map[string]any{"type": "disabled", "budget": 1}}}
	b := p.body(nil, 10)
	if b["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("extra body thinking %v", b["thinking"])
	}
	// Changing switches changes the request hash, so cassettes and reuse do not mix configurations.
	if RequestHash(Profile{Thinking: "enabled"}, nil, 1, "s") == RequestHash(Profile{Thinking: "disabled"}, nil, 1, "s") {
		t.Fatal("thinking not part of request hash")
	}
}

func TestModelToolsNeverReachVendor(t *testing.T) {
	var calls atomic.Int32
	x := newGW(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(w, okSummary, "stop")
	}, Profile{ExtraBody: map[string]any{"tools": []any{map[string]any{"type": "deepseek_web_search"}}}})
	if _, _, err := x.g.Complete(context.Background(), x.req()); err == nil {
		t.Fatal("gateway accepted model tool configuration")
	}
	if calls.Load() != 0 || len(x.ledger(t)) != 0 {
		t.Fatal("tool request reached vendor or reserved a model call")
	}
	profile := x.g.Profiles["writer"]
	if _, err := (OpenAI{}).Do(context.Background(), Call{Profile: profile}); err == nil || calls.Load() != 0 {
		t.Fatal("direct adapter accepted model tool configuration")
	}
}

func TestPeakAndOffPeakPricing(t *testing.T) {
	p := prices()
	p.PeakWindowsUTC = append(p.PeakWindowsUTC, struct {
		Weekdays []int  `json:"weekdays"`
		From     string `json:"from"`
		To       string `json:"to"`
	}{Weekdays: []int{1, 2, 3, 4, 5}, From: "01:00", To: "09:00"})
	u := Usage{PromptTokens: 1_000_000, CacheHitTokens: 0, CacheMissTokens: 1_000_000, CompletionTokens: 1_000_000}
	mon := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC) // Monday 02:00 UTC
	peak, _ := p.Cost("flash", mon, u)
	off, _ := p.Cost("flash", mon.Add(8*time.Hour), u)
	sat, _ := p.Cost("flash", mon.AddDate(0, 0, 5), u)
	if !peak.Equal(decimal.RequireFromString("1.5")) || !off.Equal(decimal.RequireFromString("0.75")) || !sat.Equal(off) {
		t.Fatalf("peak %s off %s saturday %s", peak, off, sat)
	}
	if _, err := p.Cost("unknown", mon, u); err == nil {
		t.Fatal("missing price must fail")
	}
}

func TestRecordAdapterChargesOnlyMisses(t *testing.T) {
	dir := t.TempDir()
	var n atomic.Int32
	x := newGW(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); reply(w, okSummary, "stop") }, Profile{Adapter: "record"})
	x.g.Adapters["record"] = Replay{Dir: dir, Record: OpenAI{}}
	ctx := context.Background()
	_, m1, err := x.g.Complete(ctx, x.req())
	if err != nil {
		t.Fatal(err)
	}
	_, m2, err := x.g.Complete(ctx, x.req()) // new task: no reuse, but the cassette hits
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 || !m1.Cost.IsPositive() || !m2.Cost.IsZero() || m1.Adapter != "record" {
		t.Fatalf("hits %d cost %s/%s adapter %s", n.Load(), m1.Cost, m2.Cost, m1.Adapter)
	}
	// Truncated answers are not recorded, so a cassette never replays a failure.
	rp := Replay{Dir: dir, Record: fixed{Raw{Content: `{"points":[`, FinishReason: "length"}}}
	c := Call{Hash: "ffee00112233"}
	if _, err := rp.Do(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := (Replay{Dir: dir}).Do(ctx, c); err == nil {
		t.Fatal("truncated answer was recorded")
	}
}

type fixed struct{ r Raw }

func (fixed) Name() string                            { return "fixed" }
func (f fixed) Do(context.Context, Call) (Raw, error) { return f.r, nil }
