package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

type conversationAction struct {
	Action           string     `json:"action"`
	ExpectedRevision int64      `json:"expected_revision"`
	SubjectID        *uuid.UUID `json:"subject_id,omitempty"`
	PlanID           *uuid.UUID `json:"plan_id,omitempty"`
	PlanHash         string     `json:"plan_hash,omitempty"`
	TaskRevision     *int64     `json:"task_revision,omitempty"`
}

type conversationTurn struct {
	conversation.Turn
	Scope         *conversation.Scope `json:"scope,omitempty"`
	Plan          *ResearchPlan       `json:"plan,omitempty"`
	Candidates    []Entity            `json:"candidates,omitempty"`
	ReferenceID   *uuid.UUID          `json:"reference_turn_id,omitempty"`
	ReferenceKind string              `json:"reference_kind,omitempty"`
	ResultReady   bool                `json:"result_ready"`
	IsBrief       bool                `json:"is_brief"`
}

// p is the plan for the requested task. Workspace RLS applies to the join.
const visibleShortResearch = `(NOT p.conversation_brief OR EXISTS (
	SELECT 1 FROM conversation_turns turn JOIN conversations chat
	ON chat.id=turn.conversation_id AND chat.workspace_id=turn.workspace_id
	WHERE turn.id=p.conversation_turn_id AND turn.workspace_id=p.workspace_id
	AND chat.deleted_at IS NULL AND chat.expires_at>clock_timestamp()))`

func (s *Server) conversations() conversation.Service { return conversation.Service{Engine: s.Engine} }

func (s *Server) mountConversations(m *http.ServeMux) {
	auth := s.ID.Require(true)
	register := func(pattern string, handler func(http.ResponseWriter, *http.Request) error) {
		m.Handle(pattern, auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if err := handler(w, r); err != nil {
				httpx.Error(w, r, err)
			}
		})))
	}
	register("POST /api/v1/conversations", s.sendConversation)
	register("GET /api/v1/conversations", s.listConversations)
	register("GET /api/v1/conversations/{conversation}", s.getConversation)
	register("DELETE /api/v1/conversations/{conversation}", s.deleteConversation)
	register("POST /api/v1/conversations/{conversation}/turns", s.sendConversation)
	register("GET /api/v1/conversations/{conversation}/turns/{turn}", s.getConversationTurn)
	register("POST /api/v1/conversations/{conversation}/turns/{turn}/actions", s.conversationAction)
	register("GET /api/v1/conversations/{conversation}/turns/{turn}/result", s.conversationResult)
}

func (s *Server) sendConversation(w http.ResponseWriter, r *http.Request) error {
	var in conversation.Message
	var conv *uuid.UUID
	if r.PathValue("conversation") != "" {
		id, err := pathID(r, "conversation")
		if err != nil {
			return err
		}
		conv = &id
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	} else {
		var first struct {
			ClientMessageID uuid.UUID `json:"client_message_id"`
			Text            string    `json:"text"`
		}
		if err := httpx.Decode(r, &first); err != nil {
			return err
		}
		in.ClientMessageID, in.Text = first.ClientMessageID, first.Text
	}
	_, ws := principal(r)
	out, err := s.conversations().Send(r.Context(), conversation.Sender{Workspace: ws, Channel: "web"}, conv, in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/conversations/"+out.ConversationID.String()+"/turns/"+out.TurnID.String())
	w.Header().Set("Retry-After", "2")
	httpx.JSON(w, r, http.StatusAccepted, out)
	return nil
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) error {
	n, err := pageSize(r)
	if err != nil {
		return err
	}
	c, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	var at *time.Time
	var id *uuid.UUID
	if c != nil {
		at, id = &c.At, &c.ID
	}
	_, ws := principal(r)
	items, err := s.conversations().List(r.Context(), ws, n+1, at, id)
	if err != nil {
		return err
	}
	var next *string
	if len(items) > n {
		items = items[:n]
		next = encodeCursor(items[n-1].UpdatedAt, items[n-1].ID)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
	return nil
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) error {
	conv, err := pathID(r, "conversation")
	if err != nil {
		return err
	}
	n, err := pageSize(r)
	if err != nil {
		return err
	}
	var before int64
	if value := r.URL.Query().Get("before"); value != "" {
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before <= 0 {
			return errs.BadRequest("INVALID_CURSOR", "消息游标无效")
		}
	}
	_, ws := principal(r)
	header, turns, err := s.conversations().History(r.Context(), ws, conv, n+1, before)
	if err != nil {
		return err
	}
	var older *int64
	if len(turns) > n {
		turns = turns[:n]
		older = &turns[n-1].Ordinal
	}
	items := make([]conversationTurn, 0, len(turns))
	for _, t := range turns {
		out := turnView(t)
		// Only the newest turn can accept a pending action. Fetch its plan
		// from the turn endpoint to avoid multiple licensed reads per page.
		items = append(items, out)
		if turnInProgress(t) {
			w.Header().Set("Retry-After", "2")
		}
	}
	slices.Reverse(items)
	httpx.JSON(w, r, http.StatusOK, map[string]any{"conversation": header, "turns": items, "next_before": older})
	return nil
}

func (s *Server) getConversationTurn(w http.ResponseWriter, r *http.Request) error {
	_, t, err := s.loadConversationTurn(r)
	if err != nil {
		return err
	}
	_, ws := principal(r)
	out := turnView(t)
	if turnInProgress(t) {
		w.Header().Set("Retry-After", "2")
	}
	if t.State == "clarify" && len(t.Candidates()) > 0 {
		ids := t.Candidates()
		entities, err := s.conversations().Entities(r.Context(), ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if e, ok := entities[id]; ok {
				out.Candidates = append(out.Candidates, toEntity(e, s.Engine.Skills))
			}
		}
	}
	if t.State == "confirm" && t.PlanID != nil {
		plan, err := s.Engine.LoadPlan(r.Context(), ws, *t.PlanID)
		if err != nil {
			return err
		}
		p := s.toPlan(plan)
		if time.Now().After(p.ExpiresAt) {
			out.State = "unavailable"
			message := "研究计划已过期，请重新发起。"
			out.Message = &message
		} else {
			grant, err := s.versionGrant(r, plan.SkillVersion)
			if err != nil {
				return err
			}
			out.Plan = &p
			return grant(func() error { httpx.JSON(w, r, http.StatusOK, out); return nil })
		}
	}
	httpx.JSON(w, r, http.StatusOK, out)
	return nil
}

func turnView(t conversation.Turn) conversationTurn {
	out := conversationTurn{Turn: t, ReferenceID: t.Reference(), ReferenceKind: t.ReferenceKind(), IsBrief: t.Brief, ResultReady: t.ReportID != nil && t.ReportVersion != nil || t.Reference() != nil}
	if scope := t.Scope(); scope.Skill != "" {
		out.Scope = &scope
	}
	return out
}

func turnInProgress(t conversation.Turn) bool {
	return t.State == "pending" || t.TaskStatus != nil && !harness.IsTerminal(*t.TaskStatus)
}

func (s *Server) loadConversationTurn(r *http.Request) (uuid.UUID, conversation.Turn, error) {
	conv, err := pathID(r, "conversation")
	if err != nil {
		return conv, conversation.Turn{}, err
	}
	turn, err := pathID(r, "turn")
	if err != nil {
		return conv, conversation.Turn{}, err
	}
	_, ws := principal(r)
	t, err := s.conversations().Turn(r.Context(), ws, conv, turn)
	return conv, t, err
}

func (s *Server) conversationAction(w http.ResponseWriter, r *http.Request) error {
	conv, t, err := s.loadConversationTurn(r)
	if err != nil {
		return err
	}
	_, ws := principal(r)
	var in conversationAction
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.ExpectedRevision < 1 {
		return errs.BadRequest("INVALID_ACTION", "需要消息版本")
	}
	service := s.conversations()
	switch in.Action {
	case "choose":
		if in.SubjectID == nil || in.PlanID != nil || in.PlanHash != "" || in.TaskRevision != nil {
			return errs.BadRequest("INVALID_ACTION", "请选择研究对象")
		}
		err = service.Choose(r.Context(), ws, conv, t.ID, *in.SubjectID, in.ExpectedRevision)
	case "confirm":
		if in.PlanID == nil || !validPlanHash(in.PlanHash) || in.SubjectID != nil || in.TaskRevision != nil {
			return errs.BadRequest("INVALID_ACTION", "需要计划编号和校验值")
		}
		err = service.Confirm(r.Context(), ws, conv, t.ID, *in.PlanID, in.PlanHash, in.ExpectedRevision)
	case "cancel":
		if in.SubjectID != nil || in.PlanID != nil || in.PlanHash != "" || in.TaskRevision != nil && *in.TaskRevision < 1 {
			return errs.BadRequest("INVALID_ACTION", "取消只需要当前版本")
		}
		err = service.Cancel(r.Context(), ws, conv, t.ID, in.ExpectedRevision, in.TaskRevision)
	default:
		return errs.BadRequest("INVALID_ACTION", "未知的会话操作")
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func validPlanHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) error {
	conv, err := pathID(r, "conversation")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	if err := s.conversations().Delete(r.Context(), ws, conv); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) conversationResult(w http.ResponseWriter, r *http.Request) error {
	conv, t, err := s.loadConversationTurn(r)
	if err != nil {
		return err
	}
	_, ws := principal(r)
	resolved, err := s.conversations().ReferenceTurn(r.Context(), ws, conv, t)
	if err != nil {
		return err
	}
	if resolved.ReportID == nil || resolved.ReportVersion == nil || resolved.TaskID == nil || resolved.ResearchVersion == nil {
		return errs.NotFound()
	}
	reportRequest := r.Clone(r.Context())
	reportRequest.SetPathValue("id", resolved.ReportID.String())
	reportRequest.SetPathValue("version", strconv.Itoa(*resolved.ReportVersion))
	_, doc, raw, err := s.loadDoc(reportRequest)
	if err != nil {
		return err
	}
	entities, err := s.Store.Entities(r.Context(), []uuid.UUID{doc.SubjectID})
	if err != nil {
		return err
	}
	claimByRef := map[string]harness.DocClaim{}
	for _, claim := range doc.Claims {
		claimByRef[claim.Ref] = claim
	}
	claims := []harness.DocClaim{}
	refs := doc.SummaryRefs
	if resolved.Brief {
		refs = []string{}
		for _, section := range doc.Sections {
			if section.ID != "S1" {
				refs = append(refs, section.ClaimRefs...)
			}
		}
	}
	for _, ref := range refs {
		if c, ok := claimByRef[ref]; ok {
			claims = append(claims, c)
		}
		if len(claims) == 3 {
			break
		}
	}
	tables := []facts.Table{}
	series := []facts.Series{}
	if resolved.Brief {
		for _, section := range doc.Sections {
			tables = append(tables, section.Tables...)
		}
		if err := json.Unmarshal(raw, &series); err != nil {
			return err
		}
		if series == nil {
			series = []facts.Series{}
		}
		if doc.SkillVersion == emquant.ConversationObservationVersion && len(series) == 0 {
			for _, table := range tables {
				if chart, ok := choiceConversationSeries(table); ok {
					series = append(series, chart)
					break
				}
			}
		}
	}
	grant, err := s.versionGrant(r, doc.SkillVersion)
	if err != nil {
		return err
	}
	return s.snapshotGrant(r, *resolved.TaskID, doc.SkillVersion, grant)(func() error {
		httpx.JSON(w, r, http.StatusOK, map[string]any{
			"report_id": doc.ID, "version": doc.Version, "title": doc.Title, "subject": toEntity(entities[doc.SubjectID], s.Engine.Skills),
			"skill_version": doc.SkillVersion, "data_as_of": doc.DataAsOf, "result_kind": doc.ResultKind, "synthetic": doc.Synthetic,
			"ai_generated": doc.AIGenerated, "claims": claims, "tables": tables, "series": series, "limitations": doc.Limitations, "sources": doc.Sources,
			"disclaimer": doc.Disclaimer, "is_brief": resolved.Brief, "original_turn_id": resolved.ID,
		})
		return nil
	})
}

func choiceConversationSeries(table facts.Table) (facts.Series, bool) {
	if table.Title != "Choice 日成交观察（非实时）" || len(table.Columns) < 3 || table.Columns[0] != "厂商返回日期" || table.Columns[2] != "成交额（万元）" {
		return facts.Series{}, false
	}
	if len(table.Rows) == 0 || len(table.Evidence) == 0 {
		return facts.Series{}, false
	}
	values := facts.SeriesLine{Name: "成交额"}
	for _, row := range table.Rows {
		if len(row) < 3 || row[0] == nil || row[2] == nil {
			return facts.Series{}, false
		}
		values.Points = append(values.Points, [2]*string{row[0], row[2]})
	}
	note := "日期为 Choice 返回的观察日期，未补齐缺失日期。"
	return facts.Series{
		ID: harness.DailyTradingTopic, Title: "成交额", Kind: "bar", X: "trade_date", Unit: "万元",
		Lines: []facts.SeriesLine{values}, Evidence: table.Evidence, Note: &note,
	}, true
}
