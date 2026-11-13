// Package model is the model gateway (HARNESS §9): OpenAI-compatible adapter, deterministic sim adapter,
// replay adapter, call ledger, budget reservation, retries and Go-side JSON Schema validation.
package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CacheHitTokens   int `json:"prompt_cache_hit_tokens"`
	CacheMissTokens  int `json:"prompt_cache_miss_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// Raw is one adapter response before validation.
type Raw struct {
	Content       string `json:"content"`
	FinishReason  string `json:"finish_reason"`
	ObservedModel string `json:"model"`
	Usage         Usage  `json:"usage"`
	// Replayed marks a response served from a cassette: no vendor was called, so nothing is charged.
	Replayed bool `json:"-"`
}

// Call is what an adapter receives.
type Call struct {
	Profile   Profile
	Messages  []Message
	MaxOutput int
	// Simulate produces the structured output for the sim adapter. It must be deterministic in its inputs.
	Simulate func() (any, error)
	Hash     string
}

type Adapter interface {
	Name() string
	Do(ctx context.Context, c Call) (Raw, error)
}

// Profile configures one purpose (writer / verifier / router).
type Profile struct {
	Name        string
	Adapter     string // openai | record | replay | sim
	BaseURL     string
	ModelID     string
	APIKey      string
	PriceTable  string
	Temperature float64
	// Thinking is "", "enabled" or "disabled". "" sends nothing (vendor default); otherwise the request carries
	// {"thinking":{"type":Thinking}}. An explicit "thinking" key in ExtraBody wins.
	Thinking  string
	ExtraBody map[string]any // vendor-specific request fields
	Timeout   time.Duration  // per HTTP request; 0 means 90s
	// MaxOutput caps every call's output tokens on this profile; 0 keeps the per-call limit.
	MaxOutput int
	// Concurrency limits in-flight calls on this profile (in addition to the platform limit); 0 means no extra limit.
	Concurrency int
}

// EffectiveMaxOutput applies the profile cap to a call's requested output limit.
func (p Profile) EffectiveMaxOutput(requested int) int {
	if p.MaxOutput > 0 && (requested <= 0 || p.MaxOutput < requested) {
		return p.MaxOutput
	}
	return requested
}

// body builds the chat/completions request body. Gateway-owned keys are set last so ExtraBody cannot override them.
func (p Profile) body(msgs []Message, maxOut int) map[string]any {
	body := map[string]any{}
	if p.Thinking != "" {
		body["thinking"] = map[string]string{"type": p.Thinking}
	}
	for k, v := range p.ExtraBody {
		body[k] = v
	}
	body["model"], body["messages"], body["stream"] = p.ModelID, msgs, false
	body["response_format"] = map[string]string{"type": "json_object"}
	body["max_tokens"], body["temperature"] = maxOut, p.Temperature
	return body
}

// Error classes drive retry policy.
type Error struct {
	Transient bool
	Unknown   bool // request may have been processed (timeout after send)
	Status    int
	RetryIn   time.Duration
	Msg       string
}

func (e *Error) Error() string { return e.Msg }

// ---- openai-compatible adapter ----

type OpenAI struct{ HTTP *http.Client }

func (OpenAI) Name() string { return "openai" }

func (a OpenAI) Do(ctx context.Context, c Call) (Raw, error) {
	b, _ := json.Marshal(c.Profile.body(c.Messages, c.MaxOutput))
	timeout := c.Profile.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Profile.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return Raw{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Profile.APIKey)
	req.Header.Set("Content-Type", "application/json")
	hc := a.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// The request may have reached the vendor: charge the reservation until reconciled.
		return Raw{}, &Error{Transient: true, Unknown: true, Msg: "model request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Raw{}, &Error{Transient: true, Unknown: true, Msg: "read model response: " + err.Error()}
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		e := &Error{Transient: true, Status: resp.StatusCode, Msg: fmt.Sprintf("model http %d", resp.StatusCode)}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			e.RetryIn = time.Duration(s) * time.Second
		}
		return Raw{}, e
	}
	if resp.StatusCode >= 400 {
		return Raw{}, &Error{Status: resp.StatusCode, Msg: fmt.Sprintf("model http %d: %s", resp.StatusCode, truncate(string(rb), 300))}
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Usage
			Details struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || len(out.Choices) == 0 {
		// The vendor answered (and may bill usage) but the body is unusable; not retried as transient.
		return Raw{}, &Error{Msg: "unparseable model response"}
	}
	u := out.Usage.Usage
	u.ReasoningTokens = out.Usage.Details.Reasoning
	return Raw{Content: out.Choices[0].Message.Content, FinishReason: out.Choices[0].FinishReason, ObservedModel: out.Model, Usage: u}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- sim adapter: deterministic, synthetic, refused in production ----

type Sim struct{}

func (Sim) Name() string { return "sim" }

func (Sim) Do(_ context.Context, c Call) (Raw, error) {
	if c.Simulate == nil {
		return Raw{}, &Error{Msg: "sim adapter: no simulator for this step"}
	}
	v, err := c.Simulate()
	if err != nil {
		return Raw{}, &Error{Msg: "sim: " + err.Error()}
	}
	b, _ := json.Marshal(v)
	in := 0
	for _, m := range c.Messages {
		in += utf8.RuneCountInString(m.Content)
	}
	return Raw{Content: string(b), FinishReason: "stop", ObservedModel: "vera-sim-1",
		Usage: Usage{PromptTokens: in, CacheMissTokens: in, CompletionTokens: utf8.RuneCount(b)}}, nil
}

// ---- replay adapter: cassettes keyed by request hash ----

type Replay struct {
	Dir    string
	Record Adapter // when set, misses are forwarded and recorded (development only)
}

// Name is "record" when misses are forwarded to a vendor (billable), "replay" otherwise.
func (r Replay) Name() string {
	if r.Record != nil {
		return "record"
	}
	return "replay"
}

func (r Replay) path(hash string) string { return filepath.Join(r.Dir, hash[:2], hash+".json") }

func (r Replay) Do(ctx context.Context, c Call) (Raw, error) {
	b, err := os.ReadFile(r.path(c.Hash))
	if err == nil {
		var raw Raw
		if err := json.Unmarshal(b, &raw); err != nil {
			return Raw{}, &Error{Msg: "corrupt cassette " + c.Hash}
		}
		raw.Replayed = true
		return raw, nil
	}
	if r.Record == nil {
		return Raw{}, &Error{Msg: "replay cassette missing for request " + c.Hash}
	}
	raw, err := r.Record.Do(ctx, c)
	if err != nil {
		return raw, err
	}
	// Only complete answers are worth replaying; truncated output would replay a failure forever.
	if raw.FinishReason == "stop" && strings.TrimSpace(raw.Content) != "" {
		if err := os.MkdirAll(filepath.Dir(r.path(c.Hash)), 0o755); err == nil {
			out, _ := json.MarshalIndent(raw, "", "  ")
			tmp := r.path(c.Hash) + ".tmp"
			if os.WriteFile(tmp, out, 0o644) == nil {
				_ = os.Rename(tmp, r.path(c.Hash))
			}
		}
	}
	return raw, nil
}

// RequestHash identifies a request independent of attempt; it keys replay cassettes and call reuse.
// Everything sent to the vendor that can change the answer (thinking, temperature, extra body) is included.
func RequestHash(p Profile, msgs []Message, maxOut int, schemaID string) string {
	h := sha256.New()
	extra, _ := json.Marshal(p.ExtraBody) // map keys are sorted, so this is stable
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\x00%s\x00%g\x00%s\x00", p.Name, p.ModelID, maxOut, schemaID, p.Thinking, p.Temperature, extra)
	for _, m := range msgs {
		fmt.Fprintf(h, "%s\x00%s\x00", m.Role, m.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---- prices ----

type Price struct {
	CacheHitInput  decimal.Decimal `json:"cache_hit_input"`
	CacheMissInput decimal.Decimal `json:"cache_miss_input"`
	Output         decimal.Decimal `json:"output"`
}

type PriceTable struct {
	Version  string `json:"version"`
	Currency string `json:"currency"`
	Note     string `json:"note"`
	Models   map[string]struct {
		Peak    Price `json:"peak"`
		OffPeak Price `json:"off_peak"`
	} `json:"models"`
	PeakWindowsUTC []struct {
		Weekdays []int  `json:"weekdays"` // 1=Mon … 7=Sun
		From     string `json:"from"`     // "01:00"
		To       string `json:"to"`
	} `json:"peak_windows_utc"`
}

func LoadPrices(path string) (*PriceTable, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p PriceTable
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("price table: %w", err)
	}
	return &p, nil
}

var perMillion = decimal.NewFromInt(1_000_000)

func (t *PriceTable) price(model string, at time.Time) (Price, bool) {
	m, ok := t.Models[model]
	if !ok {
		return Price{}, false
	}
	u := at.UTC()
	wd := int(u.Weekday())
	if wd == 0 {
		wd = 7
	}
	hm := u.Format("15:04")
	for _, w := range t.PeakWindowsUTC {
		for _, d := range w.Weekdays {
			if d == wd && hm >= w.From && hm < w.To {
				return m.Peak, true
			}
		}
	}
	return m.OffPeak, true
}

// Cost computes the charge for usage. Missing cache split counts all input as cache miss.
func (t *PriceTable) Cost(model string, at time.Time, u Usage) (decimal.Decimal, error) {
	p, ok := t.price(model, at)
	if !ok {
		return decimal.Zero, fmt.Errorf("no price for model %q in table %s", model, t.Version)
	}
	hit, miss := u.CacheHitTokens, u.CacheMissTokens
	if hit+miss == 0 {
		miss = u.PromptTokens
	}
	c := p.CacheHitInput.Mul(decimal.NewFromInt(int64(hit))).
		Add(p.CacheMissInput.Mul(decimal.NewFromInt(int64(miss)))).
		Add(p.Output.Mul(decimal.NewFromInt(int64(u.CompletionTokens + u.ReasoningTokens))))
	return c.Div(perMillion), nil
}

// Reserve estimates the worst-case charge before sending: all input uncached at peak, full max output.
func (t *PriceTable) Reserve(model string, inputTokens, maxOut int) (decimal.Decimal, error) {
	m, ok := t.Models[model]
	if !ok {
		return decimal.Zero, fmt.Errorf("no price for model %q", model)
	}
	return m.Peak.CacheMissInput.Mul(decimal.NewFromInt(int64(inputTokens))).
		Add(m.Peak.Output.Mul(decimal.NewFromInt(int64(maxOut)))).Div(perMillion), nil
}

// EstimateTokens is a conservative token estimate for mixed Chinese/JSON text (≈1 token per rune).
func EstimateTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += utf8.RuneCountInString(m.Content) + 8
	}
	return n
}

var ErrBudget = errors.New("model budget exhausted")
