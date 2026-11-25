package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

var (
	errPreparing = errors.New("waiting for research data")
	errTurnMoved = errors.New("conversation turn moved")
)

func (s Service) resolve(ctx context.Context, args resolveArgs) error {
	t, err := s.Turn(ctx, args.Workspace, args.Conversation, args.Turn)
	if err != nil && errs.As(err).Status == 404 {
		return nil
	}
	if err != nil {
		return err
	}
	if t.State != "pending" || t.Revision != args.Revision {
		return nil
	}
	if t.Intent.Scope.SubjectID != uuid.Nil {
		return s.plan(ctx, args, t.Intent.Scope, t.CreatedAt)
	}
	q := parseQuestion(t.Text)
	if q.missing != "" {
		return s.update(ctx, args, "clarify", q.missing, intent{})
	}
	catalog := s.catalog()
	entities, err := catalog.MentionedInQuestion(ctx, t.Text)
	if err != nil {
		return err
	}
	if q.reference != "" {
		ref, err := s.anchor(ctx, args.Workspace, args.Conversation, t)
		if err != nil {
			return err
		}
		if !matchesAnchor(q, t, ref, entities) {
			return s.update(ctx, args, "clarify", "请在已交付的一轮研究中选择追问，再查看原始结果、证据或计算。", intent{})
		}
		return s.update(ctx, args, "reference", "已找到该轮研究的原始结果。", intent{Reference: &ref.ID, ReferenceKind: q.reference})
	}
	if q.scope.Topic != "" && !slices.Contains(s.Engine.ConversationTopics(), q.scope.Topic) {
		return s.fail(ctx, args, "当前数据源尚未开放该研究主题。")
	}
	previous, err := s.contextScope(ctx, args.Workspace, args.Conversation, t)
	if err != nil {
		return err
	}
	prior := previous.SubjectID != uuid.Nil
	scope := q.scope
	if scope.Skill == "" {
		switch {
		case prior && (q.inherit || q.differentSubject || len(entities) > 0 && hasAny(t.Text, "呢", "看看", "改看")):
			scope.Topic, scope.Skill = previous.Topic, previous.Skill
		case len(entities) > 0 && (hasAny(t.Text, "研究", "分析", "看看", "怎么样") || exactSubjectName(t.Text, entities)):
			scope.Topic, scope.Skill = harness.DailyTradingTopic, "stock_analysis"
		default:
			return s.update(ctx, args, "clarify", "请注明要看的指标，例如日成交或融资余额。", intent{})
		}
	}
	if scope.Skill == "full" {
		scope.Skill = "stock_analysis"
		if prior {
			scope.Skill = previous.Skill
		}
		scope.Topic = ""
	}
	if scope.Topic != "" && scope.Window == 0 {
		scope.Window = 20
		if prior && scope.Topic == previous.Topic && previous.Window != 0 && !q.differentSubject {
			scope.Window = previous.Window
		}
	}
	if scope.Topic != "" && !slices.Contains(harness.ConversationWindows(scope.Topic), scope.Window) {
		return s.update(ctx, args, "clarify", fmt.Sprintf("该主题支持 %v 个交易日。请指定其中一个窗口。", harness.ConversationWindows(scope.Topic)), intent{})
	}
	if len(entities) == 0 && prior && !q.differentSubject && q.inherit && (q.scope.Skill != "" || q.windowSpecified || hasAny(t.Text, "重新", "更新", "继续", "再查", "再看")) {
		scope.SubjectID = previous.SubjectID
	}
	if scope.Topic != "" {
		entities = slices.DeleteFunc(entities, func(e dataservice.EntityRow) bool { return e.Kind != "stock" })
	}
	if len(entities) == 0 && scope.SubjectID == uuid.Nil {
		message := "请补充或搜索证券名称或代码。"
		if scope.Topic == "" {
			message = "请输入完整研究问题并注明研究对象，也可从工作台选择研究类型。"
		}
		return s.update(ctx, args, "clarify", message, intent{Scope: scope})
	}
	if len(entities) > 1 {
		ids := make([]uuid.UUID, 0, len(entities))
		for _, entity := range entities {
			ids = append(ids, entity.ID)
		}
		return s.update(ctx, args, "clarify", "请选择一个研究对象。", intent{Scope: scope, Candidates: ids})
	}
	if len(entities) == 1 {
		scope.SubjectID = entities[0].ID
	}
	return s.plan(ctx, args, scope, t.CreatedAt)
}

func matchesAnchor(q question, t Turn, ref *Turn, subjects []dataservice.EntityRow) bool {
	if ref == nil || ref.ReportID == nil || ref.ReportVersion == nil {
		return false
	}
	if q.scope.Topic != "" && q.scope.Topic != ref.Scope().Topic {
		return false
	}
	if len(subjects) != 0 {
		return len(subjects) == 1 && subjects[0].ID == ref.Scope().SubjectID
	}
	return t.ReplyTo != nil || q.referenceImplicit
}

func exactSubjectName(text string, entities []dataservice.EntityRow) bool {
	return slices.ContainsFunc(entities, func(e dataservice.EntityRow) bool {
		return strings.EqualFold(text, e.Code) || text == e.Name
	})
}

func (s Service) catalog() *dataservice.Store {
	if s.Engine.ChoiceCatalog != nil {
		return s.Engine.ChoiceCatalog
	}
	return s.Engine.Store
}

func (s Service) Entities(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]dataservice.EntityRow, error) {
	return s.Engine.Store.Entities(ctx, ids)
}

// contextScope is the workspace scoped point for adding opted-in memory and display preferences.
func (s Service) contextScope(ctx context.Context, ws, conv uuid.UUID, t Turn) (Scope, error) {
	if t.ReplyTo != nil {
		ref, err := s.Turn(ctx, ws, conv, *t.ReplyTo)
		if err != nil {
			return Scope{}, err
		}
		ref, err = s.ReferenceTurn(ctx, ws, conv, ref)
		return ref.Intent.Scope, err
	}
	var scope Scope
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(c.scope,'{}'::jsonb) FROM conversations c WHERE c.id=$1 AND `+visible, conv).Scan(&scope)
	})
	return scope, notFound(err)
}

func (s Service) anchor(ctx context.Context, ws, conv uuid.UUID, t Turn) (*Turn, error) {
	if t.ReplyTo != nil {
		ref, err := s.Turn(ctx, ws, conv, *t.ReplyTo)
		if err != nil {
			return nil, err
		}
		ref, err = s.ReferenceTurn(ctx, ws, conv, ref)
		return &ref, err
	}
	var ref Turn
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		var err error
		ref, err = scanTurn(tx.QueryRow(ctx, `SELECT `+turnColumns+turnJoins+` WHERE t.conversation_id=$1 AND t.ordinal<$2
			AND r.status='COMPLETED' AND v.version IS NOT NULL AND `+visible+` ORDER BY t.ordinal DESC LIMIT 1`, conv, t.Ordinal))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &ref, err
}

func (s Service) plan(ctx context.Context, args resolveArgs, scope Scope, started time.Time) error {
	ents, err := s.Entities(ctx, []uuid.UUID{scope.SubjectID})
	if err != nil {
		return err
	}
	subject, ok := ents[scope.SubjectID]
	if !ok {
		return s.fail(ctx, args, "该研究对象当前不可用。")
	}
	if subject.Kind == "stock" && s.Engine.PrepareTuShareSubject != nil {
		registered, err := s.Engine.Store.ResearchSubjectPrepared(ctx, subject.Code)
		if err != nil {
			return err
		}
		if !registered {
			if time.Since(started) > 30*time.Minute {
				return s.fail(ctx, args, "准备数据超过时限，请重新发起。")
			}
			prep, err := s.Engine.RequestSubjectPreparation(ctx, args.Workspace, subject.Code)
			switch {
			case err != nil:
				return s.fail(ctx, args, errs.As(err).Message)
			case prep.Status == "failed":
				return s.fail(ctx, args, prep.Message)
			case prep.Status != "ready":
				if err := s.setPreparing(ctx, args, scope); err != nil {
					return err
				}
				return errPreparing
			}
		}
	}
	_, err = s.Engine.BuildConversationPlan(ctx, args.Workspace, args.Turn, subject, scope.Skill, scope.Topic, scope.Window, func(tx pgx.Tx, plan *harness.Plan) error {
		if plan.Params.Window != 0 {
			scope.Window = plan.Params.Window
		}
		return s.writeResolution(ctx, tx, args, "confirm", "研究计划已准备好。", intent{Scope: scope}, &plan.ID)
	})
	if errors.Is(err, errTurnMoved) {
		return nil
	}
	if err != nil && errs.As(err).Status != 500 {
		return s.fail(ctx, args, errs.As(err).Message)
	}
	return err
}

func (s Service) setPreparing(ctx context.Context, args resolveArgs, scope Scope) error {
	return db.InWorkspace(ctx, s.Engine.Pool, args.Workspace, func(tx pgx.Tx) error {
		data, err := json.Marshal(intent{Scope: scope, Preparing: true})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_turns t SET intent=$3, message='正在准备研究数据…'
			WHERE t.id=$1 AND t.revision=$2 AND t.state='pending'`, args.Turn, args.Revision, data)
		return err
	})
}

func (s Service) writeResolution(ctx context.Context, tx pgx.Tx, args resolveArgs, state, message string, newIntent intent, plan *uuid.UUID) error {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT c.id FROM conversations c WHERE c.id=$1 AND `+visible+` FOR UPDATE`, args.Conversation).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errTurnMoved
		}
		return err
	}
	data, err := json.Marshal(newIntent)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE conversation_turns t SET state=$2,plan_id=$3,message=$4,intent=$5,revision=revision+1
		WHERE id=$1 AND conversation_id=$6 AND revision=$7 AND state='pending'`, args.Turn, state, plan, message, data, args.Conversation, args.Revision)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errTurnMoved
	}
	return touch(ctx, tx, args.Conversation)
}

func (s Service) update(ctx context.Context, args resolveArgs, state, message string, newIntent intent) error {
	err := db.InWorkspace(ctx, s.Engine.Pool, args.Workspace, func(tx pgx.Tx) error {
		return s.writeResolution(ctx, tx, args, state, message, newIntent, nil)
	})
	if errors.Is(err, errTurnMoved) {
		return nil
	}
	return err
}

func (s Service) fail(ctx context.Context, args resolveArgs, message string) error {
	return s.update(ctx, args, "unavailable", message, intent{})
}
