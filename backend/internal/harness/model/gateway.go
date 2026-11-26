package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/modelguard"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

type registeredSchema struct {
	compiled *jsonschema.Schema
	params   json.RawMessage
}

// Schemas holds the model contracts, including source JSON for tool declarations.
type Schemas map[string]registeredSchema

func LoadSchemas(dir string) (Schemas, error) {
	c := jsonschema.NewCompiler()
	out := Schemas{}
	for _, name := range []string{"section-output", "verify-output", "summary-output", "plan-parse-output", "conversation-answer-output", "conversation-review-output", "conversation-title-output", "conversation-window-tool", "conversation-depth-tool", "conversation-reference-tool"} {
		p, _ := filepath.Abs(filepath.Join(dir, name+".schema.json"))
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := c.AddResource(p, doc); err != nil {
			return nil, err
		}
		s, err := c.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", name, err)
		}
		out[name] = registeredSchema{compiled: s, params: raw}
	}
	return out, nil
}

type ToolSpec struct {
	Name        string
	Description string
	Schema      string // key in Schemas
}

// Request is one logical model call from a harness step.
type Request struct {
	WithSend func(context.Context, func() (Raw, error)) (Raw, error) // holds source rights only during the actual adapter call
	// WithReuse reads the cached result in the transaction holding the current source permissions.
	WithReuse    func(context.Context, func(pgx.Tx) error) error
	Workspace    uuid.UUID
	Task         *uuid.UUID // nil for planning calls that have no research task
	TaskEpoch    int64      // required for task-bound calls
	StepKey      string
	Profile      string
	Messages     []Message
	MaxOutput    int
	Schema       string // key in Schemas
	Tools        []ToolSpec
	Simulate     func() (any, error)
	Budget       policy.Budget
	EraseContent bool // keep accounting, omit model response and error text from the ledger
}

type Meta struct {
	CallID        uuid.UUID
	Adapter       string
	ObservedModel string
	Usage         Usage
	Cost          decimal.Decimal
	Reused        bool
	ToolCall      *ToolCall
}

// Gateway owns all model traffic. It is the only code allowed to talk to model vendors.
type Gateway struct {
	Pool       *pgxpool.Pool
	Profiles   map[string]Profile
	Adapters   map[string]Adapter
	Prices     *PriceTable
	Schemas    Schemas
	Policy     *policy.Policy
	DailyLimit decimal.Decimal
	sem        chan struct{}
	profSem    map[string]chan struct{}
	// wait is injectable so retry tests do not sleep, while production waits
	// remain interruptible when a task is cancelled or reaches its deadline.
	wait func(context.Context, time.Duration) error
	now  func() time.Time
}

func NewGateway(pool *pgxpool.Pool, profiles map[string]Profile, adapters map[string]Adapter, prices *PriceTable,
	schemas Schemas, pol *policy.Policy, daily decimal.Decimal) *Gateway {
	n := pol.Quota.PlatformModelConcurrency
	if n <= 0 {
		n = 8
	}
	ps := map[string]chan struct{}{}
	for name, p := range profiles {
		if p.Concurrency > 0 {
			ps[name] = make(chan struct{}, p.Concurrency)
		}
	}
	return &Gateway{Pool: pool, Profiles: profiles, Adapters: adapters, Prices: prices, Schemas: schemas, Policy: pol,
		DailyLimit: daily, sem: make(chan struct{}, n), profSem: ps, wait: waitContext, now: time.Now}
}

func waitContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// billable reports whether an adapter may reach a paid vendor.
func billable(adapter string) bool { return adapter == "openai" || adapter == "record" }

// acquire takes the platform slot and the profile slot (if the profile has a limit).
func (g *Gateway) acquire(ctx context.Context, profile string) (release func(), err error) {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ps := g.profSem[profile]
	if ps == nil {
		return func() { <-g.sem }, nil
	}
	select {
	case ps <- struct{}{}:
		return func() { <-ps; <-g.sem }, nil
	case <-ctx.Done():
		<-g.sem
		return nil, ctx.Err()
	}
}

// FormatError means the model output failed JSON/Schema validation after repairs.
type FormatError struct{ Msg string }

func (e *FormatError) Error() string { return "model output invalid: " + e.Msg }

// ErrStaleTask means the caller's task is gone, has ended, or has advanced to a different epoch.
var ErrStaleTask = errors.New("model call task is no longer current")

// Complete returns either validated reply JSON or a validated function call in Meta.
func (g *Gateway) Complete(ctx context.Context, r Request) (json.RawMessage, Meta, error) {
	prof, ok := g.Profiles[r.Profile]
	if !ok {
		return nil, Meta{}, fmt.Errorf("unknown model profile %q", r.Profile)
	}
	if err := modelguard.ValidateExtraBody(prof.ExtraBody); err != nil {
		return nil, Meta{}, err
	}
	ad, ok := g.Adapters[prof.Adapter]
	if !ok {
		return nil, Meta{}, fmt.Errorf("adapter %q not configured", prof.Adapter)
	}
	if r.EraseContent && prof.Adapter == "record" {
		return nil, Meta{}, errors.New("private requests cannot use the record adapter")
	}
	tools, toolSchemas, err := g.registerTools(r, prof)
	if err != nil {
		return nil, Meta{}, err
	}
	schema := g.Schemas[r.Schema].compiled
	if len(tools) > 0 && schema == nil {
		return nil, Meta{}, errors.New("tool requests require a reply schema")
	}
	toolTokens := 0
	if len(tools) > 0 {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return nil, Meta{}, err
		}
		toolTokens = utf8.RuneCount(encoded)
	}
	msgs := r.Messages
	r.MaxOutput = prof.EffectiveMaxOutput(r.MaxOutput)
	hash := RequestHash(prof, msgs, r.MaxOutput, r.Schema, tools...)
	var out json.RawMessage
	var meta Meta
	var reused bool
	if r.WithReuse != nil {
		err := r.WithReuse(ctx, func(tx pgx.Tx) error {
			out, meta, reused = g.reuse(ctx, tx, r, hash, schema)
			return nil
		})
		if err != nil {
			return nil, Meta{}, err
		}
	} else {
		out, meta, reused = g.reuse(ctx, g.Pool, r, hash, schema)
	}
	if reused {
		return out, meta, nil
	}
	transient, repairs := g.Policy.ModelCall.TransientRetries, g.Policy.ModelCall.FormatRepairs
	var lastErr error
	for attempt := 1; ; attempt++ {
		hash := RequestHash(prof, msgs, r.MaxOutput, r.Schema, tools...)
		callID, err := g.reserve(ctx, r, prof, ad.Name(), hash, attempt, EstimateTokens(msgs)+toolTokens)
		if err != nil {
			return nil, Meta{}, err
		}
		release, err := g.acquire(ctx, prof.Name)
		if err != nil {
			g.finish(ctx, callID, r.EraseContent, "FAILED", nil, Usage{}, decimal.Zero, "", "cancelled before send")
			return nil, Meta{}, err
		}

		sentSuccessfully := false
		send := func() (Raw, error) {
			raw, err := ad.Do(ctx, Call{Profile: prof, Messages: msgs, MaxOutput: r.MaxOutput, Tools: tools, Simulate: r.Simulate, Hash: hash})
			sentSuccessfully = err == nil
			return raw, err
		}
		var raw Raw
		if r.WithSend != nil {
			raw, err = r.WithSend(ctx, send)
		} else {
			raw, err = send()
		}
		if err != nil && sentSuccessfully {
			// A permission transaction may fail to commit after the paid
			// request completed. Preserve the reservation for reconciliation.
			err = &Error{Unknown: true, Msg: "source transaction failed after model response"}
		}
		release()
		cost := decimal.Zero
		if billable(ad.Name()) && !raw.Replayed && raw.Usage.PromptTokens > 0 {
			price, priceErr := g.Prices.Cost(prof.ModelID, g.now(), raw.Usage)
			if priceErr != nil {
				slog.ErrorContext(ctx, "pricing failed; charging reservation", "err", priceErr)
				cost = decimal.NewFromInt(-1)
			} else {
				cost = price
			}
		}
		if err != nil {
			var me *Error
			errors.As(err, &me)
			status := "FAILED"
			if me != nil && me.Unknown {
				status = "UNKNOWN" // keeps the reservation charged until reconciled
			}
			g.finish(ctx, callID, r.EraseContent, status, nil, raw.Usage, cost, "", err.Error())
			lastErr = err
			if me != nil && me.Transient && transient > 0 && ctx.Err() == nil {
				transient--
				wait := time.Duration(1<<(g.Policy.ModelCall.TransientRetries-transient)) * time.Second
				if me.RetryIn > wait {
					wait = me.RetryIn
				}
				if err := g.wait(ctx, wait); err != nil {
					return nil, Meta{}, err
				}
				continue
			}
			return nil, Meta{}, lastErr
		}
		out, tool, verr := validateCompletion(raw, schema, toolSchemas)
		if verr != nil {
			g.finish(ctx, callID, r.EraseContent, "FAILED", &raw, raw.Usage, cost, raw.ObservedModel, verr.Error())
			if repairs > 0 {
				repairs--
				if !r.EraseContent {
					msgs = append(append([]Message{}, msgs...), Message{Role: "assistant", Content: truncate(raw.Content, 4000)},
						Message{Role: "user", Content: "上一次输出不符合要求：" + truncate(verr.Error(), 300) + "。请只输出符合 JSON Schema 的 JSON 对象，不要输出其他内容。"})
				} else {
					hint := "上一次回复格式无效。请只返回符合回复格式的 JSON 对象，不要输出其他内容。"
					if len(tools) != 0 {
						hint = "上一次回复格式无效。只调用一个已声明的函数，参数须符合该函数的 JSON Schema，或只返回符合回复格式的 JSON 对象。"
					}
					if len(msgs) > 0 && msgs[0].Role == "system" {
						// Some model templates allow only one system message.
						msgs = append([]Message(nil), msgs...)
						msgs[0].Content += "\n\n" + hint
					} else {
						msgs = append([]Message{{Role: "system", Content: hint}}, msgs...)
					}
				}
				continue
			}
			return nil, Meta{}, &FormatError{verr.Error()}
		}
		g.finish(ctx, callID, r.EraseContent, "SUCCEEDED", &raw, raw.Usage, cost, raw.ObservedModel, "")
		return out, Meta{CallID: callID, Adapter: ad.Name(), ObservedModel: raw.ObservedModel, Usage: raw.Usage, Cost: cost, ToolCall: tool}, nil
	}
}

func (g *Gateway) registerTools(r Request, p Profile) ([]Tool, map[string]*jsonschema.Schema, error) {
	if len(r.Tools) == 0 {
		return nil, nil, nil
	}
	// Private conversation calls must not write model requests or responses to cassettes.
	if r.Profile != "router" || r.Task != nil || !r.EraseContent || p.Adapter == "record" {
		return nil, nil, errors.New("tool requests require a private router call")
	}
	tools := make([]Tool, 0, len(r.Tools))
	schemas := make(map[string]*jsonschema.Schema, len(r.Tools))
	for _, spec := range r.Tools {
		schema := g.Schemas[spec.Schema]
		if spec.Name == "" || schema.compiled == nil || schemas[spec.Name] != nil {
			return nil, nil, fmt.Errorf("invalid tool declaration %q", spec.Name)
		}
		tools = append(tools, Tool{Type: "function", Function: ToolFunction{Name: spec.Name, Description: spec.Description, Parameters: schema.params}})
		schemas[spec.Name] = schema.compiled
	}
	return tools, schemas, nil
}

func validateCompletion(raw Raw, reply *jsonschema.Schema, tools map[string]*jsonschema.Schema) (json.RawMessage, *ToolCall, error) {
	if len(raw.ToolCalls) == 0 && raw.FinishReason != "tool_calls" {
		if len(tools) != 0 && raw.FinishReason != "stop" {
			return nil, nil, errors.New("incomplete agent reply")
		}
		out, err := validate(raw, reply)
		return out, nil, err
	}
	if raw.FinishReason != "tool_calls" || len(raw.ToolCalls) != 1 {
		return nil, nil, errors.New("expected exactly one complete tool call")
	}
	call := raw.ToolCalls[0]
	schema := tools[call.Function.Name]
	if call.Type != "function" || call.ID == "" || schema == nil || len(call.Function.Arguments) > 4096 {
		return nil, nil, errors.New("invalid tool call")
	}
	args, err := validate(Raw{Content: call.Function.Arguments}, schema)
	if err != nil {
		return nil, nil, errors.New("invalid tool arguments")
	}
	call.Function.Arguments = string(args)
	return nil, &call, nil
}

func validate(raw Raw, schema *jsonschema.Schema) (json.RawMessage, error) {
	if raw.FinishReason == "length" {
		return nil, errors.New("output truncated (finish_reason=length)")
	}
	if raw.FinishReason == "tool_calls" || len(raw.ToolCalls) != 0 {
		return nil, errors.New("unexpected tool call")
	}
	content := strings.TrimSpace(raw.Content)
	if content == "" {
		return nil, errors.New("empty content")
	}
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if schema != nil {
		if err := schema.Validate(v); err != nil {
			return nil, fmt.Errorf("schema: %s", truncate(err.Error(), 400))
		}
	}
	return json.RawMessage(content), nil
}

// reuse returns an already-succeeded call for the same task step and request (River redelivery, worker restart).
type cachedQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const callableTask = `t.workspace_id=$1 AND t.id=$2 AND t.epoch=$3 AND t.status IN ('ANALYZING','ASSEMBLING')`

func (g *Gateway) reuse(ctx context.Context, q cachedQuery, r Request, hash string, schema *jsonschema.Schema) (json.RawMessage, Meta, bool) {
	if r.Task == nil {
		return nil, Meta{}, false
	}
	var raw Raw
	var m Meta
	err := q.QueryRow(ctx, `SELECT m.id,m.adapter,coalesce(m.observed_model,''),m.response FROM model_calls m
		JOIN research_tasks t ON t.workspace_id=m.workspace_id AND t.id=m.task_id
	WHERE `+callableTask+` AND m.epoch=t.epoch AND m.step_key=$4 AND m.request_hash=$5 AND m.status='SUCCEEDED' AND m.content_erased_at IS NULL
		ORDER BY m.attempt DESC LIMIT 1`, r.Workspace, *r.Task, r.TaskEpoch, r.StepKey, hash).Scan(&m.CallID, &m.Adapter, &m.ObservedModel, &raw)
	if err != nil {
		return nil, Meta{}, false
	}
	out, err := validate(raw, schema)
	if err != nil {
		return nil, Meta{}, false
	}
	m.Reused, m.Usage = true, raw.Usage
	return out, m, true
}

// reserve checks task and platform budgets and inserts a SENT row holding the worst-case reservation.
// All reservations are serialised by one advisory lock; MVP volume makes this cheap and keeps limits exact.
func (g *Gateway) reserve(ctx context.Context, r Request, p Profile, adapter, hash string, attempt, in int) (uuid.UUID, error) {
	reserve := decimal.Zero
	if billable(adapter) {
		var err error
		if reserve, err = g.Prices.Reserve(p.ModelID, in, r.MaxOutput); err != nil {
			return uuid.Nil, err
		}
	}
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7224001)`); err != nil {
			return err
		}
		if r.Task != nil {
			// Serialize reservation with task cancellation and deletion. The lock
			// is released before waiting for a model slot or calling an adapter.
			var current int
			err := tx.QueryRow(ctx, `SELECT 1 FROM research_tasks t WHERE `+callableTask+` FOR SHARE`, r.Workspace, *r.Task, r.TaskEpoch).Scan(&current)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStaleTask
			}
			if err != nil {
				return err
			}
		}
		spent := `coalesce(sum(CASE WHEN status IN ('SENT','UNKNOWN') OR cost_usd < 0 THEN reserved_usd ELSE coalesce(cost_usd,0) END),0)`
		var day decimal.Decimal
		if err := tx.QueryRow(ctx, `SELECT `+spent+` FROM model_calls WHERE sent_at >= date_trunc('day', now())`).Scan(&day); err != nil {
			return err
		}
		if day.Add(reserve).GreaterThan(g.DailyLimit) {
			return fmt.Errorf("%w: platform daily cost limit", ErrBudget)
		}
		if r.Task != nil {
			var calls, tin, tout int
			var cost decimal.Decimal
			err := tx.QueryRow(ctx, `SELECT count(*), coalesce(sum((usage->>'prompt_tokens')::int),0),
				coalesce(sum((usage->>'completion_tokens')::int + coalesce((usage->>'reasoning_tokens')::int,0)),0), `+spent+`
				FROM model_calls WHERE task_id=$1`, *r.Task).Scan(&calls, &tin, &tout, &cost)
			if err != nil {
				return err
			}
			b := r.Budget
			switch {
			case calls >= b.MaxModelCalls:
				return fmt.Errorf("%w: max model calls %d", ErrBudget, b.MaxModelCalls)
			case tin+in > b.InputTokens:
				return fmt.Errorf("%w: input tokens", ErrBudget)
			case tout+r.MaxOutput > b.OutputTokens:
				return fmt.Errorf("%w: output tokens", ErrBudget)
			case cost.Add(reserve).GreaterThan(b.CostLimitUSD):
				return fmt.Errorf("%w: task cost limit", ErrBudget)
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO model_calls(workspace_id,task_id,step_key,attempt,profile,adapter,model_id,request_hash,status,
			reserved_usd,price_table,epoch,content_erased_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'SENT',$9,$10,$11,CASE WHEN $12 THEN now() END) RETURNING id`,
			r.Workspace, r.Task, r.StepKey, attempt, p.Name, adapter, p.ModelID, hash, reserve, nullIf(g.priceVersion()),
			max(1, r.TaskEpoch), r.EraseContent).Scan(&id)
	})
	return id, err
}

func (g *Gateway) priceVersion() string {
	if g.Prices == nil {
		return ""
	}
	return g.Prices.Version
}

func nullIf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (g *Gateway) finish(ctx context.Context, id uuid.UUID, eraseContent bool, status string, raw *Raw, u Usage, cost decimal.Decimal, observed, errMsg string) {
	// The vendor may already have billed even if the task or worker stopped.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var c *decimal.Decimal
	if status != "UNKNOWN" {
		c = &cost
	}
	if eraseContent {
		raw, errMsg, observed = nil, "", ""
	}
	ub, _ := json.Marshal(u)
	_, err := g.Pool.Exec(ctx, `UPDATE model_calls SET status=$2, usage=$4, cost_usd=$5, observed_model=NULLIF($6,''), finished_at=now(),
		response=CASE WHEN content_erased_at IS NULL THEN $3::jsonb END,
		error=CASE WHEN content_erased_at IS NULL THEN NULLIF($7,'') END
		WHERE id=$1 AND status='SENT'`, id, status, raw, ub, c, observed, truncate(errMsg, 1000))
	if err != nil {
		slog.ErrorContext(ctx, "model call ledger update failed", "call", id, "err", err)
	}
}

// UnknownReleaseAfter is how long an UNKNOWN call keeps its reservation charged (HARNESS §9).
const UnknownReleaseAfter = 24 * time.Hour

// ReleaseUnknown stops charging reservations of UNKNOWN calls and interrupted
// conversation calls left in SENT before cutoff. Late vendor bills can still be reconciled.
// It runs outside workspace scope (platform job) and returns the released call IDs.
func (g *Gateway) ReleaseUnknown(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	rows, err := g.Pool.Query(ctx, `UPDATE model_calls SET status='RECONCILED', cost_usd=0, auto_released_at=now(), finished_at=coalesce(finished_at, now()),
		error=CASE WHEN content_erased_at IS NULL THEN left(coalesce(error,'') || ' [auto-released: no reconciliation within 24h]', 1000) END
		WHERE (status='UNKNOWN' OR (status='SENT' AND step_key='conversation_agent')) AND sent_at < $1 RETURNING id`, cutoff)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		slog.WarnContext(ctx, "released UNKNOWN model call reservations", "count", len(ids))
	}
	return ids, nil
}
