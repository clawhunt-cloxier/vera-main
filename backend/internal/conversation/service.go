package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

type Service struct{ Engine *harness.Engine }

// Sender is constructed by a trusted channel adapter after authentication.
type Sender struct {
	Workspace uuid.UUID
	Channel   string
}

type Message struct {
	ClientMessageID  uuid.UUID  `json:"client_message_id"`
	Text             string     `json:"text"`
	ReplyTo          *uuid.UUID `json:"reply_to_turn_id,omitempty"`
	Automatic        bool       `json:"automatic,omitempty"`
	ExpectedRevision int64      `json:"expected_revision,omitempty"`
}

type Summary struct {
	ID           uuid.UUID `json:"id"`
	Title        string    `json:"title"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`
	TitlePending bool      `json:"-"`
}

type Turn struct {
	ID        uuid.UUID  `json:"id"`
	Ordinal   int64      `json:"ordinal"`
	Text      string     `json:"text"`
	Revision  int64      `json:"revision"`
	State     string     `json:"state"`
	ReplyTo   *uuid.UUID `json:"reply_to_turn_id,omitempty"`
	Message   *string    `json:"message,omitempty"`
	CreatedAt time.Time  `json:"created_at"`

	Automatic       bool       `json:"-"`
	PlanID          *uuid.UUID `json:"-"`
	TaskID          *uuid.UUID `json:"-"`
	TaskStatus      *string    `json:"task_status,omitempty"`
	TaskRevision    *int64     `json:"task_revision,omitempty"`
	ReasonCode      *string    `json:"reason_code,omitempty"`
	ReportID        *uuid.UUID `json:"-"`
	ReportVersion   *int       `json:"-"`
	ResearchVersion *string    `json:"-"`
	Brief           bool       `json:"-"`
	Intent          intent     `json:"-"`
}

type Scope struct {
	SubjectID uuid.UUID `json:"subject_id"`
	Topic     string    `json:"topic,omitempty"`
	Skill     string    `json:"skill_id"`
	Window    int       `json:"window,omitempty"`
	Mode      string    `json:"mode,omitempty"`
}

type intent struct {
	Scope          Scope       `json:"scope"`
	Candidates     []uuid.UUID `json:"candidates,omitempty"`
	Reference      *uuid.UUID  `json:"reference,omitempty"`
	ReferenceKind  string      `json:"reference_kind,omitempty"`
	Preparing      bool        `json:"preparing,omitempty"`
	FinanceAnswer  bool        `json:"finance_answer,omitempty"`
	NeedsSubject   bool        `json:"needs_subject,omitempty"`
	AdviceDeclined bool        `json:"advice_declined,omitempty"`
}

type Accepted struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	TurnID         uuid.UUID `json:"turn_id"`
}

const visible = `c.deleted_at IS NULL AND c.expires_at>clock_timestamp()`

func (s Service) Send(ctx context.Context, sender Sender, conversationID *uuid.UUID, message Message) (Accepted, error) {
	var out Accepted
	message.Text = strings.TrimSpace(message.Text)
	if message.ClientMessageID == uuid.Nil || message.Text == "" || utf8.RuneCountInString(message.Text) > 500 {
		return out, errs.BadRequest("INVALID_MESSAGE", "请输入 1–500 字并提供消息编号")
	}
	if sender.Channel != "web" || conversationID == nil && (message.ReplyTo != nil || message.ExpectedRevision != 0) || conversationID != nil && message.ExpectedRevision < 1 {
		return out, errs.BadRequest("INVALID_MESSAGE", "消息渠道或会话版本无效")
	}
	if s.Engine.River == nil || len(s.Engine.ConversationTopics()) == 0 {
		return out, errs.Unavailable("CONVERSATIONS_UNAVAILABLE", "对话研究服务未就绪")
	}
	request := []any{conversationID, message.Text, message.ReplyTo, sender.Channel}
	if message.Automatic {
		request = append(request, true)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	err = db.InWorkspace(ctx, s.Engine.Pool, sender.Workspace, func(tx pgx.Tx) error {
		// Serializes acceptance, including the first message, until idempotency has a durable row.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43001))`, sender.Workspace.String()); err != nil {
			return err
		}
		var previousHash string
		var stillVisible bool
		err := tx.QueryRow(ctx, `SELECT t.conversation_id,t.id,t.request_hash,(`+visible+`) FROM conversation_turns t
			JOIN conversations c ON c.id=t.conversation_id AND c.workspace_id=t.workspace_id WHERE t.client_message_id=$1`,
			message.ClientMessageID).Scan(&out.ConversationID, &out.TurnID, &previousHash, &stillVisible)
		if err == nil {
			if !stillVisible {
				return errs.NotFound()
			}
			if previousHash != hash {
				return errs.Conflict("MESSAGE_ID_REUSED", "该消息编号已用于不同请求")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if conversationID == nil {
			if err := tx.QueryRow(ctx, `INSERT INTO conversations(workspace_id,title) VALUES($1,'') RETURNING id`, sender.Workspace).Scan(&out.ConversationID); err != nil {
				return err
			}
		} else {
			out.ConversationID = *conversationID
			var revision int64
			if err := tx.QueryRow(ctx, `SELECT revision FROM conversations c WHERE id=$1 AND `+visible+` FOR UPDATE`, conversationID).Scan(&revision); err != nil {
				return notFound(err)
			}
			if revision != message.ExpectedRevision {
				return errs.Conflict("CONVERSATION_CHANGED", "会话已更新，请刷新后重试")
			}
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conversation_turns t LEFT JOIN research_tasks r ON r.id=t.task_id AND r.workspace_id=t.workspace_id
			WHERE t.conversation_id=$1 AND
			(t.state='pending' OR t.state='research' AND r.status<>ALL($2::text[])))`,
			out.ConversationID, terminalStatuses).Scan(&active); err != nil {
			return err
		}
		if active {
			return errs.Conflict("CONVERSATION_ACTIVE", "请先完成或取消本会话中的研究")
		}
		if message.ReplyTo != nil {
			var found uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM conversation_turns WHERE conversation_id=$1 AND id=$2`, out.ConversationID, message.ReplyTo).Scan(&found); err != nil {
				return notFound(err)
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO conversation_turns(workspace_id,conversation_id,client_message_id,request_hash,ordinal,channel,body,reply_to_turn_id,state,automatic)
			VALUES($1,$2,$3,$4,(SELECT coalesce(max(ordinal),0)+1 FROM conversation_turns WHERE conversation_id=$2),$5,$6,$7,'pending',$8) RETURNING id`,
			sender.Workspace, out.ConversationID, message.ClientMessageID, hash, sender.Channel, message.Text, message.ReplyTo, message.Automatic).Scan(&out.TurnID); err != nil {
			return err
		}
		if err := touch(ctx, tx, out.ConversationID); err != nil {
			return err
		}
		if err := s.enqueue(ctx, tx, sender.Workspace, out.ConversationID, out.TurnID, 1); err != nil {
			return err
		}
		if conversationID == nil {
			return s.enqueueTitle(ctx, tx, sender.Workspace, out.ConversationID, out.TurnID)
		}
		return nil
	})
	return out, err
}

var terminalStatuses = []string{"COMPLETED", "ABSTAINED", "BUDGET_EXHAUSTED", "CANCELLED", "BLOCKED", "FAILED"}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.NotFound()
	}
	return err
}

func touch(ctx context.Context, tx pgx.Tx, conv uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE conversations SET revision=revision+1, updated_at=now(), expires_at=now()+interval '90 days' WHERE id=$1`, conv)
	return err
}

const titleDeadline = `c.created_at+interval '1 minute'>clock_timestamp()`

const summaryColumns = `c.id,coalesce(nullif(c.title,''),'新会话'),c.revision,c.updated_at,c.title='' AND ` + titleDeadline

func (s Service) List(ctx context.Context, ws uuid.UUID, n int, at *time.Time, id *uuid.UUID) ([]Summary, error) {
	items := []Summary{}
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+summaryColumns+` FROM conversations c WHERE `+visible+`
		AND ($1::timestamptz IS NULL OR (c.updated_at,c.id)<($1,$2)) ORDER BY c.updated_at DESC,c.id DESC LIMIT $3`, at, id, n)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) { return scanSummary(row) })
		return err
	})
	return items, err
}

func scanSummary(row pgx.Row) (Summary, error) {
	var c Summary
	err := row.Scan(&c.ID, &c.Title, &c.Revision, &c.UpdatedAt, &c.TitlePending)
	return c, err
}

const turnColumns = `t.id,t.ordinal,t.body,t.revision,t.state,t.reply_to_turn_id,t.message,t.created_at,t.plan_id,t.task_id,
	r.status,r.revision,r.reason_code,r.report_id,v.version,r.skill_version,coalesce(p.conversation_brief,false),coalesce(t.intent,'{}'::jsonb),t.automatic`

const turnJoins = ` FROM conversation_turns t JOIN conversations c ON c.id=t.conversation_id AND c.workspace_id=t.workspace_id
	LEFT JOIN research_tasks r ON r.id=t.task_id AND r.workspace_id=t.workspace_id
	LEFT JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
	LEFT JOIN report_versions v ON v.report_id=r.report_id AND v.workspace_id=r.workspace_id AND v.version=1`

func scanTurn(row pgx.Row) (Turn, error) {
	var t Turn
	err := row.Scan(&t.ID, &t.Ordinal, &t.Text, &t.Revision, &t.State, &t.ReplyTo, &t.Message, &t.CreatedAt, &t.PlanID, &t.TaskID,
		&t.TaskStatus, &t.TaskRevision, &t.ReasonCode, &t.ReportID, &t.ReportVersion, &t.ResearchVersion, &t.Brief, &t.Intent, &t.Automatic)
	return t, err
}

func (s Service) History(ctx context.Context, ws, conv uuid.UUID, n int, before int64) (Summary, []Turn, error) {
	var c Summary
	var turns []Turn
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		// Keep the revision and turns consistent if a resolver commits during a read.
		var err error
		c, err = scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM conversations c WHERE c.id=$1 AND `+visible+` FOR SHARE`, conv))
		if err != nil {
			return notFound(err)
		}
		rows, err := tx.Query(ctx, `SELECT `+turnColumns+turnJoins+` WHERE t.conversation_id=$1 AND ($2::bigint=0 OR t.ordinal<$2) AND `+visible+`
			ORDER BY t.ordinal DESC LIMIT $3`, conv, before, n)
		if err != nil {
			return err
		}
		turns, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Turn, error) { return scanTurn(r) })
		return err
	})
	return c, turns, err
}

func (s Service) Turn(ctx context.Context, ws, conv, id uuid.UUID) (Turn, error) {
	var t Turn
	err := db.InWorkspace(ctx, s.Engine.Pool, ws, func(tx pgx.Tx) error {
		var err error
		t, err = scanTurn(tx.QueryRow(ctx, `SELECT `+turnColumns+turnJoins+` WHERE t.conversation_id=$1 AND t.id=$2 AND `+visible, conv, id))
		return notFound(err)
	})
	return t, err
}

func (s Service) ReferenceTurn(ctx context.Context, ws, conv uuid.UUID, t Turn) (Turn, error) {
	if t.State == "reference" && t.Intent.Reference != nil {
		return s.Turn(ctx, ws, conv, *t.Intent.Reference)
	}
	return t, nil
}

type resolveArgs struct {
	Workspace    uuid.UUID `json:"workspace"`
	Conversation uuid.UUID `json:"conversation"`
	Turn         uuid.UUID `json:"turn" river:"unique"`
	Revision     int64     `json:"revision" river:"unique"`
}

func (resolveArgs) Kind() string { return "conversation.resolve" }

func (s Service) enqueue(ctx context.Context, tx pgx.Tx, ws, conv, turn uuid.UUID, revision int64) error {
	_, err := s.Engine.River.InsertTx(ctx, tx, resolveArgs{ws, conv, turn, revision}, &river.InsertOpts{Queue: "conversation", MaxAttempts: 4, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err
}

type worker struct {
	river.WorkerDefaults[resolveArgs]
	service Service
}

func (w *worker) Timeout(*river.Job[resolveArgs]) time.Duration { return 2 * time.Minute }

func (w *worker) Work(ctx context.Context, job *river.Job[resolveArgs]) error {
	err := w.service.resolve(ctx, job.Args)
	if err != nil && errs.As(err).Status == 404 {
		return nil // The work item expired or was deleted during resolution.
	}
	if err != nil && job.Attempt >= job.MaxAttempts && !errors.Is(err, errPreparing) {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return w.service.fail(finishCtx, job.Args, "研究准备未能完成，请重新发送问题。")
	}
	if errors.Is(err, errPreparing) {
		return river.JobSnooze(4 * time.Second)
	}
	return err
}

func Register(engine *harness.Engine) func(*river.Workers, *river.Config) {
	return func(workers *river.Workers, cfg *river.Config) {
		river.AddWorker(workers, &worker{service: Service{Engine: engine}})
		river.AddWorker(workers, &titleWorker{service: Service{Engine: engine}})
		if cfg.Queues != nil {
			cfg.Queues["conversation"] = river.QueueConfig{MaxWorkers: 2}
			cfg.Queues[titleQueue] = river.QueueConfig{MaxWorkers: 1}
			registerRetention(workers, cfg, engine)
		}
	}
}
