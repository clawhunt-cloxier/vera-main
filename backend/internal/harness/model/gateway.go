package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/modelguard"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

// Schemas holds the compiled model output schemas from contracts/model.
type Schemas map[string]*jsonschema.Schema

func LoadSchemas(dir string) (Schemas, error) {
	c := jsonschema.NewCompiler()
	out := Schemas{}
	for _, name := range []string{"section-output", "verify-output", "summary-output", "plan-parse-output"} {
		p, _ := filepath.Abs(filepath.Join(dir, name+".schema.json"))
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(f)
		f.Close()
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
		out[name] = s
	}
	return out, nil
}

// Request is one logical model call from a harness step.
type Request struct {
	WithSend  func(context.Context, func() (Raw, error)) (Raw, error) // holds source rights only during the actual adapter call
	Workspace uuid.UUID
	Task      *uuid.UUID
	StepKey   string
	Profile   string
	Messages  []Message
	MaxOutput int
	Schema    string // key in Schemas
	Simulate  func() (any, error)
	Budget    policy.Budget
}

type Meta struct {
	CallID        uuid.UUID
	Adapter       string
	ObservedModel string
	Usage         Usage
	Cost          decimal.Decimal
	Reused        bool
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

// Complete runs the call with reuse, budget reservation, retries and validation. It returns the validated JSON.
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
	schema := g.Schemas[r.Schema]
	msgs := r.Messages
	r.MaxOutput = prof.EffectiveMaxOutput(r.MaxOutput)
	if out, meta, ok := g.reuse(ctx, r, RequestHash(prof, msgs, r.MaxOutput, r.Schema), schema); ok {
		return out, meta, nil
	}
	transient, repairs := g.Policy.ModelCall.TransientRetries, g.Policy.ModelCall.FormatRepairs
	var lastErr error
	for attempt := 1; ; attempt++ {
		hash := RequestHash(prof, msgs, r.MaxOutput, r.Schema)
		callID, err := g.reserve(ctx, r, prof, ad.Name(), hash, attempt, msgs)
		if err != nil {
			return nil, Meta{}, err
		}
		release, err := g.acquire(ctx, prof.Name)
		if err != nil {
			g.finish(context.WithoutCancel(ctx), callID, "FAILED", nil, Usage{}, decimal.Zero, "", "cancelled before send")
			return nil, Meta{}, err
		}

		sentSuccessfully := false
		send := func() (Raw, error) {
			raw, err := ad.Do(ctx, Call{Profile: prof, Messages: msgs, MaxOutput: r.MaxOutput, Simulate: r.Simulate, Hash: hash})
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
		if err != nil {
			var me *Error
			errors.As(err, &me)
			status := "FAILED"
			if me != nil && me.Unknown {
				status = "UNKNOWN" // keeps the reservation charged until reconciled
			}
			g.finish(context.WithoutCancel(ctx), callID, status, nil, Usage{}, decimal.Zero, "", err.Error())
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
		cost := decimal.Zero
		if billable(ad.Name()) && !raw.Replayed {
			if cost, err = g.Prices.Cost(prof.ModelID, g.now(), raw.Usage); err != nil {
				slog.ErrorContext(ctx, "pricing failed; charging reservation", "err", err)
				cost = decimal.NewFromInt(-1)
			}
		}
		out, verr := validate(raw, schema)
		if verr != nil {
			g.finish(ctx, callID, "FAILED", &raw, raw.Usage, cost, raw.ObservedModel, verr.Error())
			if repairs > 0 {
				repairs--
				msgs = append(append([]Message{}, msgs...), Message{Role: "assistant", Content: truncate(raw.Content, 4000)},
					Message{Role: "user", Content: "上一次输出不符合要求：" + truncate(verr.Error(), 300) + "。请只输出符合 JSON Schema 的 JSON 对象，不要输出其他内容。"})
				continue
			}
			return nil, Meta{}, &FormatError{verr.Error()}
		}
		g.finish(ctx, callID, "SUCCEEDED", &raw, raw.Usage, cost, raw.ObservedModel, "")
		return out, Meta{CallID: callID, Adapter: ad.Name(), ObservedModel: raw.ObservedModel, Usage: raw.Usage, Cost: cost}, nil
	}
}

func validate(raw Raw, schema *jsonschema.Schema) (json.RawMessage, error) {
	if raw.FinishReason == "length" {
		return nil, errors.New("output truncated (finish_reason=length)")
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
func (g *Gateway) reuse(ctx context.Context, r Request, hash string, schema *jsonschema.Schema) (json.RawMessage, Meta, bool) {
	if r.Task == nil {
		return nil, Meta{}, false
	}
	var raw Raw
	var m Meta
	err := g.Pool.QueryRow(ctx, `SELECT id, adapter, coalesce(observed_model,''), response FROM model_calls
		WHERE task_id=$1 AND step_key=$2 AND request_hash=$3 AND status='SUCCEEDED' ORDER BY attempt DESC LIMIT 1`,
		*r.Task, r.StepKey, hash).Scan(&m.CallID, &m.Adapter, &m.ObservedModel, &raw)
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
func (g *Gateway) reserve(ctx context.Context, r Request, p Profile, adapter, hash string, attempt int, msgs []Message) (uuid.UUID, error) {
	in := EstimateTokens(msgs)
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
			reserved_usd,price_table) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'SENT',$9,$10) RETURNING id`,
			r.Workspace, r.Task, r.StepKey, attempt, p.Name, adapter, p.ModelID, hash, reserve, nullIf(g.priceVersion())).Scan(&id)
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

func (g *Gateway) finish(ctx context.Context, id uuid.UUID, status string, raw *Raw, u Usage, cost decimal.Decimal, observed, errMsg string) {
	var c *decimal.Decimal
	if status != "UNKNOWN" {
		c = &cost
	}
	ub, _ := json.Marshal(u)
	_, err := g.Pool.Exec(ctx, `UPDATE model_calls SET status=$2, response=$3, usage=$4, cost_usd=$5, observed_model=NULLIF($6,''),
		error=NULLIF($7,''), finished_at=now() WHERE id=$1`, id, status, raw, ub, c, observed, truncate(errMsg, 1000))
	if err != nil {
		slog.ErrorContext(ctx, "model call ledger update failed", "call", id, "err", err)
	}
}

// UnknownReleaseAfter is how long an UNKNOWN call keeps its reservation charged (HARNESS §9).
const UnknownReleaseAfter = 24 * time.Hour

// ReleaseUnknown stops charging reservations of UNKNOWN calls sent before cutoff: they become RECONCILED with
// cost 0 and a note. A later vendor bill showing a charge must be recorded with `vera admin usage reconcile`.
// It runs outside workspace scope (platform job) and returns the released call IDs.
func (g *Gateway) ReleaseUnknown(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	rows, err := g.Pool.Query(ctx, `UPDATE model_calls SET status='RECONCILED', cost_usd=0, finished_at=coalesce(finished_at, now()),
		error=left(coalesce(error,'') || ' [auto-released: no reconciliation within 24h]', 1000)
		WHERE status='UNKNOWN' AND sent_at < $1 RETURNING id`, cutoff)
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
