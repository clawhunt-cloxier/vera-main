package harness

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

type runWorker struct {
	river.WorkerDefaults[RunArgs]
	e *Engine
}

func (w *runWorker) Timeout(*river.Job[RunArgs]) time.Duration { return 20 * time.Minute }

func (w *runWorker) Work(ctx context.Context, j *river.Job[RunArgs]) error {
	err := w.e.Run(ctx, j.Args.Workspace, j.Args.TaskID, j.Args.Epoch)
	if err != nil && j.Attempt >= j.MaxAttempts {
		slog.ErrorContext(ctx, "research run exhausted retries", "task", j.Args.TaskID, "err", err)
		t, lerr := w.e.loadTask(ctx, j.Args.Workspace, j.Args.TaskID)
		if lerr == nil && t.Epoch == j.Args.Epoch {
			return w.e.finish(ctx, t, "FAILED", "INTERNAL_ERROR", "研究执行失败，已停止重试")
		}
	}
	return err
}

type deadlineWorker struct {
	river.WorkerDefaults[DeadlineArgs]
	e *Engine
}

// Work is the safety net when a worker died: the run job normally finishes before the deadline itself.
func (w *deadlineWorker) Work(ctx context.Context, j *river.Job[DeadlineArgs]) error {
	t, err := w.e.loadTask(ctx, j.Args.Workspace, j.Args.TaskID)
	if err != nil || IsTerminal(t.Status) || t.Status == "CANCEL_REQUESTED" {
		return err
	}
	if time.Now().Before(t.Deadline) {
		return river.JobSnooze(time.Until(t.Deadline) + 5*time.Second)
	}
	return w.e.finish(ctx, t, "ABSTAINED", "DEADLINE_EXCEEDED", "超过研究时限，必需章节未完成")
}

type cancelWorker struct {
	river.WorkerDefaults[CancelArgs]
	e *Engine
}

// Work waits for in-flight steps of earlier epochs to stop (or 60s), then marks the task CANCELLED.
func (w *cancelWorker) Work(ctx context.Context, j *river.Job[CancelArgs]) error {
	var running int
	var requested time.Time
	err := db.InWorkspace(ctx, w.e.Pool, j.Args.Workspace, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM task_steps WHERE task_id=$1 AND epoch < $2 AND status='RUNNING'`,
			j.Args.TaskID, j.Args.Epoch).Scan(&running); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT updated_at FROM research_tasks WHERE id=$1`, j.Args.TaskID).Scan(&requested)
	})
	if err != nil {
		return err
	}
	if running > 0 && time.Since(requested) < time.Minute {
		return river.JobSnooze(2 * time.Second)
	}
	return db.InWorkspace(ctx, w.e.Pool, j.Args.Workspace, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE research_tasks SET status='CANCELLED', revision=revision+1, updated_at=now()
			WHERE id=$1 AND epoch=$2 AND status='CANCEL_REQUESTED'`, j.Args.TaskID, j.Args.Epoch)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE task_steps SET status='SKIPPED', error='任务已取消' WHERE task_id=$1 AND status IN ('PENDING','RUNNING')`,
			j.Args.TaskID); err != nil {
			return err
		}
		return event(ctx, tx, j.Args.Workspace, j.Args.TaskID, j.Args.Epoch, "CANCELLED", nil)
	})
}

// NewRiver builds the River client. workers=true starts queue processing (vera-worker); the API uses insert-only.
func (e *Engine) NewRiver(pool *pgxpool.Pool, work bool, concurrency int) (*river.Client[pgx.Tx], error) {
	ws := river.NewWorkers()
	river.AddWorker(ws, &runWorker{e: e})
	river.AddWorker(ws, &deadlineWorker{e: e})
	river.AddWorker(ws, &cancelWorker{e: e})
	cfg := &river.Config{Workers: ws, Logger: slog.Default()}
	if work {
		cfg.Queues = map[string]river.QueueConfig{"research": {MaxWorkers: max(1, concurrency)}}
	}
	c, err := river.NewClient(riverpgxv5.New(pool), cfg)
	if err != nil {
		return nil, err
	}
	e.River = c
	return c, nil
}

// CancelTask persists a cancel request (HARNESS §10.1). Returns the new revision.
func (e *Engine) CancelTask(ctx context.Context, ws, id uuid.UUID, expected int64) (int64, string, error) {
	var rev int64
	var status string
	err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT status, revision, epoch FROM research_tasks WHERE id=$1 FOR UPDATE`, id).Scan(&status, &rev, &epoch); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errs.NotFound()
			}
			return err
		}
		switch {
		case status == "COMPLETED":
			return errs.Conflict("ALREADY_COMPLETED", "任务已完成，无法取消")
		case status == "CANCEL_REQUESTED" || status == "CANCELLED":
			return nil
		case IsTerminal(status):
			return errs.Conflict("ALREADY_TERMINAL", "任务已结束")
		case rev != expected:
			return errs.Conflict("REVISION_MISMATCH", "任务状态已变化，请刷新后重试").With("revision", rev)
		}
		next := "CANCEL_REQUESTED"
		if status == "QUEUED" {
			next = "CANCELLED"
		}
		if err := tx.QueryRow(ctx, `UPDATE research_tasks SET status=$2, epoch=epoch+1, revision=revision+1, reason_code='USER_CANCELLED', updated_at=now()
			WHERE id=$1 RETURNING revision, epoch`, id, next).Scan(&rev, &epoch); err != nil {
			return err
		}
		status = next
		if err := event(ctx, tx, ws, id, epoch, next, nil); err != nil {
			return err
		}
		if next == "CANCELLED" {
			_, err := tx.Exec(ctx, `UPDATE task_steps SET status='SKIPPED', error='任务已取消' WHERE task_id=$1 AND status IN ('PENDING','RUNNING')`, id)
			return err
		}
		_, err := e.River.InsertTx(ctx, tx, CancelArgs{TaskID: id, Workspace: ws, Epoch: epoch}, nil)
		return err
	})
	return rev, status, err
}
