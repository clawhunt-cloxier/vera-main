package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

func status(t *testing.T, e *Engine, ws, id uuid.UUID) (string, int64) {
	var s string
	var rev int64
	if err := db.InWorkspace(context.Background(), e.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT status, revision FROM research_tasks WHERE id=$1`, id).Scan(&s, &rev)
	}); err != nil {
		t.Fatal(err)
	}
	return s, rev
}

func plan(t *testing.T, e *Engine, ws uuid.UUID, skill, code string) *Plan {
	sub, err := e.Store.EntityByCode(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.BuildPlan(context.Background(), ws, skill, sub, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLifecycle(t *testing.T) {
	e, pool := testEngine(t)
	ctx := context.Background()
	ws := workspace(t, pool)

	// Plan hash and disabled-section guards.
	p := plan(t, e, ws, "stock_analysis", "DM0001.DEMO")
	if _, err := e.CreateTask(ctx, ws, p.ID, "0000", nil); errs.As(err).Code != "PLAN_CHANGED" {
		t.Fatalf("hash mismatch: %v", err)
	}
	if _, err := e.CreateTask(ctx, ws, p.ID, p.Hash, []string{"S3"}); errs.As(err).Code != "INVALID_DISABLED_SECTION" {
		t.Fatalf("required section disabled: %v", err)
	}
	id, err := e.CreateTask(ctx, ws, p.ID, p.Hash, []string{"S9"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.CreateTask(ctx, ws, p.ID, p.Hash, []string{"S9"})
	if err != nil || again != id {
		t.Fatalf("confirming twice must return the same task: %v %v", again, err)
	}
	// Workspace isolation: another workspace cannot see the plan.
	other := workspace(t, pool)
	if _, err := e.LoadPlan(ctx, other, p.ID); errs.As(err).Code != "NOT_FOUND" {
		t.Fatalf("cross-workspace plan visible: %v", err)
	}
	if err := e.Run(ctx, ws, id, 1); err != nil {
		t.Fatal(err)
	}
	if s, _ := status(t, e, ws, id); s != "COMPLETED" {
		t.Fatalf("status %s", s)
	}
	// Re-running a completed task is a no-op; no new model calls.
	var calls int
	pool.QueryRow(ctx, `SELECT count(*) FROM model_calls WHERE task_id=$1`, id).Scan(&calls)
	if err := e.Run(ctx, ws, id, 1); err != nil {
		t.Fatal(err)
	}
	var calls2 int
	pool.QueryRow(ctx, `SELECT count(*) FROM model_calls WHERE task_id=$1`, id).Scan(&calls2)
	if calls != calls2 {
		t.Fatalf("rerun made model calls %d→%d", calls, calls2)
	}
	// Disabled optional section is omitted with the user reason.
	var doc Document
	db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, id).Scan(&doc)
	})
	for _, s := range doc.Sections {
		if s.ID == "S9" && (s.Status != "omitted" || s.Note == nil || *s.Note != "用户取消") {
			t.Fatalf("S9 should be omitted by user: %+v", s)
		}
	}
	// Adversarial texts are excluded from what models see and from the report.
	var suspicious int
	db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (output->>'suspicious')::int FROM task_steps WHERE task_id=$1 AND step_key='compute'`, id).Scan(&suspicious)
	})
	if suspicious < 1 {
		t.Fatalf("ADV-01 paragraph not flagged suspicious")
	}
	// The compute output in the step ledger is what a redelivered run reloads: it must keep evidence bindings.
	var stored facts.Sheet
	db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT output FROM task_steps WHERE task_id=$1 AND step_key='compute'`, id).Scan(&stored)
	})
	if len(stored.Facts) == 0 {
		t.Fatal("no facts in stored compute output")
	}
	for _, f := range stored.Facts {
		if len(f.Evidence) == 0 || (f.Kind == facts.KindComputed && f.CalcID == nil) || (f.Display != "" && f.Display != "不适用" && f.Kind != facts.KindText && f.Value == nil && f.Unit != "") {
			t.Fatalf("stored fact lost bindings: %+v", f)
		}
	}
	// Cancelling a completed task conflicts.
	_, rev := status(t, e, ws, id)
	if _, _, err := e.CancelTask(ctx, ws, id, rev); errs.As(err).Code != "ALREADY_COMPLETED" {
		t.Fatalf("cancel completed: %v", err)
	}

	// Cancel while queued → CANCELLED immediately; a stale run does nothing.
	p2 := plan(t, e, ws, "capital_flow", "DM0001.DEMO")
	id2, err := e.CreateTask(ctx, ws, p2.ID, p2.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, rev = status(t, e, ws, id2)
	if _, _, err := e.CancelTask(ctx, ws, id2, rev+5); errs.As(err).Code != "REVISION_MISMATCH" {
		t.Fatalf("stale revision: %v", err)
	}
	if _, st, err := e.CancelTask(ctx, ws, id2, rev); err != nil || st != "CANCELLED" {
		t.Fatalf("cancel queued: %s %v", st, err)
	}
	if err := e.Run(ctx, ws, id2, 1); err != nil {
		t.Fatal(err)
	}
	if s, _ := status(t, e, ws, id2); s != "CANCELLED" {
		t.Fatalf("old epoch ran after cancel: %s", s)
	}

	// Cancel mid-run: request while COLLECTING; the old epoch must not deliver.
	p3 := plan(t, e, ws, "capital_flow", "DM0004.DEMO")
	id3, _ := e.CreateTask(ctx, ws, p3.ID, p3.Hash, nil)
	tr, _ := e.loadTask(ctx, ws, id3)
	if err := e.transition(ctx, tr, []string{"QUEUED"}, "COLLECTING", "", ""); err != nil {
		t.Fatal(err)
	}
	_, rev = status(t, e, ws, id3)
	if _, st, err := e.CancelTask(ctx, ws, id3, rev); err != nil || st != "CANCEL_REQUESTED" {
		t.Fatalf("cancel running: %s %v", st, err)
	}
	if err := e.Run(ctx, ws, id3, 1); err != nil {
		t.Fatal(err)
	}
	var reports int
	db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM report_versions WHERE task_id=$1`, id3).Scan(&reports)
	})
	if reports != 0 {
		t.Fatal("cancelled epoch delivered a report")
	}
	w := &cancelWorker{e: e}
	_ = w
}

func TestQuota(t *testing.T) {
	e, pool := testEngine(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	e.Policy.Quota.UserQueuedTasks = 1
	p := plan(t, e, ws, "capital_flow", "DM0001.DEMO")
	if _, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil); err != nil {
		t.Fatal(err)
	}
	p2 := plan(t, e, ws, "capital_flow", "DM0004.DEMO")
	if _, err := e.CreateTask(ctx, ws, p2.ID, p2.Hash, nil); errs.As(err).Code != "QUEUE_FULL" {
		t.Fatalf("want QUEUE_FULL, got %v", err)
	}
	pool.Exec(ctx, `INSERT INTO platform_settings(key,value) VALUES('kill_switch','{"on":true}')`)
	if _, err := e.CreateTask(ctx, ws, p2.ID, p2.Hash, nil); errs.As(err).Code != "RESEARCH_PAUSED" {
		t.Fatalf("want RESEARCH_PAUSED, got %v", err)
	}
}

func TestRiverWorkerRunsTask(t *testing.T) {
	e, pool := testEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := e.NewRiver(pool, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	ws := workspace(t, pool)
	p := plan(t, e, ws, "capital_flow", "DM0001.DEMO")
	id, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		if s, _ := status(t, e, ws, id); IsTerminal(s) {
			if s != "COMPLETED" {
				t.Fatalf("worker finished with %s", s)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal(errors.New("worker did not finish the task"))
}
