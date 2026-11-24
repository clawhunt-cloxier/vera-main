package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

// ---- me / onboarding ----

func (s *Server) meBody(r *http.Request) (Me, error) {
	p, ws := principal(r)
	var name string
	_ = s.Pool.QueryRow(r.Context(), `SELECT display_name FROM users WHERE id=$1`, p.UserID).Scan(&name)
	me := Me{UserID: p.UserID, WorkspaceID: p.WorkspaceID, CSRF: p.CSRF, Persona: p.Persona, Onboarded: p.WorkspaceID != nil,
		Synthetic: s.Synthetic, DisplayName: name}
	if s.FullVendorResearch {
		me.ClosingResearchAvailable = s.closeGrant(r)(noop) == nil
	}
	if !s.combined() || s.choiceGrant(r)(noop) == nil {
		quote, err := s.quoteCapabilities(r.Context())
		if err != nil {
			return me, err
		}
		if quote.Latest {
			me.ResearchProtocol = "v0.4"
			me.RealtimeTradeAvailable = quote.Realtime
		} else if s.Engine.Brief != nil {
			me.ResearchProtocol = "observation.v1"
		}
	}
	if ws != uuid.Nil {
		q, err := s.Engine.Quota(r.Context(), ws)
		if err != nil {
			return me, err
		}
		me.Quota = q
	}
	return me, nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	me, err := s.meBody(r)
	if err != nil {
		return err
	}
	httpx.JSON(w, r, 200, me)
	return nil
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Persona string `json:"persona"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, _ := principal(r)
	if err := s.ID.SetPersona(r.Context(), p, in.Persona); err != nil {
		return err
	}
	return s.me(w, r)
}

func (s *Server) onboard(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		InviteCode string `json:"invite_code"`
		Persona    string `json:"persona"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, _ := principal(r)
	if _, err := s.ID.Onboard(r.Context(), p, in.InviteCode, in.Persona); err != nil {
		return err
	}
	return s.me(w, r)
}

// ---- catalog ----

func (s *Server) searchEntities(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if kind != "" && !slices.Contains([]string{"stock", "industry", "market"}, kind) {
		return errs.BadRequest("INVALID_QUERY", "kind 无效")
	}
	items := []Entity{}
	if q == "" || len([]rune(q)) > 50 {
		httpx.JSON(w, r, 200, map[string]any{"items": items})
		return nil
	}

	ents, err := s.catalogFor(r).Search(r.Context(), q, kind, maxSearchEntities)
	if err != nil {
		return err
	}
	if s.choiceRequest(r) && (kind == "" || kind == "stock") {
		ents, err = choiceSearchResults(q, ents, func() ([]dataservice.EntityRow, error) {
			return s.searchVendorStocks(r, q, kind)
		})
		if err != nil {
			return errs.Unavailable("STOCK_SEARCH_UNAVAILABLE", "证券搜索暂时不可用，请稍后重试")
		}
	}
	registry := s.skillsFor(r)
	for _, e := range ents {
		items = append(items, toEntity(e, registry))
	}

	httpx.JSON(w, r, 200, map[string]any{"items": items})
	return nil
}

const maxSearchEntities = 20

func choiceSearchResults(q string, cached []dataservice.EntityRow, lookup func() ([]dataservice.EntityRow, error)) ([]dataservice.EntityRow, error) {
	qualified := emquantBriefCodeLike(strings.ToUpper(q))
	bareCode := isStockCode(q)
	exact := func(e dataservice.EntityRow) bool {
		return e.Kind == "stock" && (strings.EqualFold(e.Code, q) || strings.EqualFold(e.Name, q))
	}
	// A bare code requires the vendor's exchange candidates. For a qualified
	// code only its exact match is relevant, including when the cache is fuzzy.
	if qualified || bareCode {
		cached = slices.DeleteFunc(cached, func(e dataservice.EntityRow) bool {
			return bareCode || e.Kind != "stock" || !strings.EqualFold(e.Code, q)
		})
	}
	all := cached
	if !slices.ContainsFunc(cached, exact) && (bareCode || qualified || isStockName(q)) {
		discovered, err := lookup()
		if err != nil {
			return nil, err
		}
		all = slices.Concat(discovered, cached)
	}
	result := make([]dataservice.EntityRow, 0, min(len(all), maxSearchEntities))
	seen := make(map[uuid.UUID]bool)
	for _, wantExact := range []bool{true, false} {
		for _, e := range all {
			if exact(e) == wantExact && !seen[e.ID] {
				seen[e.ID] = true
				result = append(result, e)
				if len(result) == maxSearchEntities {
					return result, nil
				}
			}
		}
	}
	return result, nil
}

// isStockCode returns true for 6-digit numeric strings (A-share code without exchange suffix).
func isStockCode(q string) bool {
	if len(q) != 6 {
		return false
	}
	for _, ch := range q {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// isStockName returns true for strings that contain letter/CJK characters and only valid name chars.
func isStockName(q string) bool {
	if len([]rune(q)) > 32 {
		return false
	}
	hasLetter := false
	for _, ch := range q {
		hasLetter = hasLetter || unicode.IsLetter(ch)
		if !unicode.IsLetter(ch) && !unicode.IsNumber(ch) && !strings.ContainsRune(" *.-·()（）", ch) {
			return false
		}
	}
	return hasLetter
}

// searchVendorStocks calls the Choice API for the query, upserts discovered entities, and
// returns the matched EntityRows. Only searches stocks; industry/market have no equivalent.
func (s *Server) searchVendorStocks(r *http.Request, q, kind string) ([]dataservice.EntityRow, error) {
	if kind != "" && kind != "stock" {
		return nil, nil
	}
	runtime := s.Engine.Brief.Runtime
	if runtime == nil {
		return nil, errors.New("stock search runtime unavailable")
	}

	var table emquant.Table
	colName, colCode := -1, -1
	queryCode := strings.ToUpper(q)
	qualified := emquantBriefCodeLike(queryCode)
	codeLookup := qualified || isStockCode(q)

	switch {
	case codeLookup:
		t, _, err := runtime.CollectStockCodeCandidates(r.Context(), queryCode[:6])
		if err != nil {
			return nil, err
		}
		table = t
		for i, col := range table.Columns {
			if strings.EqualFold(col, "FULLCODES") {
				colCode = i
			}
		}
	case isStockName(q):
		t, _, err := runtime.CollectStockNameCandidates(r.Context(), q)
		if err != nil {
			return nil, err
		}
		table = t
		for i, col := range table.Columns {
			switch strings.ToUpper(col) {
			case "SECURITYSHORTNAME":
				colName = i
			case "SECUCODE":
				colCode = i
			}
		}
	default:
		return nil, nil
	}

	if table.Rows == 0 {
		return nil, nil
	}
	if len(table.Columns) == 0 || colCode < 0 || (!codeLookup && colName < 0) || table.Rows != len(table.Values)/len(table.Columns) || len(table.Values)%len(table.Columns) != 0 {
		return nil, emquant.ErrShape
	}

	numCols := len(table.Columns)
	var out []dataservice.EntityRow
	seen := map[string]bool{}

	for row := 0; row < table.Rows; row++ {
		base := row * numCols
		var name string
		if colName >= 0 && base+colName < len(table.Values) {
			name, _ = table.Values[base+colName].Value.(string)
		}
		rawCode, _ := table.Values[base+colCode].Value.(string)
		for _, code := range matchedSearchCodes(rawCode, queryCode, codeLookup) {
			if seen[code] {
				continue
			}
			seen[code] = true
			label := name
			if label == "" {
				label = code
			}
			e, err := s.catalogFor(r).UpsertSearchEntity(r.Context(), "stock", code, label, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func matchedSearchCodes(raw, query string, codeLookup bool) []string {
	var codes []string
	for _, part := range strings.Split(raw, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if emquantBriefCodeLike(code) && (!codeLookup || query == code || query == code[:6]) {
			codes = append(codes, code)
		}
	}
	return codes
}

// emquantBriefCodeLike checks that a code looks like a qualified A-share code (NNNNNN.XX).
func emquantBriefCodeLike(code string) bool {
	parts := strings.SplitN(code, ".", 2)
	if len(parts) != 2 || len(parts[0]) != 6 {
		return false
	}
	for _, ch := range parts[0] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	switch parts[1] {
	case "SH", "SZ", "BJ":
		return true
	}
	return false
}

func (s *Server) skills(w http.ResponseWriter, r *http.Request) error {
	paused := harness.KillSwitch(r.Context(), s.Pool)
	items := []Skill{}
	registry := s.skillsFor(r)
	choice := s.choiceRequest(r)
	for _, id := range skillOrder {
		sk := registry[id]
		// Choice protocols build single-stock plans. On a combined deployment,
		// ordinary JSON requests use the close research registry instead.
		available := !paused && (!choice || id == "stock_analysis")
		items = append(items, Skill{ID: sk.ID, Version: sk.Version, Title: sk.Title, SubjectKinds: sk.Subjects, ParamsSchema: sk.Schema, Available: available})
	}
	httpx.JSON(w, r, 200, map[string]any{"items": items})
	return nil
}

// ---- plans ----

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) error {
	if s.Engine.Brief != nil && !s.FullVendorResearch && !realtimeVersion(r) && !observationVersion(r) {
		return errs.New(406, "REPRESENTATION_UNAVAILABLE", "真实简报需要新版客户端")
	}
	var in struct {
		Text             *string         `json:"text"`
		SkillID          string          `json:"skill_id"`
		SubjectID        *uuid.UUID      `json:"subject_id"`
		Params           json.RawMessage `json:"params"`
		QuoteRequirement json.RawMessage `json:"quote_requirement"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if realtimeVersion(r) && len(in.QuoteRequirement) == 0 {
		// The current product policy is latest available market data. The
		// explicit field remains readable for old clients that requested
		// realtime_trade, but new clients do not need to choose a requirement.
		in.QuoteRequirement = json.RawMessage(`"latest_available"`)
	}
	if !realtimeVersion(r) && len(in.QuoteRequirement) != 0 {
		return errs.BadRequest("INVALID_BODY", "旧版请求不支持 quote_requirement，请使用 0.4 协议")
	}
	var requirement string
	if realtimeVersion(r) && (json.Unmarshal(in.QuoteRequirement, &requirement) != nil || (requirement != "realtime_trade" && requirement != "latest_available")) {
		return errs.Unprocessable("INVALID_QUOTE_REQUIREMENT", "quote_requirement 必须是 realtime_trade 或 latest_available")
	}
	_, ws := principal(r)
	var res *harness.ParseResult
	switch {
	case in.Text != nil && in.SubjectID == nil && in.SkillID == "" && len(in.Params) == 0:
		t := strings.TrimSpace(*in.Text)
		if n := len([]rune(t)); n < 2 || n > 500 {
			return errs.BadRequest("INVALID_BODY", "请输入 2–500 字")
		}
		var err error
		switch {
		case realtimeVersion(r):
			res, err = s.Engine.ParseRealtimePlan(r.Context(), ws, t, requirement)
		case observationVersion(r):
			res, err = s.Engine.ParseObservationPlan(r.Context(), ws, t)
		default:
			res, err = s.Engine.ParsePlan(r.Context(), ws, t)
		}
		if err != nil {
			return err
		}
	case in.Text == nil && in.SubjectID != nil && in.SkillID != "" && len(in.Params) > 0 && string(in.Params) != "null":
		ents, err := s.catalogFor(r).Entities(r.Context(), []uuid.UUID{*in.SubjectID})
		if err != nil {
			return err
		}
		sub, ok := ents[*in.SubjectID]
		if !ok {
			return errs.Unprocessable("UNKNOWN_SUBJECT", "研究对象不存在")
		}
		params := map[string]any{}
		if err := json.Unmarshal(in.Params, &params); err != nil || params == nil {
			return errs.BadRequest("INVALID_BODY", "params 必须是对象")
		}
		var p *harness.Plan
		switch {
		case realtimeVersion(r):
			p, err = s.Engine.BuildRealtimePlan(r.Context(), ws, in.SkillID, sub, params, requirement)
		case observationVersion(r):
			p, err = s.Engine.BuildObservationPlan(r.Context(), ws, in.SkillID, sub, params)
		default:
			p, err = s.Engine.BuildPlan(r.Context(), ws, in.SkillID, sub, params)
		}
		if err != nil {
			return err
		}
		res = &harness.ParseResult{Kind: "plan", Plan: p, Candidates: []harness.Candidate{}, Suggestions: []string{}}
	default:
		return errs.BadRequest("INVALID_BODY", "请提供 text，或 skill_id + subject_id + params")
	}
	out := PlanResult{Kind: res.Kind, UnsupportedReason: res.Unsupported, Suggestions: res.Suggestions, Candidates: []Candidate{}}
	if out.Suggestions == nil {
		out.Suggestions = []string{}
	}
	if res.Plan != nil {
		pl := s.toPlan(res.Plan)
		out.Plan = &pl
	}
	for _, c := range res.Candidates {
		out.Candidates = append(out.Candidates, Candidate{SkillID: c.SkillID, Subject: toEntity(c.Subject, s.skillsFor(r)), Label: c.Label})
	}
	httpx.JSON(w, r, 200, out)
	return nil
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	p, err := s.Engine.LoadPlan(r.Context(), ws, id)
	if err != nil {
		return err
	}
	if !slices.Contains(s.historyVersions(), p.SkillVersion) {
		return errs.NotFound()
	}
	if p.SnapshotMode == "collect_latest" && p.QuoteRequirement != nil && !realtimeVersion(r) {
		return errs.New(406, "REPRESENTATION_UNAVAILABLE", "该计划需要 0.4 客户端")
	}
	if p.SnapshotMode == "collect_latest" && p.QuoteRequirement == nil && !observationVersion(r) && !realtimeVersion(r) {
		return errs.New(406, "REPRESENTATION_UNAVAILABLE", "该计划需要新版客户端")
	}
	httpx.JSON(w, r, 200, s.toPlan(p))
	return nil
}

// ---- tasks ----

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PlanID   uuid.UUID `json:"plan_id"`
		PlanHash string    `json:"plan_hash"`
		Disabled []string  `json:"disabled_sections"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Disabled) > 10 {
		return errs.BadRequest("INVALID_BODY", "disabled_sections 过多")
	}
	_, ws := principal(r)
	plan, err := s.Engine.LoadPlan(r.Context(), ws, in.PlanID)
	if err != nil {
		return err
	}
	if !slices.Contains(s.historyVersions(), plan.SkillVersion) {
		return errs.NotFound()
	}
	if !planConfirmationVersion(plan.SnapshotMode, plan.QuoteRequirement, representation(r)) {
		return errs.Conflict("PLAN_VERSION_MISMATCH", "确认协议必须与创建计划的协议一致")
	}
	id, err := s.Engine.CreateTask(r.Context(), ws, in.PlanID, in.PlanHash, in.Disabled)
	if err != nil {
		return err
	}
	t, err := s.task(r, ws, id)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/research-tasks/"+id.String())
	httpx.JSON(w, r, http.StatusAccepted, t)
	return nil
}

const taskCols = `t.id, t.skill_id, t.subject_id, t.status, t.revision, t.created_at, t.deadline, t.as_of, t.report_id, t.reason_code, t.reason_message, t.epoch, t.snapshot_mode, t.snapshot_as_of, p.quote_requirement`

type taskRow struct {
	t       Task
	subject uuid.UUID
	report  *uuid.UUID
	epoch   int64
}

func scanTask(row pgx.Row) (taskRow, error) {
	var tr taskRow
	err := row.Scan(&tr.t.ID, &tr.t.SkillID, &tr.subject, &tr.t.Status, &tr.t.Revision, &tr.t.CreatedAt, &tr.t.Deadline, &tr.t.AsOf, &tr.report,
		&tr.t.ReasonCode, &tr.t.ReasonMessage, &tr.epoch, &tr.t.SnapshotMode, &tr.t.SnapshotAsOf, &tr.t.QuoteRequirement)
	return tr, err
}

func (s *Server) fillTasks(r *http.Request, ws uuid.UUID, rows []taskRow, withSteps bool) ([]Task, error) {
	var ids []uuid.UUID
	for _, tr := range rows {
		ids = append(ids, tr.subject)
	}
	ents, err := s.Store.Entities(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		for _, tr := range rows {
			t := tr.t
			if t.SnapshotMode == "collect_latest" && t.QuoteRequirement != nil && !realtimeVersion(r) {
				return errs.New(406, "REPRESENTATION_UNAVAILABLE", "该任务需要 0.4 客户端")
			}
			if t.SnapshotMode == "collect_latest" && t.QuoteRequirement == nil && !observationVersion(r) && !realtimeVersion(r) {
				return errs.New(406, "REPRESENTATION_UNAVAILABLE", "该任务需要新版客户端")
			}
			if _, ok := ents[tr.subject]; !ok {
				continue
			}
			t.Subject = toEntity(ents[tr.subject], s.Engine.Skills)
			t.Steps = []TaskStep{}
			if tr.report != nil {
				t.ReportRef = &ReportRef{ID: *tr.report, Version: 1}
			}
			if withSteps {
				st, err := tx.Query(r.Context(), `SELECT step_key, label, status FROM task_steps WHERE task_id=$1 AND epoch=$2 ORDER BY id`, t.ID, tr.epoch)
				if err != nil {
					return err
				}
				for st.Next() {
					var x TaskStep
					if err := st.Scan(&x.Key, &x.Label, &x.Status); err != nil {
						st.Close()
						return err
					}
					t.Steps = append(t.Steps, x)
				}
				st.Close()
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}

func (s *Server) task(r *http.Request, ws, id uuid.UUID) (Task, error) {
	var tr taskRow
	err := db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		var err error
		tr, err = scanTask(tx.QueryRow(r.Context(), `SELECT `+taskCols+` FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id WHERE t.id=$1 AND t.skill_version=ANY($2::text[])`, id, s.historyVersions()))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, errs.NotFound()
	}
	if err != nil {
		return Task{}, err
	}
	ts, err := s.fillTasks(r, ws, []taskRow{tr}, true)
	if err != nil {
		return Task{}, err
	}
	if len(ts) == 0 {
		return Task{}, errs.NotFound()
	}
	return ts[0], nil
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	t, err := s.task(r, ws, id)
	if err != nil {
		return err
	}
	if !harness.IsTerminal(t.Status) {
		w.Header().Set("Retry-After", "2")
	}
	httpx.JSON(w, r, 200, t)
	return nil
}

type cursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

func parseCursor(s string) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return nil, errs.BadRequest("INVALID_CURSOR", "cursor 无效")
	}
	return &c, nil
}

func encodeCursor(t time.Time, id uuid.UUID) *string {
	b, _ := json.Marshal(cursor{t, id})
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func pageSize(r *http.Request) (int, error) {
	v := r.URL.Query().Get("page_size")
	if v == "" {
		return 20, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 100 {
		return 0, errs.BadRequest("INVALID_QUERY", "page_size 为 1–100")
	}
	return n, nil
}

func optUUID(r *http.Request, k string) (*uuid.UUID, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, errs.BadRequest("INVALID_QUERY", k+" 无效")
	}
	return &id, nil
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) error {
	_, ws := principal(r)
	n, err := pageSize(r)
	if err != nil {
		return err
	}
	cur, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	subj, err := optUUID(r, "subject_id")
	if err != nil {
		return err
	}
	var at *time.Time
	var cid *uuid.UUID
	if cur != nil {
		at, cid = &cur.At, &cur.ID
	}
	skill, status := r.URL.Query().Get("skill_id"), r.URL.Query().Get("status")
	var rows []taskRow
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT `+taskCols+` FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id WHERE ($1::timestamptz IS NULL OR (t.created_at, t.id) < ($1, $2))
			AND ($3 = '' OR t.skill_id=$3) AND ($4 = '' OR t.status=$4) AND ($5::uuid IS NULL OR t.subject_id=$5)
			AND t.skill_version=ANY($7::text[]) ORDER BY t.created_at DESC, t.id DESC LIMIT $6`, at, cid, skill, status, subj, n+1, s.listVersions(r))
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			tr, err := scanTask(rs)
			if err != nil {
				return err
			}
			rows = append(rows, tr)
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	var next *string
	if len(rows) > n {
		rows = rows[:n]
		last := rows[n-1].t
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	items, err := s.fillTasks(r, ws, rows, false)
	if err != nil {
		return err
	}
	httpx.JSON(w, r, 200, map[string]any{"items": items, "next_cursor": next})
	return nil
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Expected int64 `json:"expected_revision"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, ws := principal(r)
	if err := db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM research_tasks WHERE id=$1 AND skill_version=ANY($2::text[]))`, id, s.historyVersions()).Scan(&exists)
		if err == nil && !exists {
			return errs.NotFound()
		}
		return err
	}); err != nil {
		return err
	}
	if _, _, err := s.Engine.CancelTask(r.Context(), ws, id, in.Expected); err != nil {
		return err
	}
	if s.Engine.Brief != nil || s.FullVendorResearch {
		// No vendor-derived fields are needed to acknowledge this command.
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	t, err := s.task(r, ws, id)
	if err != nil {
		return err
	}
	code := http.StatusAccepted
	if t.Status == "CANCELLED" {
		code = http.StatusOK
	}
	httpx.JSON(w, r, code, t)
	return nil
}

// ---- reports ----

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) error {
	_, ws := principal(r)
	n, err := pageSize(r)
	if err != nil {
		return err
	}
	cur, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	subj, err := optUUID(r, "subject_id")
	if err != nil {
		return err
	}
	var at *time.Time
	var cid *uuid.UUID
	if cur != nil {
		at, cid = &cur.At, &cur.ID
	}
	items, err := s.reportSummaries(r, ws, `($1::timestamptz IS NULL OR (delivered_at, report_id) < ($1, $2)) AND ($3 = '' OR skill_id=$3)
		AND ($4::uuid IS NULL OR subject_id=$4) ORDER BY delivered_at DESC, report_id DESC LIMIT $5`, at, cid, r.URL.Query().Get("skill_id"), subj, n+1)
	if err != nil {
		return err
	}
	var next *string
	if len(items) > n {
		items = items[:n]
		next = encodeCursor(items[n-1].DeliveredAt, items[n-1].ID)
	}
	httpx.JSON(w, r, 200, map[string]any{"items": items, "next_cursor": next})
	return nil
}

func (s *Server) reportSummaries(r *http.Request, ws uuid.UUID, where string, args ...any) ([]ReportSummary, error) {
	type row struct {
		ReportSummary
		subject uuid.UUID
	}
	var rows []row
	err := db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		scope := fmt.Sprintf(`EXISTS (SELECT 1 FROM research_tasks t WHERE t.id=v.task_id AND t.workspace_id=v.workspace_id AND t.skill_version=ANY($%d::text[]))`, len(args)+1)
		rs, err := tx.Query(r.Context(), `SELECT report_id, version, title, skill_id, subject_id, result_kind, delivered_at FROM report_versions v WHERE `+scope+` AND `+where, append(args, s.listVersions(r))...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var x row
			if err := rs.Scan(&x.ID, &x.Version, &x.Title, &x.SkillID, &x.subject, &x.ResultKind, &x.DeliveredAt); err != nil {
				return err
			}
			rows = append(rows, x)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, x := range rows {
		ids = append(ids, x.subject)
	}
	ents, err := s.Store.Entities(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	out := []ReportSummary{}
	for _, x := range rows {
		if _, ok := ents[x.subject]; !ok {
			continue
		}
		x.Subject = toEntity(ents[x.subject], s.Engine.Skills)
		out = append(out, x.ReportSummary)
	}
	return out, nil
}

func (s *Server) loadDoc(r *http.Request) (uuid.UUID, harness.Document, []byte, error) {
	var doc harness.Document
	id, err := pathID(r, "id")
	if err != nil {
		return uuid.Nil, doc, nil, err
	}
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || v < 1 {
		return uuid.Nil, doc, nil, errs.NotFound()
	}
	_, ws := principal(r)
	var body, series []byte
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT v.document, v.series FROM report_versions v JOIN research_tasks t ON t.id=v.task_id AND t.workspace_id=v.workspace_id WHERE v.report_id=$1 AND v.version=$2 AND t.skill_version=ANY($3::text[])`, id, v, s.historyVersions()).Scan(&body, &series)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, doc, nil, errs.NotFound()
	}
	if err != nil {
		return uuid.Nil, doc, nil, err
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return uuid.Nil, doc, nil, err
	}
	if err := formatDailyTradeTables(&doc); err != nil {
		return uuid.Nil, doc, nil, err
	}
	if doc.SnapshotMode == "" {
		doc.SnapshotMode = "fixed_legacy"
		if doc.SkillVersion == emquant.ObservationBriefVersion {
			doc.SnapshotMode = "collect_latest"
			doc.SnapshotAsOf = &doc.AsOf
		}
	}
	// Old observation documents predate source_freshness. Read their exact
	// frozen manifest for 0.4 display without rewriting the stored document or
	// inventing event/publication/availability timestamps.
	if realtimeVersion(r) && doc.SkillVersion == emquant.ObservationBriefVersion && len(doc.SourceFreshness) == 0 {
		if s.Engine.Brief == nil {
			return uuid.Nil, doc, nil, errs.Unavailable("SOURCE_UNAVAILABLE", "真实报告来源当前不可读取")
		}
		if err := s.Engine.Brief.WithSnapshot(r.Context(), ws, doc.TaskID, dataservice.SourceDisplay, func(o emquant.BriefObservation) error {
			doc.SnapshotAsOf = &o.Cutoff
			doc.SourceFreshness = harness.ObservationFreshness(o, s.Engine.Brief.ProviderID)
			return nil
		}); err != nil {
			return uuid.Nil, doc, nil, err
		}
	}
	return ws, doc, series, nil
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) error {
	_, d, _, err := s.loadDoc(r)
	if err != nil {
		return err
	}
	ents, err := s.Store.Entities(r.Context(), []uuid.UUID{d.SubjectID})
	if err != nil {
		return err
	}
	mode := d.SnapshotMode
	if mode == "" {
		mode = "fixed_legacy"
	}
	httpx.JSON(w, r, 200, Report{SnapshotMode: mode, SnapshotAsOf: d.SnapshotAsOf, SourceFreshness: d.SourceFreshness, ID: d.ID, Version: d.Version, TaskID: d.TaskID, Title: d.Title, SkillID: d.SkillID, SkillVersion: d.SkillVersion,
		Subject: toEntity(ents[d.SubjectID], s.Engine.Skills), AsOf: d.AsOf, DataAsOf: d.DataAsOf, ResultKind: d.ResultKind, Synthetic: d.Synthetic,
		AIGenerated: true, SummaryRefs: d.SummaryRefs, Sections: d.Sections, Claims: d.Claims, Limitations: d.Limitations, Sources: d.Sources,
		Disclaimer: d.Disclaimer, DeliveredAt: d.DeliveredAt})
	return nil
}

func (s *Server) getSeries(w http.ResponseWriter, r *http.Request) error {
	_, _, raw, err := s.loadDoc(r)
	if err != nil {
		return err
	}
	var series []json.RawMessage
	_ = json.Unmarshal(raw, &series)
	want := r.PathValue("series_id")
	for _, x := range series {
		var head struct {
			ID string `json:"series_id"`
		}
		if json.Unmarshal(x, &head) == nil && head.ID == want {
			w.Header().Set("Content-Type", representation(r)+"; charset=utf-8")
			_, _ = w.Write(x)
			return nil
		}
	}
	return errs.NotFound()
}

func (s *Server) feedback(w http.ResponseWriter, r *http.Request) error {
	ws, d, _, err := s.loadDoc(r)
	if err != nil {
		return err
	}
	var in struct {
		Kind     string  `json:"kind"`
		Comment  string  `json:"comment"`
		ClaimRef *string `json:"claim_ref"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !slices.Contains([]string{"useful", "confusing", "incorrect", "other"}, in.Kind) || len([]rune(in.Comment)) > 2000 {
		return errs.Unprocessable("INVALID_FEEDBACK", "反馈类型或内容无效")
	}
	if in.ClaimRef != nil && !slices.ContainsFunc(d.Claims, func(c harness.DocClaim) bool { return c.Ref == *in.ClaimRef }) {
		return errs.Unprocessable("INVALID_FEEDBACK", "结论编号不存在")
	}
	p, _ := principal(r)
	var out struct {
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
	}
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `INSERT INTO feedback(workspace_id,user_id,report_id,version,kind,claim_ref,comment) VALUES($1,$2,$3,$4,$5,$6,$7)
			RETURNING id, created_at`, ws, p.UserID, d.ID, d.Version, in.Kind, in.ClaimRef, in.Comment).Scan(&out.ID, &out.CreatedAt)
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, r, http.StatusCreated, out)
	return nil
}

// ---- evidence & calculations: visible only when used by a task of this workspace ----

func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	// A published TuShare record may also be present in a standalone close
	// task. That task grants its own path, independently of Choice.
	var version string
	var task uuid.UUID
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT t.id,t.skill_version FROM research_tasks t JOIN snapshots sn ON sn.hash = t.snapshot_hash
			WHERE $1 = ANY(sn.record_ids) AND t.skill_version=ANY($2::text[]) AND t.skill_version=ANY($3::text[])
			AND (t.skill_version<>$4 OR t.status='COMPLETED')
			ORDER BY (t.skill_version=$4),t.id LIMIT 1`, id, s.historyVersions(),
			[]string{"1.0.0", harness.CloseResearchVersion, harness.CloseCompanyVersion, harness.CloseDisclosureVersion, harness.CloseUnifiedVersion}, harness.CloseUnifiedVersion).Scan(&task, &version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if handled, err := s.supplementEvidence(w, r, ws, id); handled || err != nil {
			return err
		}
		if s.Engine.Brief != nil {
			if handled, err := s.briefEvidence(w, r); handled || err != nil {
				return err
			}
		}
		return errs.NotFound()
	}
	if err != nil {
		return err
	}
	grant, err := s.versionGrant(r, version)
	if err != nil {
		return err
	}
	if version == harness.CloseUnifiedVersion {
		grant = s.snapshotGrant(r, task, version, grant)
	}
	// Query before taking the write-time grant to keep the transaction short.
	recs, err := s.Store.Get(r.Context(), []uuid.UUID{id})
	if err != nil {
		return err
	}
	rec, found := recs[id]
	if !found || rec.ProviderID != s.historyRecordProvider() {
		return errs.NotFound()
	}
	ev := s.evidence(r, rec)
	return grant(func() error { httpx.JSON(w, r, 200, ev); return nil })
}

func (s *Server) evidence(r *http.Request, rec dataservice.StoredRecord) Evidence {
	cat := s.Store.Catalog()
	ds, _ := cat.Dataset(rec.DatasetID)
	title := rec.DatasetID
	if ds != nil {
		title = ds.Title
	}
	var provider string
	_ = s.Pool.QueryRow(r.Context(), `SELECT name FROM providers WHERE id=$1`, rec.ProviderID).Scan(&provider)
	if rec.EntityID != nil {
		if ents, err := s.Store.Entities(r.Context(), []uuid.UUID{*rec.EntityID}); err == nil {
			title = ents[*rec.EntityID].Name + " · " + title
		}
	}
	ev := Evidence{ID: rec.ID, DatasetID: rec.DatasetID, Provider: provider, Origin: rec.Origin, SourceTier: rec.SourceTier, Title: title,
		Label: title, PublishedAt: rec.PublishedAt, AvailableAt: rec.AvailableAt, ContentHash: rec.ContentHash}
	if rec.Basis != "" {
		b := rec.Basis
		ev.Basis = &b
	}
	if rec.Period != nil {
		p := rec.Period.Format("2006-01-02")
		ev.Period = &p
	}
	str := func(k string) string { v, _ := rec.Payload[k].(string); return v }
	switch rec.DatasetID {
	case "fin.statement":
		if it, ok := cat.Item(str("item_code")); ok {
			ev.Label = it.Label + "（" + str("scope") + "，" + str("report_type") + "）"
		}
		v, u := str("value"), "元"
		if str("item_code") == "IS.basic_eps" || str("item_code") == "IS.diluted_eps" {
			u = "元/股"
		}
		if v != "" {
			ev.Value, ev.Unit = &v, &u
		}
		loc := "公告 " + str("filing_id")
		if revision := rec.Payload["revision"]; revision != nil {
			loc += fmt.Sprintf("，公告更正序号 %v", revision)
		}
		ev.Locator = &loc
	case "doc.periodic_text":
		ev.Label = str("heading_path")
		ex := clip(str("text"), 1000)
		ev.Excerpt = &ex
		loc := fmt.Sprintf("%s 第 %v 页", str("announcement_id"), rec.Payload["page"])
		ev.Locator = &loc
	case "news.item":
		ev.Label = str("title")
		ex := clip(str("body"), 1000)
		ev.Excerpt = &ex
		src := str("source")
		ev.Locator = &src
	case "doc.announcement":
		ev.Label = str("title")
		loc := str("announcement_id")
		ev.Locator = &loc
	default:
		var parts []string
		if ds != nil {
			for _, f := range ds.Fields {
				if v, ok := rec.Payload[f.Name]; ok && v != nil {
					parts = append(parts, evidenceField(rec.DatasetID, f, v))
				}
			}
		}
		ex := clip(strings.Join(parts, "；"), 1000)
		ev.Excerpt = &ex
		if rec.ProviderID == "tushare" {
			var location string
			switch rec.DatasetID {
			case "flow.dragon_tiger_day":
				location = "龙虎榜日查询 · " + str("trade_date")
			case "flow.dragon_tiger":
				board := "买入榜"
				if str("side") == "sell" {
					board = "卖出榜"
				}
				location = strings.Join([]string{str("trade_date"), str("reason_text"), board, str("seat_name")}, " · ")
			case "fund.disclosed_position":
				location = str("fund_code") + " · " + str("period_end") + " 持仓 · " + str("announcement_date") + " 公告"
			case "corp.dividend":
				if announced := str("implementation_date"); announced != "" {
					location = "分红实施公告 · " + announced
				}
			}
			if location != "" {
				location = clip(location, 300)
				ev.Locator = &location
			}
		}
	}
	return ev
}

func (s *Server) getCalculation(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	var c Calculation
	var inputs []byte
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT c.id, c.formula_id, c.formula_version, c.inputs, c.result::text, c.unit, c.applicability, c.reason FROM calculations c JOIN research_tasks t ON t.id=c.task_id AND t.workspace_id=c.workspace_id WHERE c.id=$1 AND t.skill_version=ANY($2::text[])`, id, s.historyVersions()).
			Scan(&c.ID, &c.FormulaID, &c.FormulaVersion, &inputs, &c.Result, &c.Unit, &c.Applicability, &c.Reason)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.NotFound()
	}
	if err != nil {
		return err
	}
	c.Inputs = []CalcInput{}
	_ = json.Unmarshal(inputs, &c.Inputs)
	httpx.JSON(w, r, 200, c)
	return nil
}
