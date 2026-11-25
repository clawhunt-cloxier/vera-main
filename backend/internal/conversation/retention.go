package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

type retentionArgs struct{}

func (retentionArgs) Kind() string { return "conversation.retention" }

type retentionWorker struct {
	river.WorkerDefaults[retentionArgs]
	service Service
}

func registerRetention(workers *river.Workers, cfg *river.Config, engine *harness.Engine) {
	river.AddWorker(workers, &retentionWorker{service: Service{Engine: engine}})
	cfg.PeriodicJobs = append(cfg.PeriodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return retentionArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}

type expired struct{ workspace, conversation uuid.UUID }

func (w *retentionWorker) Work(ctx context.Context, _ *river.Job[retentionArgs]) error {
	s := w.service
	if _, err := s.Engine.Pool.Exec(ctx, "DELETE FROM conversation_quota_debits WHERE created_at<date_trunc('day', now())"); err != nil {
		return err
	}
	rows, err := s.Engine.Pool.Query(ctx, `SELECT workspace_id,id FROM conversations
		WHERE deleted_at IS NOT NULL OR expires_at<=now() ORDER BY coalesce(deleted_at,expires_at),id LIMIT 100`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (expired, error) {
		var value expired
		err := r.Scan(&value.workspace, &value.conversation)
		return value, err
	})
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if err := s.expire(ctx, item.workspace, item.conversation); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := s.clean(ctx, item); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s Service) clean(ctx context.Context, item expired) error {
	return db.InSystem(ctx, s.Engine.Pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM conversations WHERE id=$1 AND workspace_id=$2 AND deleted_at IS NOT NULL FOR UPDATE`,
			item.conversation, item.workspace).Scan(&id)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conversation_turns t
			JOIN research_tasks r ON r.id=t.task_id AND r.workspace_id=t.workspace_id
			WHERE t.conversation_id=$1 AND t.workspace_id=$2 AND r.status<>ALL($3::text[]))`, id, item.workspace, terminalStatuses).Scan(&active); err != nil {
			return err
		}
		if active {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT t.task_id FROM conversation_turns t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
			WHERE t.conversation_id=$1 AND t.workspace_id=$2 AND p.conversation_brief AND t.task_id IS NOT NULL`, id, item.workspace)
		if err != nil {
			return err
		}
		shortTasks, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_turns SET task_id=NULL,plan_id=NULL WHERE conversation_id=$1 AND workspace_id=$2`, id, item.workspace); err != nil {
			return err
		}
		for _, taskID := range shortTasks {
			if err := purgeShortTask(ctx, tx, item.workspace, taskID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM research_plans WHERE workspace_id=$1 AND consumed_task_id IS NULL
			AND conversation_turn_id IN (SELECT id FROM conversation_turns WHERE conversation_id=$2 AND workspace_id=$1)`, item.workspace, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM conversations WHERE id=$1 AND workspace_id=$2`, id, item.workspace)
		return err
	})
}

func purgeShortTask(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID) error {
	for _, statement := range []string{
		`INSERT INTO conversation_quota_debits(task_id,workspace_id,created_at) SELECT id,workspace_id,created_at
			FROM research_tasks WHERE workspace_id=$1 AND id=$2 AND created_at>=date_trunc('day', now()) ON CONFLICT DO NOTHING`,
		`DELETE FROM feedback WHERE workspace_id=$1 AND report_id IN (SELECT report_id FROM report_versions WHERE workspace_id=$1 AND task_id=$2)`,
		`DELETE FROM publications WHERE workspace_id=$1 AND report_id IN (SELECT report_id FROM report_versions WHERE workspace_id=$1 AND task_id=$2)`,
		`DELETE FROM observation_brief_snapshots WHERE workspace_id=$1 AND task_id=$2`,
		`DELETE FROM report_versions WHERE workspace_id=$1 AND task_id=$2`,
		`DELETE FROM claims WHERE workspace_id=$1 AND task_id=$2`,
		`DELETE FROM calculations WHERE workspace_id=$1 AND task_id=$2`,
		`DELETE FROM fact_sheets WHERE workspace_id=$1 AND task_id=$2`,
		`DELETE FROM task_steps WHERE workspace_id=$1 AND task_id=$2`,
		`DELETE FROM task_events WHERE workspace_id=$1 AND task_id=$2`,
		`UPDATE model_calls SET response=NULL,error=NULL WHERE workspace_id=$1 AND task_id=$2`,
	} {
		if _, err := tx.Exec(ctx, statement, ws, task); err != nil {
			return err
		}
	}
	var plan uuid.UUID
	if err := tx.QueryRow(ctx, `DELETE FROM research_tasks WHERE workspace_id=$1 AND id=$2 RETURNING plan_id`, ws, task).Scan(&plan); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM research_plans WHERE workspace_id=$1 AND id=$2 AND conversation_brief`, ws, plan)
	return err
}
