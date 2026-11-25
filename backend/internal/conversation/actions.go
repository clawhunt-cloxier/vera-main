package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

func (t Turn) Scope() Scope            { return t.Intent.Scope }
func (t Turn) Candidates() []uuid.UUID { return slices.Clone(t.Intent.Candidates) }
func (t Turn) Reference() *uuid.UUID   { return t.Intent.Reference }
func (t Turn) ReferenceKind() string   { return t.Intent.ReferenceKind }

func lockConversation(ctx context.Context, tx pgx.Tx, conv uuid.UUID) error {
	var id uuid.UUID
	return notFound(tx.QueryRow(ctx, `SELECT c.id FROM conversations c WHERE c.id=$1 AND `+visible+` FOR UPDATE`, conv).Scan(&id))
}

func (s Service) Choose(ctx context.Context, ws, conv, turn, subject uuid.UUID, revision int64) error {
	entities, err := s.Entities(ctx, []uuid.UUID{subject})
	if err != nil {
		return err
	}
	entity, found := entities[subject]
	if !found {
		return errs.NotFound()
	}
	return db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		if err := lockConversation(ctx, tx, conv); err != nil {
			return err
		}
		var status string
		var actual, ordinal int64
		var in intent
		err := tx.QueryRow(ctx, `SELECT state,revision,ordinal,coalesce(intent,'{}'::jsonb) FROM conversation_turns WHERE id=$1 AND conversation_id=$2`, turn, conv).
			Scan(&status, &actual, &ordinal, &in)
		if err != nil {
			return notFound(err)
		}
		if actual > revision && in.Scope.SubjectID == subject && (status == "pending" || status == "confirm" || status == "research") {
			return nil
		}
		if status != "clarify" || actual != revision || in.Scope.Skill == "" {
			return errs.Conflict("CONVERSATION_CHANGED", "该轮消息已更新，请刷新后重试")
		}
		var newest int64
		if err := tx.QueryRow(ctx, `SELECT max(ordinal) FROM conversation_turns WHERE conversation_id=$1`, conv).Scan(&newest); err != nil {
			return err
		}
		if newest != ordinal {
			return errs.Conflict("CONVERSATION_CHANGED", "请从最近一轮继续研究")
		}
		if len(in.Candidates) > 0 && !slices.Contains(in.Candidates, subject) || in.Scope.Topic != "" && entity.Kind != "stock" {
			return errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "请选择该研究范围内的标的")
		}
		if in.Scope.Topic != "" && !slices.Contains(harness.ConversationWindows(in.Scope.Topic), in.Scope.Window) {
			return errs.Unprocessable("INVALID_PARAMS", "请先明确交易日窗口")
		}
		in.Scope.SubjectID = subject
		data, err := json.Marshal(intent{Scope: in.Scope})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_turns SET state='pending',intent=$2,message=NULL,revision=revision+1 WHERE id=$1`, turn, data); err != nil {
			return err
		}
		if err := touch(ctx, tx, conv); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, ws, conv, turn, actual+1)
	})
}

func (s Service) Confirm(ctx context.Context, ws, conv, turn, plan uuid.UUID, hash string, revision int64) error {
	t, err := s.Turn(ctx, ws, conv, turn)
	if err != nil {
		return err
	}
	if t.PlanID == nil || *t.PlanID != plan {
		return errs.NotFound()
	}
	if t.TaskID != nil {
		bound, err := s.Engine.LoadPlan(ctx, ws, plan)
		if err != nil {
			return err
		}
		if bound.Hash != hash {
			return errs.Conflict("PLAN_CHANGED", "研究计划已变更，请重新确认")
		}
		return nil
	}
	_, err = s.Engine.CreateConversationTask(ctx, ws, turn, plan, hash, revision)
	return err
}

func (s Service) Cancel(ctx context.Context, ws, conv, turn uuid.UUID, revision int64, taskRevision *int64) error {
	t, err := s.Turn(ctx, ws, conv, turn)
	if err != nil {
		return err
	}
	if t.TaskID != nil {
		if taskRevision == nil || revision != t.Revision {
			return errs.Conflict("CONVERSATION_CHANGED", "请刷新后取消当前研究")
		}
		_, _, err = s.Engine.CancelTask(ctx, ws, *t.TaskID, *taskRevision)
		return err
	}
	return db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		if err := lockConversation(ctx, tx, conv); err != nil {
			return err
		}
		var state string
		var current int64
		err := tx.QueryRow(ctx, `SELECT state,revision FROM conversation_turns WHERE id=$1 AND conversation_id=$2`, turn, conv).Scan(&state, &current)
		if err != nil {
			return notFound(err)
		}
		if state == "unavailable" && current == revision+1 {
			return nil
		}
		if current != revision || !slices.Contains([]string{"pending", "confirm", "clarify"}, state) {
			return errs.Conflict("CONVERSATION_CHANGED", "该轮消息已更新，请刷新后重试")
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_turns SET state='unavailable',message='已取消本轮研究。',revision=revision+1 WHERE id=$1`, turn); err != nil {
			return err
		}
		return touch(ctx, tx, conv)
	})
}

func (s Service) Delete(ctx context.Context, ws, conv uuid.UUID) error {
	return db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE conversations SET deleted_at=coalesce(deleted_at,now()),title='',scope=NULL
			WHERE id=$1 RETURNING id`, conv).Scan(&id)
		if err != nil {
			return notFound(err)
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_turns SET body='',message=NULL,intent=NULL WHERE conversation_id=$1`, conv)
		return err
	})
}

func (s Service) expire(ctx context.Context, ws, conv uuid.UUID) error {
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE conversations SET deleted_at=coalesce(deleted_at,now()),title='',scope=NULL
			WHERE id=$1 AND (deleted_at IS NOT NULL OR expires_at<=clock_timestamp()) RETURNING id`, conv).Scan(&id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_turns SET body='',message=NULL,intent=NULL WHERE conversation_id=$1`, conv)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
