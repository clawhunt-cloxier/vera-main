package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

// Engine wires the data service, model gateway, policy and skills. One per process.
type Engine struct {
	Pool      *pgxpool.Pool
	Store     *dataservice.Store
	Gateway   *model.Gateway
	Policy    *policy.Policy
	Skills    map[string]*Skill
	Now       func() time.Time // data clock: fixture as_of in demo, wall clock with vendors
	Synthetic bool
	Basis     map[string]string
	River     *river.Client[pgx.Tx]
	Prompts   string // prompt set version, frozen into reports
	// RealResearch is VERA_REAL_RESEARCH_ENABLED: required before plans and tasks run on real data or real models.
	RealResearch bool
}

const PromptVersion = "1.1.0"

// Terminal and active statuses.
var terminal = []string{"COMPLETED", "ABSTAINED", "BUDGET_EXHAUSTED", "CANCELLED", "BLOCKED", "FAILED"}
var active = []string{"COLLECTING", "ANALYZING", "ASSEMBLING", "DELIVERING", "CANCEL_REQUESTED"}

func IsTerminal(s string) bool { return slices.Contains(terminal, s) }

// ---- plans ----

type PlanSection struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Required bool    `json:"required"`
	Avail    string  `json:"availability"`
	Reason   *string `json:"reason"`
}

type Plan struct {
	ID           uuid.UUID             `json:"id"`
	Hash         string                `json:"plan_hash"`
	SkillID      string                `json:"skill_id"`
	SkillVersion string                `json:"skill_version"`
	Subject      dataservice.EntityRow `json:"-"`
	Params       Params                `json:"params"`
	AsOf         time.Time             `json:"as_of"`
	Sections     []PlanSection         `json:"sections"`
	Executable   bool                  `json:"executable"`
	ReasonCode   *string               `json:"reason_code"`
	Notes        []string              `json:"notes"`
	P95Seconds   int                   `json:"p95_seconds"`
	QuotaCost    int                   `json:"quota_cost"`
	ExpiresAt    time.Time             `json:"expires_at"`
}

const planTTL = 30 * time.Minute

func planHash(skill, version string, subject uuid.UUID, p Params, asOf time.Time, secs []PlanSection) string {
	b, _ := json.Marshal(struct {
		S, V string
		Sub  uuid.UUID
		P    Params
		A    string
		Secs []PlanSection
	}{skill, version, subject, p, asOf.UTC().Format(time.RFC3339), secs})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// BuildPlan computes availability and persists the plan for the workspace.
func (e *Engine) BuildPlan(ctx context.Context, ws uuid.UUID, skillID string, subject dataservice.EntityRow, raw map[string]any) (*Plan, error) {
	if !e.Synthetic && !e.RealResearch {
		return nil, errs.Unavailable("REAL_RESEARCH_DISABLED", "真实研究尚未开启")
	}
	sk, ok := e.Skills[skillID]
	if !ok {
		return nil, errs.Unprocessable("UNKNOWN_SKILL", "不支持的研究技能")
	}
	if !slices.Contains(sk.Subjects, subject.Kind) {
		return nil, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", sk.Title+"不支持该研究对象类型")
	}
	if raw == nil {
		raw = map[string]any{}
	}
	p, err := sk.Validate(subject, raw)
	if err != nil {
		return nil, err
	}
	asOf := e.Now()
	av, notes, err := sk.Plan(ctx, e, subject, &p, asOf)
	if err != nil {
		return nil, err
	}
	plan := &Plan{ID: uuid.New(), SkillID: sk.ID, SkillVersion: sk.Version, Subject: subject, Params: p, AsOf: asOf,
		Executable: true, Notes: notes, ExpiresAt: time.Now().Add(planTTL)}
	writers := 0
	plan.Sections = append(plan.Sections, PlanSection{ID: "S1", Title: "核心要点", Required: true, Avail: "available"})
	for _, s := range sk.Sections(p) {
		a := av[s.ID]
		if a.State == "" {
			a = avail()
		}
		plan.Sections = append(plan.Sections, PlanSection{ID: s.ID, Title: s.Title, Required: s.Required, Avail: a.State, Reason: a.Reason})
		if s.Required && a.State == "unavailable" && plan.Executable {
			plan.Executable = false
			code := "REQUIRED_SECTION_UNAVAILABLE"
			plan.ReasonCode = &code
		}
		if a.State != "unavailable" {
			writers++
		}
	}
	b := e.Policy.Budgets[sk.ID]
	plan.P95Seconds = min(b.DeadlineSeconds, 60+writers*25)
	plan.QuotaCost = 1
	plan.Hash = planHash(sk.ID, sk.Version, subject.ID, p, asOf, plan.Sections)
	params, _ := json.Marshal(p)
	secs, _ := json.Marshal(plan.Sections)
	err = db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO research_plans(id,workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,reason_code,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, plan.ID, ws, sk.ID, sk.Version, subject.ID, params, secs, asOf, plan.Hash,
			plan.Executable, plan.ReasonCode, plan.ExpiresAt)
		return err
	})
	return plan, err
}

// LoadPlan reads a stored plan.
func (e *Engine) LoadPlan(ctx context.Context, ws, id uuid.UUID) (*Plan, error) {
	var p Plan
	var params, secs []byte
	var subject uuid.UUID
	err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, plan_hash, skill_id, skill_version, subject_id, params, sections, as_of, executable, reason_code, expires_at
			FROM research_plans WHERE id=$1`, id).Scan(&p.ID, &p.Hash, &p.SkillID, &p.SkillVersion, &subject, &params, &secs, &p.AsOf,
			&p.Executable, &p.ReasonCode, &p.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.NotFound()
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(params, &p.Params)
	_ = json.Unmarshal(secs, &p.Sections)
	ents, err := e.Store.Entities(ctx, []uuid.UUID{subject})
	if err != nil {
		return nil, err
	}
	p.Subject = ents[subject]
	sk := e.Skills[p.SkillID]
	b := e.Policy.Budgets[p.SkillID]
	w := 0
	for _, s := range p.Sections {
		if s.Avail != "unavailable" {
			w++
		}
	}
	p.P95Seconds, p.QuotaCost = min(b.DeadlineSeconds, 60+w*25), 1
	_ = sk
	return &p, nil
}

// ---- task creation ----

type RunArgs struct {
	TaskID    uuid.UUID `json:"task_id" river:"unique"`
	Workspace uuid.UUID `json:"workspace_id"`
	Epoch     int64     `json:"epoch" river:"unique"`
}

func (RunArgs) Kind() string { return "research.run" }
func (RunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "research", MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

type DeadlineArgs struct {
	TaskID    uuid.UUID `json:"task_id" river:"unique"`
	Workspace uuid.UUID `json:"workspace_id"`
}

func (DeadlineArgs) Kind() string { return "research.deadline" }
func (DeadlineArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "research", MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

type CancelArgs struct {
	TaskID    uuid.UUID `json:"task_id" river:"unique"`
	Workspace uuid.UUID `json:"workspace_id"`
	Epoch     int64     `json:"epoch" river:"unique"`
}

func (CancelArgs) Kind() string { return "research.cancel" }
func (CancelArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "research", MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// Quota is the caller's remaining allowance (Me.quota).
type Quota struct {
	ActiveRemaining    int `json:"active_remaining"`
	QueuedRemaining    int `json:"queued_remaining"`
	DailyRemaining     int `json:"daily_remaining"`
	DeepDailyRemaining int `json:"deep_research_daily_remaining"`
}

func (e *Engine) quota(ctx context.Context, tx pgx.Tx) (Quota, error) {
	q := e.Policy.Quota
	// Per-workspace override set by `vera admin quota set` (only the user_* limits).
	var override []byte
	_ = tx.QueryRow(ctx, `SELECT quota_override FROM workspaces WHERE id = current_setting('app.workspace_id', true)::uuid`).Scan(&override)
	if len(override) > 0 {
		_ = json.Unmarshal(override, &q)
	}
	var activeN, queued, daily, deep int
	err := tx.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = ANY($1)), count(*) FILTER (WHERE status='QUEUED'),
		count(*) FILTER (WHERE created_at >= date_trunc('day', now())),
		count(*) FILTER (WHERE created_at >= date_trunc('day', now()) AND skill_id='deep_research')
		FROM research_tasks`, active).Scan(&activeN, &queued, &daily, &deep)
	return Quota{max(0, q.UserActiveTasks-activeN), max(0, q.UserQueuedTasks-queued), max(0, q.UserDailyTasks-daily),
		max(0, q.UserDailyDeepResearch-deep)}, err
}

func (e *Engine) Quota(ctx context.Context, ws uuid.UUID) (Quota, error) {
	var q Quota
	err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		var err error
		q, err = e.quota(ctx, tx)
		return err
	})
	return q, err
}

// KillSwitch reports whether new research is stopped platform-wide.
func KillSwitch(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) bool {
	var on bool
	_ = q.QueryRow(ctx, `SELECT coalesce((SELECT (value->>'on')::boolean FROM platform_settings WHERE key='kill_switch'), false)`).Scan(&on)
	return on
}

// CreateTask confirms a plan: verifies hash/expiry, reserves quota and enqueues the run in one transaction.
func (e *Engine) CreateTask(ctx context.Context, ws uuid.UUID, planID uuid.UUID, hash string, disabled []string) (uuid.UUID, error) {
	if !e.Synthetic && !e.RealResearch {
		return uuid.Nil, errs.Unavailable("REAL_RESEARCH_DISABLED", "真实研究尚未开启")
	}
	plan, err := e.LoadPlan(ctx, ws, planID)
	if err != nil {
		return uuid.Nil, err
	}
	sk := e.Skills[plan.SkillID]
	switch {
	case plan.Hash != hash:
		return uuid.Nil, errs.Conflict("PLAN_CHANGED", "研究计划已变更，请重新确认")
	case time.Now().After(plan.ExpiresAt):
		return uuid.Nil, errs.Conflict("PLAN_EXPIRED", "研究计划已过期，请重新生成")
	case !plan.Executable:
		return uuid.Nil, errs.Unprocessable("PLAN_NOT_EXECUTABLE", "该研究计划不可执行")
	case sk == nil || sk.Version != plan.SkillVersion:
		return uuid.Nil, errs.Conflict("SKILL_VERSION_CHANGED", "技能版本已更新，请重新生成计划")
	}
	for _, d := range disabled {
		i := slices.IndexFunc(plan.Sections, func(s PlanSection) bool { return s.ID == d })
		if i < 0 || plan.Sections[i].Required {
			return uuid.Nil, errs.Unprocessable("INVALID_DISABLED_SECTION", "只能取消可选章节："+d)
		}
	}
	budget := e.Policy.Budgets[sk.ID]
	var taskID uuid.UUID
	err = db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		if KillSwitch(ctx, tx) {
			return errs.Unavailable("RESEARCH_PAUSED", "平台暂停接受新的研究任务")
		}
		// Serialise quota checks per workspace.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7224002))`, ws.String()); err != nil {
			return err
		}
		var consumed *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT consumed_task_id FROM research_plans WHERE id=$1 FOR UPDATE`, planID).Scan(&consumed); err != nil {
			return err
		}
		if consumed != nil {
			taskID = *consumed // idempotent confirm
			return nil
		}
		q, err := e.quota(ctx, tx)
		if err != nil {
			return err
		}
		var inflight int
		if err := tx.QueryRow(ctx, `SELECT platform_inflight_tasks()`).Scan(&inflight); err != nil {
			return err
		}
		switch {
		case q.DailyRemaining <= 0:
			return errs.TooMany("DAILY_QUOTA_EXCEEDED", "今日研究次数已用完")
		case sk.ID == "deep_research" && q.DeepDailyRemaining <= 0:
			return errs.TooMany("DEEP_RESEARCH_QUOTA_EXCEEDED", "今日深度研究次数已用完")
		case q.QueuedRemaining <= 0:
			return errs.TooMany("QUEUE_FULL", "排队中的任务过多，请稍后再试")
		case inflight >= e.Policy.Quota.PlatformInFlightTasks:
			return errs.TooMany("PLATFORM_BUSY", "平台繁忙，请稍后再试")
		}
		params, _ := json.Marshal(plan.Params)
		secs := []PlanSection{}
		for _, s := range plan.Sections {
			if slices.Contains(disabled, s.ID) {
				s.Avail = "unavailable"
				r := "用户取消"
				s.Reason = &r
			}
			secs = append(secs, s)
		}
		sb, _ := json.Marshal(secs)
		bb, _ := json.Marshal(budget)
		deadline := time.Now().Add(time.Duration(budget.DeadlineSeconds) * time.Second)
		if err := tx.QueryRow(ctx, `INSERT INTO research_tasks(workspace_id,plan_id,skill_id,skill_version,subject_id,params,sections,as_of,status,deadline,budget)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'QUEUED',$9,$10) RETURNING id`, ws, planID, sk.ID, sk.Version, plan.Subject.ID, params, sb, plan.AsOf,
			deadline, bb).Scan(&taskID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE research_plans SET consumed_task_id=$2 WHERE id=$1`, planID, taskID); err != nil {
			return err
		}
		if err := e.insertSteps(ctx, tx, ws, taskID, 1, sk, plan.Params, secs); err != nil {
			return err
		}
		if err := event(ctx, tx, ws, taskID, 1, "CREATED", map[string]any{"plan_hash": plan.Hash}); err != nil {
			return err
		}
		if _, err := e.River.InsertTx(ctx, tx, RunArgs{TaskID: taskID, Workspace: ws, Epoch: 1}, nil); err != nil {
			return err
		}
		_, err = e.River.InsertTx(ctx, tx, DeadlineArgs{TaskID: taskID, Workspace: ws}, &river.InsertOpts{ScheduledAt: deadline.Add(30 * time.Second)})
		return err
	})
	return taskID, err
}

func (e *Engine) insertSteps(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID, epoch int64, sk *Skill, p Params, secs []PlanSection) error {
	type st struct{ key, label, status string }
	steps := []st{{"collect", "取数并冻结数据快照", "PENDING"}, {"compute", "计算指标与事实表", "PENDING"}}
	for _, s := range sk.Sections(p) {
		status := "PENDING"
		if i := slices.IndexFunc(secs, func(x PlanSection) bool { return x.ID == s.ID }); i >= 0 && secs[i].Avail == "unavailable" {
			status = "SKIPPED"
		}
		steps = append(steps, st{"write:" + s.ID, "撰写 " + s.ID + " " + s.Title, status}, st{"verify:" + s.ID, "核验 " + s.ID + " " + s.Title, status})
	}
	steps = append(steps, st{"summary", "生成核心要点", "PENDING"}, st{"verify:summary", "核验核心要点", "PENDING"}, st{"render", "组装报告", "PENDING"}, st{"deliver", "交付", "PENDING"})
	for _, s := range steps {
		if _, err := tx.Exec(ctx, `INSERT INTO task_steps(workspace_id,task_id,epoch,step_key,label,status) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT (task_id,epoch,step_key) DO NOTHING`, ws, task, epoch, s.key, s.label, s.status); err != nil {
			return err
		}
	}
	return nil
}

func event(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID, epoch int64, typ string, payload map[string]any) error {
	b, _ := json.Marshal(payload)
	_, err := tx.Exec(ctx, `INSERT INTO task_events(task_id,workspace_id,seq,epoch,event_type,payload)
		VALUES($1,$2,(SELECT coalesce(max(seq),0)+1 FROM task_events WHERE task_id=$1),$3,$4,$5)`, task, ws, epoch, typ, b)
	return err
}

// ---- running ----

type taskRow struct {
	ID, Workspace, Subject uuid.UUID
	Skill, Version, Status string
	Epoch                  int64
	Params                 Params
	Sections               []PlanSection
	AsOf, Deadline         time.Time
	Budget                 policy.Budget
	Snapshot               *string
	PlanHash               string
	SnapshotMode           string
}

func (e *Engine) loadTask(ctx context.Context, ws, id uuid.UUID) (*taskRow, error) {
	var t taskRow
	var params, secs, budget []byte
	err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT t.id, t.workspace_id, t.subject_id, t.skill_id, t.skill_version, t.status, t.epoch, t.params, t.sections,
			t.as_of, t.deadline, t.budget, t.snapshot_hash, p.plan_hash, t.snapshot_mode FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id WHERE t.id=$1`, id).
			Scan(&t.ID, &t.Workspace, &t.Subject, &t.Skill, &t.Version, &t.Status, &t.Epoch, &params, &secs, &t.AsOf, &t.Deadline, &budget,
				&t.Snapshot, &t.PlanHash, &t.SnapshotMode)
	})
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(params, &t.Params)
	_ = json.Unmarshal(secs, &t.Sections)
	_ = json.Unmarshal(budget, &t.Budget)
	return &t, nil
}

var errStale = errors.New("task epoch or status changed")

// ErrSnapshotModeUnsupported prevents a legacy worker from using preview time
// as a collect_latest cutoff. A newer worker must implement the full protocol.
var ErrSnapshotModeUnsupported = errors.New("task snapshot mode is not supported by this worker")

// transition moves a task between states only if epoch and from-status still match.
func (e *Engine) transition(ctx context.Context, t *taskRow, from []string, to string, code, msg string) error {
	return db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE research_tasks SET status=$4, reason_code=NULLIF($5,''), reason_message=NULLIF($6,''), revision=revision+1, updated_at=now()
			WHERE id=$1 AND epoch=$2 AND status = ANY($3)`, t.ID, t.Epoch, from, to, code, msg)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errStale
		}
		t.Status = to
		return event(ctx, tx, t.Workspace, t.ID, t.Epoch, to, map[string]any{"reason_code": code})
	})
}

// ErrStepBusy means another worker holds the lease of a step this run needs; the run should be retried later.
var ErrStepBusy = errors.New("step is running in another worker")

// Step lease timing (HARNESS §10.2). Variables so tests can change them.
var (
	stepLease     = 2 * time.Minute
	stepHeartbeat = 30 * time.Second
)

// readStep returns a succeeded step's stored output.
func readStep[T any](ctx context.Context, e *Engine, t *taskRow, key string) (out T, done bool, err error) {
	var status string
	var stored []byte
	err = db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, output FROM task_steps WHERE task_id=$1 AND epoch=$2 AND step_key=$3`, t.ID, t.Epoch, key).Scan(&status, &stored)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil || status != "SUCCEEDED" || stored == nil {
		return out, false, err
	}
	err = json.Unmarshal(stored, &out)
	return out, true, err
}

// step runs fn once per (task, epoch, key); a succeeded step returns its stored output (River redelivery is safe).
// A worker claims the step with a lease (attempt + lease_until), renews it while fn runs, and writes the result only
// while it still holds that attempt. A step leased by a live worker yields ErrStepBusy; a step whose lease expired
// (crashed worker) is taken over. If the lease was lost meanwhile, the other worker's stored result wins.
func step[T any](ctx context.Context, e *Engine, t *taskRow, key string, fn func() (T, error)) (T, error) {
	if out, done, err := readStep[T](ctx, e, t, key); done || err != nil {
		return out, err
	}
	var zero T
	if err := e.stillCurrent(ctx, t); err != nil {
		return zero, err
	}
	var attempt int
	leaseMs := stepLease.Milliseconds()
	err := db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO task_steps(workspace_id,task_id,epoch,step_key,label,status,started_at,attempt,lease_until)
			VALUES($1,$2,$3,$4,$4,'RUNNING',now(),1,now() + $5 * interval '1 millisecond')
			ON CONFLICT (task_id,epoch,step_key) DO UPDATE SET status='RUNNING', started_at=now(), error=NULL, finished_at=NULL,
				attempt=task_steps.attempt+1, lease_until=excluded.lease_until
			WHERE task_steps.status IN ('PENDING','FAILED','SKIPPED')
			   OR (task_steps.status='RUNNING' AND (task_steps.lease_until IS NULL OR task_steps.lease_until < now()))
			RETURNING attempt`, t.Workspace, t.ID, t.Epoch, key, leaseMs).Scan(&attempt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if out, done, err := readStep[T](ctx, e, t, key); done || err != nil {
			return out, err
		}
		return zero, ErrStepBusy
	}
	if err != nil {
		return zero, err
	}
	hbCtx, stopHB := context.WithCancel(context.WithoutCancel(ctx))
	every := stepHeartbeat
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-tick.C:
				_ = db.InWorkspace(hbCtx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
					_, err := tx.Exec(hbCtx, `UPDATE task_steps SET lease_until=now() + $5 * interval '1 millisecond'
						WHERE task_id=$1 AND epoch=$2 AND step_key=$3 AND attempt=$4 AND status='RUNNING'`, t.ID, t.Epoch, key, attempt, leaseMs)
					return err
				})
			}
		}
	}()
	out, ferr := fn()
	stopHB()
	final := context.WithoutCancel(ctx)
	if ferr != nil {
		_ = db.InWorkspace(final, e.Pool, t.Workspace, func(tx pgx.Tx) error {
			_, err := tx.Exec(final, `UPDATE task_steps SET status='FAILED', error=$5, finished_at=now(), lease_until=NULL
				WHERE task_id=$1 AND epoch=$2 AND step_key=$3 AND attempt=$4 AND status='RUNNING'`, t.ID, t.Epoch, key, attempt, ferr.Error())
			return err
		})
		return out, ferr
	}
	b, _ := json.Marshal(out)
	h := sha256.Sum256(b)
	var held bool
	err = db.InWorkspace(final, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		tag, err := tx.Exec(final, `UPDATE task_steps SET status='SUCCEEDED', output=$5, output_hash=$6, finished_at=now(), lease_until=NULL
			WHERE task_id=$1 AND epoch=$2 AND step_key=$3 AND attempt=$4 AND status='RUNNING'`, t.ID, t.Epoch, key, attempt, b, hex.EncodeToString(h[:]))
		held = err == nil && tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return zero, err
	}
	if !held {
		// Lease lost: another worker took the step over. Use its result if it finished, otherwise let it finish.
		slog.WarnContext(ctx, "step lease lost; discarding result", "task", t.ID, "step", key, "attempt", attempt)
		if stored, done, err := readStep[T](final, e, t, key); done || err != nil {
			return stored, err
		}
		return zero, ErrStepBusy
	}
	return out, nil
}

func (e *Engine) skipStep(ctx context.Context, t *taskRow, key, reason string) {
	_ = db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE task_steps SET status='SKIPPED', error=$4, finished_at=now() WHERE task_id=$1 AND epoch=$2 AND step_key=$3 AND status<>'SUCCEEDED'`,
			t.ID, t.Epoch, key, reason)
		return err
	})
}

func (e *Engine) stillCurrent(ctx context.Context, t *taskRow) error {
	var epoch int64
	var status string
	err := db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT epoch, status FROM research_tasks WHERE id=$1`, t.ID).Scan(&epoch, &status)
	})
	if err != nil {
		return err
	}
	if epoch != t.Epoch || status == "CANCEL_REQUESTED" || IsTerminal(status) {
		return errStale
	}
	return nil
}

// abstainError carries a terminal outcome decided inside the pipeline.
type abstainError struct{ status, code, msg string }

func (a *abstainError) Error() string { return a.code + ": " + a.msg }

func abstain(code, msg string) error { return &abstainError{"ABSTAINED", code, msg} }

type collectOut struct {
	Snapshot string      `json:"snapshot"`
	Members  []uuid.UUID `json:"members"`
	Peers    []uuid.UUID `json:"peers"`
	Extra    []uuid.UUID `json:"extra"` // catalog entities without records (e.g. the industry itself)
}

// SectionResult is the verified outcome of one writer section.
type SectionResult struct {
	ID       string        `json:"id"`
	Status   string        `json:"status"` // ok | limited | omitted
	Claims   []check.Claim `json:"claims"`
	Removed  []Removed     `json:"removed"`
	Note     string        `json:"note"`
	Verified bool          `json:"verified"`
}

type Removed struct {
	Claim  check.Claim `json:"claim"`
	Rule   string      `json:"rule"`
	Reason string      `json:"reason"`
}

// Run advances a task to a terminal state. Safe to call repeatedly (ledger + epoch checks).
func (e *Engine) Run(ctx context.Context, ws, id uuid.UUID, epoch int64) error {
	t, err := e.loadTask(ctx, ws, id)
	if err != nil {
		return err
	}
	if t.Epoch != epoch || IsTerminal(t.Status) || t.Status == "CANCEL_REQUESTED" {
		return nil
	}
	if t.SnapshotMode != "fixed_legacy" {
		return ErrSnapshotModeUnsupported
	}
	sk := e.Skills[t.Skill]
	if sk == nil || sk.Version != t.Version {
		return e.finish(ctx, t, "FAILED", "SKILL_VERSION_MISSING", "技能版本不可用")
	}
	// Leave 30s before the deadline for assembly without model calls.
	runCtx, cancel := context.WithDeadline(ctx, t.Deadline.Add(-30*time.Second))
	defer cancel()
	go e.watchCancel(runCtx, cancel, t)
	err = e.pipeline(runCtx, ctx, t, sk)
	var ab *abstainError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errStale):
		return nil
	case errors.As(err, &ab):
		return e.finish(ctx, t, ab.status, ab.code, ab.msg)
	case errors.Is(err, model.ErrBudget):
		return e.finish(ctx, t, "BUDGET_EXHAUSTED", "BUDGET_EXHAUSTED", "研究预算已用尽，必需章节未完成")
	case ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
		return e.finish(ctx, t, "ABSTAINED", "DEADLINE_EXCEEDED", "超过研究时限，必需章节未完成")
	}
	return err // River retries; ledger makes retries cheap
}

func (e *Engine) finish(ctx context.Context, t *taskRow, status, code, msg string) error {
	err := e.transition(context.WithoutCancel(ctx), t, append([]string{"QUEUED"}, active[:4]...), status, code, msg)
	if errors.Is(err, errStale) {
		return nil
	}
	slog.InfoContext(ctx, "task finished", "task", t.ID, "status", status, "code", code)
	return err
}

func (e *Engine) watchCancel(ctx context.Context, cancel context.CancelFunc, t *taskRow) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := e.stillCurrent(ctx, t); errors.Is(err, errStale) {
				cancel()
				return
			}
		}
	}
}

func (e *Engine) pipeline(ctx, parent context.Context, t *taskRow, sk *Skill) error {
	if t.Status == "QUEUED" {
		if err := e.transition(ctx, t, []string{"QUEUED"}, "COLLECTING", "", ""); err != nil {
			return err
		}
	}
	subjects, err := e.Store.Entities(ctx, []uuid.UUID{t.Subject})
	if err != nil {
		return err
	}
	subject := subjects[t.Subject]
	co, err := step(ctx, e, t, "collect", func() (collectOut, error) {
		b, err := sk.Collect(ctx, e, subject, t.Params, t.AsOf)
		if err != nil {
			return collectOut{}, err
		}
		h, err := e.Store.Freeze(ctx, t.AsOf, b.IDs)
		if err != nil {
			return collectOut{}, err
		}
		out := collectOut{Snapshot: h, Members: b.Members, Peers: b.Peers}
		for id, en := range b.Entities {
			if en.Kind != "stock" {
				out.Extra = append(out.Extra, id)
			}
		}
		return out, db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE research_tasks SET snapshot_hash=$2 WHERE id=$1 AND epoch=$3`, t.ID, h, t.Epoch)
			return err
		})
	})
	if err != nil {
		return err
	}
	sheet, err := step(ctx, e, t, "compute", func() (*facts.Sheet, error) {
		b, err := e.bundleFromSnapshot(ctx, subject, co, t.AsOf)
		if err != nil {
			return nil, err
		}
		s := facts.NewSheet(t.ID, t.Epoch)
		bl := &builder{S: s, B: b, basis: e.Basis}
		sk.Compute(bl, t.Params)
		s.Finalize()
		if s.Suspicious > 0 {
			s.Limit(fmt.Sprintf("有 %d 段原文或新闻含疑似指令或评级内容，已标记为可疑并排除在写作材料之外。", s.Suspicious))
		}
		return s, e.saveCalcs(ctx, t, s)
	})
	if err != nil {
		return err
	}
	secs := sk.Sections(t.Params)
	for _, s := range secs {
		if e.sectionState(t, s.ID) == "unavailable" {
			continue
		}
		if len(allowed(sheet, s)) == 0 && s.Required {
			return abstain("REQUIRED_DATA_MISSING", s.Title+"缺少可用事实")
		}
	}
	if t.Status == "COLLECTING" {
		if err := e.transition(ctx, t, []string{"COLLECTING"}, "ANALYZING", "", ""); err != nil {
			return err
		}
	}
	results := make([]SectionResult, len(secs))
	sem := make(chan struct{}, max(1, e.Policy.Quota.PerTaskModelConcurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, s := range secs {
		if e.sectionState(t, s.ID) == "unavailable" {
			results[i] = SectionResult{ID: s.ID, Status: "omitted", Note: e.sectionReason(t, s.ID)}
			continue
		}
		if len(allowed(sheet, s)) == 0 {
			note := "数据截止时点前没有可用于本章节的事实"
			e.skipStep(ctx, t, "write:"+s.ID, note)
			e.skipStep(ctx, t, "verify:"+s.ID, note)
			results[i] = SectionResult{ID: s.ID, Status: "omitted", Note: note}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r, err := e.runSection(ctx, t, sk, subject, sheet, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && errors.Is(err, ErrStepBusy) {
				// Another worker is running this section: retry the whole run later, never omit the section for it.
				if firstErr == nil {
					firstErr = err
				}
				err, r = nil, SectionResult{ID: s.ID, Status: "omitted"}
			}
			if err != nil {
				if errors.Is(err, errStale) || (!s.Required && !errors.Is(err, context.Canceled)) {
					r = SectionResult{ID: s.ID, Status: "omitted", Note: "本章节未能完成：" + shortErr(err)}
					if errors.Is(err, errStale) && firstErr == nil {
						firstErr = err
					}
				} else if firstErr == nil {
					firstErr = err
				}
			}
			results[i] = r
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	for i, s := range secs {
		r := results[i]
		if s.Required && (r.Status == "omitted" || len(r.Claims) == 0) {
			return abstain("REQUIRED_SECTION_UNSUPPORTED", fmt.Sprintf("必需章节 %s %s 无有效结论：%s", s.ID, s.Title, r.Note))
		}
	}
	if err := e.transition(parent, t, []string{"ANALYZING"}, "ASSEMBLING", "", ""); err != nil && !errors.Is(err, errStale) {
		return err
	} else if errors.Is(err, errStale) && t.Status != "ASSEMBLING" {
		return err
	}
	return e.assemble(ctx, parent, t, sk, subject, sheet, secs, results)
}

func shortErr(err error) string {
	var fe *model.FormatError
	switch {
	case errors.As(err, &fe):
		return "模型输出格式无效"
	case errors.Is(err, model.ErrBudget):
		return "预算不足"
	case errors.Is(err, context.DeadlineExceeded):
		return "超过时限"
	}
	return "处理失败"
}

func (e *Engine) sectionState(t *taskRow, id string) string {
	for _, s := range t.Sections {
		if s.ID == id {
			return s.Avail
		}
	}
	return "available"
}

func (e *Engine) sectionReason(t *taskRow, id string) string {
	for _, s := range t.Sections {
		if s.ID == id && s.Reason != nil {
			return *s.Reason
		}
	}
	return ""
}

// bundleFromSnapshot rebuilds the bundle exactly from the frozen record set and the frozen member/peer lists.
func (e *Engine) bundleFromSnapshot(ctx context.Context, subject dataservice.EntityRow, co collectOut, expectedAsOf time.Time) (*Bundle, error) {
	asOf, recs, err := e.Store.Snapshot(ctx, co.Snapshot)
	if err != nil {
		return nil, err
	}
	if !asOf.Equal(expectedAsOf.UTC().Truncate(time.Microsecond)) {
		return nil, errors.New("snapshot cutoff does not match research task")
	}
	ids := map[uuid.UUID]bool{subject.ID: true}
	for _, r := range recs {
		if r.EntityID != nil {
			ids[*r.EntityID] = true
		}
	}
	for _, l := range [][]uuid.UUID{co.Members, co.Peers, co.Extra} {
		for _, id := range l {
			ids[id] = true
		}
	}
	var list []uuid.UUID
	for id := range ids {
		list = append(list, id)
	}
	ents, err := e.Store.Entities(ctx, list)
	if err != nil {
		return nil, err
	}
	b := newBundle(asOf, subject, ents, recs)
	b.Members, b.Peers = co.Members, co.Peers
	return b, nil
}

func (e *Engine) saveCalcs(ctx context.Context, t *taskRow, s *facts.Sheet) error {
	return db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		for _, c := range s.Calcs {
			in, _ := json.Marshal(c.Inputs)
			if _, err := tx.Exec(ctx, `INSERT INTO calculations(id,workspace_id,task_id,epoch,formula_id,formula_version,inputs,result,unit,applicability,reason)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, c.ID, t.Workspace, t.ID, t.Epoch, c.FormulaID, c.FormulaVer,
				in, c.Result, c.Unit, c.Applicability, c.Reason); err != nil {
				return err
			}
		}
		return nil
	})
}
