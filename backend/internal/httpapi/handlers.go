package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	if s.Engine.Brief != nil {
		me.ResearchProtocol = "observation.v1"
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

	// Try local store first (previously cached vendor results and demo data)
	ents, err := s.Store.Search(r.Context(), q, kind, 20)
	if err != nil {
		return err
	}
	for _, e := range ents {
		items = append(items, toEntity(e, s.Engine.Skills))
	}

	// If local search found nothing and we have a real vendor, query Choice in real-time
	if len(items) == 0 && s.Engine.Brief != nil {
		if vendorEnts, err := s.searchVendorStocks(r, q, kind); err != nil {
			slog.Warn("vendor stock search failed", "query", q, "err", err)
		} else {
			for _, e := range vendorEnts {
				items = append(items, toEntity(e, s.Engine.Skills))
			}
		}
	}

	httpx.JSON(w, r, 200, map[string]any{"items": items})
	return nil
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
	for _, ch := range q {
		if !unicode.IsLetter(ch) && !unicode.IsNumber(ch) && !strings.ContainsRune(" *.-·()（）", ch) {
			return false
		}
	}
	return true
}

// searchVendorStocks calls the Choice API for the query, upserts discovered entities, and
// returns the matched EntityRows. Only searches stocks; industry/market have no equivalent.
func (s *Server) searchVendorStocks(r *http.Request, q, kind string) ([]dataservice.EntityRow, error) {
	if kind != "" && kind != "stock" {
		return nil, nil
	}
	runtime := s.Engine.Brief.Runtime
	if runtime == nil {
		return nil, nil
	}

	var table emquant.Table
	var colName, colCode int = -1, -1

	if isStockCode(q) {
		t, _, err := runtime.CollectStockCodeCandidates(r.Context(), q)
		if err != nil {
			return nil, err
		}
		table = t
		for i, col := range table.Columns {
			if strings.ToUpper(col) == "FULLCODES" {
				colCode = i
			}
		}
		if colCode == -1 && len(table.Columns) > 0 {
			colCode = 0
		}
	} else if isStockName(q) {
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
	} else {
		return nil, nil
	}

	if table.Rows == 0 || len(table.Columns) == 0 {
		return nil, nil
	}

	numCols := len(table.Columns)
	var out []dataservice.EntityRow
	seen := map[string]bool{}

	for row := 0; row < table.Rows; row++ {
		base := row * numCols
		var name, code string
		if colName >= 0 && base+colName < len(table.Values) {
			name, _ = table.Values[base+colName].Value.(string)
		}
		if colCode >= 0 && base+colCode < len(table.Values) {
			rawCode, _ := table.Values[base+colCode].Value.(string)
			if idx := strings.Index(rawCode, ","); idx >= 0 {
				rawCode = rawCode[:idx]
			}
			code = strings.TrimSpace(rawCode)
		}
		if name == "" && code != "" {
			name = strings.Split(code, ".")[0]
		}
		if code == "" || seen[code] || !emquantBriefCodeLike(code) {
			continue
		}
		seen[code] = true
		e, err := s.Store.UpsertSearchEntity(r.Context(), "stock", code, name, nil)
		if err != nil {
			slog.Warn("failed to upsert vendor search entity", "code", code, "err", err)
			continue
		}
		out = append(out, e)
	}
	return out, nil
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
	for _, id := range skillOrder {
		sk := s.Engine.Skills[id]
		items = append(items, Skill{ID: sk.ID, Version: sk.Version, Title: sk.Title, SubjectKinds: sk.Subjects, ParamsSchema: sk.Schema, Available: !paused && (s.Engine.Brief == nil || id == "stock_analysis")})
	}
	httpx.JSON(w, r, 200, map[string]any{"items": items})
	return nil
}

// ---- plans ----

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) error {
	if s.Engine.Brief != nil && !observationVersion(r) {
		return errs.New(406, "REPRESENTATION_UNAVAILABLE", "真实简报需要新版客户端")
	}
	var in struct {
		Text      *string         `json:"text"`
		SkillID   string          `json:"skill_id"`
		SubjectID *uuid.UUID      `json:"subject_id"`
		Params    json.RawMessage `json:"params"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, ws := principal(r)
	var res *harness.ParseResult
	switch {
	case in.Text != nil && in.SubjectID == nil && in.SkillID == "":
		t := strings.TrimSpace(*in.Text)
		if n := len([]rune(t)); n < 2 || n > 500 {
			return errs.BadRequest("INVALID_BODY", "请输入 2–500 字")
		}
		var err error
		if res, err = s.Engine.ParsePlan(r.Context(), ws, t); err != nil {
			return err
		}
	case in.Text == nil && in.SubjectID != nil && in.SkillID != "":
		ents, err := s.Store.Entities(r.Context(), []uuid.UUID{*in.SubjectID})
		if err != nil {
			return err
		}
		sub, ok := ents[*in.SubjectID]
		if !ok {
			return errs.Unprocessable("UNKNOWN_SUBJECT", "研究对象不存在")
		}
		params := map[string]any{}
		if len(in.Params) > 0 && string(in.Params) != "null" {
			if err := json.Unmarshal(in.Params, &params); err != nil {
				return errs.BadRequest("INVALID_BODY", "params 必须是对象")
			}
		}
		p, err := s.Engine.BuildPlan(r.Context(), ws, in.SkillID, sub, params)
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
		out.Candidates = append(out.Candidates, Candidate{SkillID: c.SkillID, Subject: toEntity(c.Subject, s.Engine.Skills), Label: c.Label})
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
	if p.SnapshotMode == "collect_latest" && !observationVersion(r) {
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
	if plan.SnapshotMode == "collect_latest" && !observationVersion(r) {
		return errs.New(406, "REPRESENTATION_UNAVAILABLE", "该计划需要新版客户端确认")
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

const taskCols = `t.id, t.skill_id, t.subject_id, t.status, t.revision, t.created_at, t.deadline, t.as_of, t.report_id, t.reason_code, t.reason_message, t.epoch, t.snapshot_mode, t.snapshot_as_of`

type taskRow struct {
	t       Task
	subject uuid.UUID
	report  *uuid.UUID
	epoch   int64
}

func scanTask(row pgx.Row) (taskRow, error) {
	var tr taskRow
	err := row.Scan(&tr.t.ID, &tr.t.SkillID, &tr.subject, &tr.t.Status, &tr.t.Revision, &tr.t.CreatedAt, &tr.t.Deadline, &tr.t.AsOf, &tr.report,
		&tr.t.ReasonCode, &tr.t.ReasonMessage, &tr.epoch, &tr.t.SnapshotMode, &tr.t.SnapshotAsOf)
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
			if t.SnapshotMode == "collect_latest" && !observationVersion(r) {
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
		tr, err = scanTask(tx.QueryRow(r.Context(), `SELECT `+taskCols+` FROM research_tasks t WHERE t.id=$1`, id))
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
		rs, err := tx.Query(r.Context(), `SELECT `+taskCols+` FROM research_tasks t WHERE ($1::timestamptz IS NULL OR (t.created_at, t.id) < ($1, $2))
			AND ($3 = '' OR t.skill_id=$3) AND ($4 = '' OR t.status=$4) AND ($5::uuid IS NULL OR t.subject_id=$5)
			ORDER BY t.created_at DESC, t.id DESC LIMIT $6`, at, cid, skill, status, subj, n+1)
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
	if _, _, err := s.Engine.CancelTask(r.Context(), ws, id, in.Expected); err != nil {
		return err
	}
	if s.Engine.Brief != nil {
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
		rs, err := tx.Query(r.Context(), `SELECT report_id, version, title, skill_id, subject_id, result_kind, delivered_at FROM report_versions WHERE `+where, args...)
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
		return tx.QueryRow(r.Context(), `SELECT document, series FROM report_versions WHERE report_id=$1 AND version=$2`, id, v).Scan(&body, &series)
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
	mode := "fixed_legacy"
	var snapshotAt *time.Time
	if d.SkillVersion == "1.1.0-demo.2" {
		mode = "collect_latest"
		snapshotAt = &d.AsOf
	}
	httpx.JSON(w, r, 200, Report{SnapshotMode: mode, SnapshotAsOf: snapshotAt, ID: d.ID, Version: d.Version, TaskID: d.TaskID, Title: d.Title, SkillID: d.SkillID, SkillVersion: d.SkillVersion,
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
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
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
	if handled, err := s.briefEvidence(w, r); handled || err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, ws := principal(r)
	var ok bool
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM research_tasks t JOIN snapshots sn ON sn.hash = t.snapshot_hash
			WHERE $1 = ANY(sn.record_ids))`, id).Scan(&ok)
	})
	if err != nil {
		return err
	}
	if !ok {
		return errs.NotFound()
	}
	recs, err := s.Store.Get(r.Context(), []uuid.UUID{id})
	if err != nil {
		return err
	}
	rec, found := recs[id]
	if !found {
		return errs.NotFound()
	}
	httpx.JSON(w, r, 200, s.evidence(r, rec))
	return nil
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
		ev.Value, ev.Unit = &v, &u
		loc := "公告 " + str("filing_id") + fmt.Sprintf("，修订版本 %d", rec.Version)
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
					parts = append(parts, fmt.Sprintf("%s：%v", f.Description, v))
				}
			}
		}
		ex := clip(strings.Join(parts, "；"), 1000)
		ev.Excerpt = &ex
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
		return tx.QueryRow(r.Context(), `SELECT id, formula_id, formula_version, inputs, result::text, unit, applicability, reason FROM calculations WHERE id=$1`, id).
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
