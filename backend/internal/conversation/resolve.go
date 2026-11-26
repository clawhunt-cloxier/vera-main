package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	turn, err := s.Turn(ctx, args.Workspace, args.Conversation, args.Turn)
	if err != nil && errs.As(err).Status == 404 {
		return nil
	}
	if err != nil {
		return err
	}
	if turn.State != "pending" || turn.Revision != args.Revision {
		return nil
	}
	if turn.Intent.Scope.SubjectID != uuid.Nil {
		return s.plan(ctx, args, turn.Intent, turn)
	}
	return s.runAgent(ctx, args, turn)
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

func (s Service) lastClarification(ctx context.Context, args resolveArgs, ordinal int64) (intent, error) {
	var draft intent
	err := db.InWorkspace(ctx, s.Engine.Pool, args.Workspace, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT t.intent FROM conversation_turns t JOIN conversations c
			ON c.workspace_id=t.workspace_id AND c.id=t.conversation_id WHERE t.conversation_id=$1
			AND t.ordinal=$2 AND t.state='clarify' AND t.intent IS NOT NULL AND `+visible, args.Conversation, ordinal-1).Scan(&draft)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return intent{}, nil
	}
	return draft, err
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

func (s Service) researchReferences(ctx context.Context, ws, conv uuid.UUID, t Turn) ([]Turn, error) {
	if t.ReplyTo != nil {
		ref, err := s.Turn(ctx, ws, conv, *t.ReplyTo)
		if err != nil {
			return nil, err
		}
		ref, err = s.ReferenceTurn(ctx, ws, conv, ref)
		if err != nil || ref.ReportID == nil || ref.ReportVersion == nil {
			return nil, err
		}
		return []Turn{ref}, nil
	}
	var references []Turn
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+turnColumns+turnJoins+` WHERE t.conversation_id=$1 AND t.ordinal<$2
			AND r.status='COMPLETED' AND v.version IS NOT NULL AND `+visible+` ORDER BY t.ordinal DESC LIMIT 6`, conv, t.Ordinal)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			ref, err := scanTurn(rows)
			if err != nil {
				return err
			}
			references = append(references, ref)
		}
		return rows.Err()
	})
	return references, err
}

func (s Service) plan(ctx context.Context, args resolveArgs, in intent, turn Turn) error {
	scope := in.Scope
	spec, ok := s.Engine.ConversationResearchSpec(scope.Skill, scope.Topic)
	if !ok {
		return s.update(ctx, args, "unavailable", "当前环境尚未开放这项研究。", in)
	}
	if scope.Window != 0 && !slices.Contains(spec.Windows, scope.Window) || scope.Mode != "" && !slices.Contains(spec.Modes, scope.Mode) {
		return s.fail(ctx, args, "该研究能力不支持所请求的范围。")
	}
	ents, err := s.Entities(ctx, []uuid.UUID{scope.SubjectID})
	if err != nil {
		return err
	}
	subject, ok := ents[scope.SubjectID]
	if !ok {
		return s.fail(ctx, args, "该研究对象当前不可用。")
	}
	if !slices.Contains(spec.Subjects, subject.Kind) || scope.Mode == "company_industry" && subject.Kind != "stock" {
		return s.fail(ctx, args, "该研究能力不支持此对象类型。")
	}
	if scope.Topic == harness.ComparisonTopic {
		return s.planComparison(ctx, args, in, turn)
	}
	var prep harness.SubjectPreparation
	if s.Engine.UsesIndicatorPreparation(scope.Topic) {
		if time.Since(turn.CreatedAt) > 30*time.Minute {
			return s.fail(ctx, args, "准备数据超过时限，请重新发起。")
		}
		prep, err = s.Engine.PrepareIndicator(ctx, args.Workspace, args.Turn, args.Revision, subject, scope.Topic, scope.Window)
	} else if subject.Kind == "stock" && s.Engine.PrepareTuShareSubject != nil {
		registered, readErr := s.Engine.Store.ResearchSubjectPrepared(ctx, subject.Code)
		if readErr != nil {
			return readErr
		}
		if !registered {
			if time.Since(turn.CreatedAt) > 30*time.Minute {
				return s.fail(ctx, args, "准备数据超过时限，请重新发起。")
			}
			prep, err = s.Engine.RequestConversationSubjectPreparation(ctx, args.Workspace, subject.Code, args.Turn, args.Revision)
		}
	}
	switch {
	case err != nil:
		return s.fail(ctx, args, errs.As(err).Message)
	case prep.Status == "failed":
		return s.fail(ctx, args, prep.Message)
	case prep.Status == "pending":
		if err := s.setPreparing(ctx, args, in); err != nil {
			if errors.Is(err, errTurnMoved) {
				return nil
			}
			return err
		}
		return errPreparing
	}
	return s.launchResearch(ctx, args, in, turn, func(launch harness.ConversationLaunch) (*harness.Plan, error) {
		return s.Engine.StartConversationResearch(ctx, args.Workspace, subject, scope.Skill, scope.Topic, scope.Window, scope.Mode, launch)
	})
}

func (s Service) launchResearch(ctx context.Context, args resolveArgs, in intent, turn Turn, start func(harness.ConversationLaunch) (*harness.Plan, error)) error {
	launch := harness.ConversationLaunch{TurnID: args.Turn, Revision: args.Revision, AutoRun: turn.Automatic}
	launch.Bind = func(tx pgx.Tx, plan *harness.Plan) error {
		if plan.Params.Window != 0 {
			in.Scope.Window = plan.Params.Window
		}
		in.Preparing = false
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_turns SET intent=$2 WHERE id=$1`, args.Turn, data)
		return err
	}
	plan, err := start(launch)
	if err != nil {
		if errs.As(err).Status != 500 {
			return s.fail(ctx, args, errs.As(err).Message)
		}
		return err
	}
	if turn.Automatic && !plan.Executable {
		for _, section := range plan.Sections {
			if section.Required && section.Avail == "unavailable" && section.Reason != nil {
				return s.fail(ctx, args, *section.Reason)
			}
		}
		return s.fail(ctx, args, "缺少本轮研究所需的数据。")
	}
	return nil
}

func (s Service) setPreparing(ctx context.Context, args resolveArgs, in intent) error {
	return db.InWorkspace(ctx, s.Engine.Pool, args.Workspace, func(tx pgx.Tx) error {
		if err := lockConversation(ctx, tx, args.Conversation); err != nil {
			if errs.As(err).Status == 404 {
				return errTurnMoved
			}
			return err
		}
		in.Preparing = true
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE conversation_turns SET intent=$3, message='正在准备研究数据…'
			WHERE id=$1 AND revision=$2 AND state='pending' AND conversation_id=$4`, args.Turn, args.Revision, data, args.Conversation)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return errTurnMoved
		}
		return nil
	})
}

func (s Service) writeResolution(ctx context.Context, tx pgx.Tx, args resolveArgs, state, message string, newIntent intent) error {
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
	result, err := tx.Exec(ctx, `UPDATE conversation_turns SET state=$2,message=$3,intent=$4,revision=revision+1
		WHERE id=$1 AND conversation_id=$5 AND revision=$6 AND state='pending'`, args.Turn, state, message, data, args.Conversation, args.Revision)
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
		return s.writeResolution(ctx, tx, args, state, message, newIntent)
	})
	if errors.Is(err, errTurnMoved) {
		return nil
	}
	return err
}

func (s Service) fail(ctx context.Context, args resolveArgs, message string) error {
	return s.update(ctx, args, "unavailable", message, intent{})
}
