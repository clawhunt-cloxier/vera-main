package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

const Disclaimer = "本报告由 Vera 基于已接入数据自动生成（AI 生成内容），仅供研究参考，不构成任何投资建议。数据可能存在延迟或口径差异，请以原始披露为准。"

type summaryPoint struct {
	Text      string   `json:"text"`
	ClaimRefs []string `json:"claim_refs"`
}

// Document is the stored report (contracts Report minus the expanded subject entity).
type Document struct {
	ID           uuid.UUID      `json:"id"`
	Version      int            `json:"version"`
	TaskID       uuid.UUID      `json:"task_id"`
	Title        string         `json:"title"`
	SkillID      string         `json:"skill_id"`
	SkillVersion string         `json:"skill_version"`
	SubjectID    uuid.UUID      `json:"subject_id"`
	AsOf         time.Time      `json:"as_of"`
	DataAsOf     string         `json:"data_as_of"`
	ResultKind   string         `json:"result_kind"`
	Synthetic    bool           `json:"synthetic"`
	AIGenerated  bool           `json:"ai_generated"`
	SummaryRefs  []string       `json:"summary_claim_refs"`
	Sections     []DocSection   `json:"sections"`
	Claims       []DocClaim     `json:"claims"`
	Limitations  []string       `json:"limitations"`
	Sources      []DocSource    `json:"sources"`
	Disclaimer   string         `json:"disclaimer"`
	DeliveredAt  time.Time      `json:"delivered_at"`
	Series       []facts.Series `json:"-"`
}

type DocSection struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Status    string        `json:"status"`
	ClaimRefs []string      `json:"claim_refs"`
	Tables    []facts.Table `json:"tables"`
	Charts    []DocChart    `json:"charts"`
	Note      *string       `json:"note"`
}

type DocChart struct {
	SeriesID string `json:"series_id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
}

type DocClaim struct {
	Ref          string          `json:"ref"`
	Kind         string          `json:"kind"`
	Text         string          `json:"text"`
	EvidenceRefs []uuid.UUID     `json:"evidence_refs"`
	CalcRefs     []uuid.UUID     `json:"calculation_refs"`
	Scenario     *check.Scenario `json:"scenario"`
}

type DocSource struct {
	Provider  string `json:"provider"`
	DatasetID string `json:"dataset_id"`
	UpdatedAt string `json:"updated_at"`
}

func refsToEvidence(sheet *facts.Sheet, ids []string) ([]uuid.UUID, []uuid.UUID) {
	ev, calcs := []uuid.UUID{}, []uuid.UUID{}
	for _, id := range ids {
		f, ok := sheet.Get(id)
		if !ok {
			continue
		}
		for _, e := range f.Evidence {
			if !slices.Contains(ev, e) {
				ev = append(ev, e)
			}
		}
		if f.CalcID != nil && !slices.Contains(calcs, *f.CalcID) {
			calcs = append(calcs, *f.CalcID)
		}
	}
	return ev, calcs
}

// summaryClaim is the pseudo-claim used to run the deterministic rules on a summary point.
func summaryClaim(p summaryPoint, by map[string]finalClaim) (check.Claim, bool) {
	c := check.Claim{Text: p.Text, Numbers: []check.Number{}, Scenario: noScenario()}
	kinds := map[string]bool{}
	for _, r := range p.ClaimRefs {
		fc, ok := by[r]
		if !ok {
			return c, false
		}
		kinds[fc.Claim.Kind] = true
		for _, id := range fc.Claim.Refs {
			if !slices.Contains(c.Refs, id) {
				c.Refs = append(c.Refs, id)
			}
		}
		if fc.Claim.Kind == KScen && c.Scenario.Trigger == "" {
			c.Scenario = fc.Claim.Scenario
		}
	}
	switch {
	case len(kinds) == 1:
		for k := range kinds {
			c.Kind = k
		}
	case kinds[KScen]:
		c.Kind = KScen
	default:
		c.Kind = KInf
	}
	if c.Kind != KScen {
		c.Scenario = noScenario()
	}
	return c, len(p.ClaimRefs) > 0
}

func (e *Engine) assemble(ctx, parent context.Context, t *taskRow, sk *Skill, subject dataservice.EntityRow, sheet *facts.Sheet, secs []Section, results []SectionResult) error {
	var final []finalClaim
	var required []string
	for i, s := range secs {
		if s.Required {
			required = append(required, s.ID)
		}
		for j, c := range results[i].Claims {
			final = append(final, finalClaim{Ref: fmt.Sprintf("%s.%d", s.ID, j+1), Section: s.ID, Claim: c})
		}
	}
	by := map[string]finalClaim{}
	for _, c := range final {
		by[c.Ref] = c
	}
	// Summary: model may fail or time out; the deterministic fallback keeps delivery possible.
	points, err := step(parent, e, t, "summary", func() ([]summaryPoint, error) {
		var msg string
		for _, c := range final {
			msg += fmt.Sprintf("%s（%s）：%s\n", c.Ref, c.Claim.Kind, c.Claim.Text)
		}
		raw, _, err := e.Gateway.Complete(ctx, model.Request{Workspace: t.Workspace, Task: &t.ID, StepKey: "summary", Profile: "writer",
			Messages: []model.Message{{Role: "system", Content: summaryRules}, {Role: "user", Content: "已核验结论：\n" + msg}}, MaxOutput: 1200,
			Schema: "summary-output", Budget: t.Budget, Simulate: func() (any, error) { return simulateSummary(final, required), nil }})
		var pts []summaryPoint
		if err == nil {
			var out struct {
				Points []summaryPoint `json:"points"`
			}
			if json.Unmarshal(raw, &out) == nil {
				all := Section{Kinds: allKinds, MaxChars: 160}
				cx := check.Context{Sheet: sheet, Kinds: all.Kinds, AsOf: t.AsOf, MaxChars: all.MaxChars}
				for _, p := range out.Points {
					if c, ok := summaryClaim(p, by); ok && check.Check(c, cx) == nil && len(pts) < 5 {
						pts = append(pts, p)
					}
				}
			}
		} else if errors.Is(err, errStale) || errors.Is(err, context.Canceled) && parent.Err() != nil {
			return nil, err
		}
		if len(pts) < 3 {
			pts = fallbackSummary(final, required)
		}
		return pts, nil
	})
	if err != nil {
		return err
	}
	doc, err := step(parent, e, t, "render", func() (Document, error) {
		return e.render(parent, t, sk, subject, sheet, secs, results, final, points)
	})
	if err != nil {
		return err
	}
	return e.deliver(parent, t, doc, sheet, secs, results, final)
}

func (e *Engine) render(ctx context.Context, t *taskRow, sk *Skill, subject dataservice.EntityRow, sheet *facts.Sheet, secs []Section,
	results []SectionResult, final []finalClaim, points []summaryPoint) (Document, error) {
	doc := Document{ID: uuid.NewSHA1(t.ID, []byte("report")), Version: 1, TaskID: t.ID, Title: sk.Name(subject, t.Params), SkillID: sk.ID,
		SkillVersion: sk.Version, SubjectID: subject.ID, AsOf: t.AsOf, DataAsOf: t.AsOf.In(cst).Format("2006-01-02 15:04") + "（北京时间）",
		ResultKind: "full", Synthetic: e.Synthetic || subject.Synthetic, AIGenerated: true, Disclaimer: Disclaimer, Claims: []DocClaim{},
		Limitations: []string{}, Sources: []DocSource{}, SummaryRefs: []string{}}
	// S1 summary claims
	s1 := DocSection{ID: "S1", Title: "核心要点", Status: "ok", ClaimRefs: []string{}, Tables: []facts.Table{}, Charts: []DocChart{}}
	byRef := map[string]finalClaim{}
	for _, c := range final {
		byRef[c.Ref] = c
	}
	for i, p := range points {
		ref := fmt.Sprintf("S1.%d", i+1)
		c, _ := summaryClaim(p, byRef)
		ev, calcs := refsToEvidence(sheet, c.Refs)
		dc := DocClaim{Ref: ref, Kind: c.Kind, Text: p.Text, EvidenceRefs: ev, CalcRefs: calcs}
		if c.Kind == KScen {
			sc := c.Scenario
			dc.Scenario = &sc
		}
		doc.Claims = append(doc.Claims, dc)
		s1.ClaimRefs = append(s1.ClaimRefs, ref)
		doc.SummaryRefs = append(doc.SummaryRefs, ref)
	}
	doc.Sections = append(doc.Sections, s1)
	seenSeries := map[string]bool{}
	for i, s := range secs {
		r := results[i]
		ds := DocSection{ID: s.ID, Title: s.Title, Status: r.Status, ClaimRefs: []string{}, Tables: sheet.Tables[s.ID], Charts: []DocChart{}}
		if ds.Tables == nil {
			ds.Tables = []facts.Table{}
		}
		if r.Status == "" {
			ds.Status = "omitted"
		}
		if r.Note != "" {
			n := r.Note
			ds.Note = &n
		}
		if ds.Status != "ok" {
			if s.Required && ds.Status == "omitted" {
				return doc, fmt.Errorf("required section %s omitted at render", s.ID)
			}
			doc.ResultKind = "limited"
		}
		for _, c := range final {
			if c.Section != s.ID {
				continue
			}
			ev, calcs := refsToEvidence(sheet, append(slices.Clone(c.Claim.Refs), c.Claim.Scenario.Watch...))
			dc := DocClaim{Ref: c.Ref, Kind: c.Claim.Kind, Text: c.Claim.Text, EvidenceRefs: ev, CalcRefs: calcs}
			if c.Claim.Kind == KScen {
				sc := c.Claim.Scenario
				dc.Scenario = &sc
			}
			doc.Claims = append(doc.Claims, dc)
			ds.ClaimRefs = append(ds.ClaimRefs, c.Ref)
		}
		if ds.Status != "omitted" {
			for _, ch := range sheet.Charts {
				if ch.Section == s.ID && !seenSeries[ch.ID] {
					seenSeries[ch.ID] = true
					ds.Charts = append(ds.Charts, DocChart{SeriesID: ch.ID, Kind: ch.Kind, Title: ch.Title})
					doc.Series = append(doc.Series, ch)
				}
			}
		} else if ds.Note != nil {
			doc.Limitations = append(doc.Limitations, fmt.Sprintf("%s %s：%s", s.ID, s.Title, *ds.Note))
		}
		doc.Sections = append(doc.Sections, ds)
	}
	doc.Limitations = append(doc.Limitations, sheet.Limitations...)
	if doc.Synthetic {
		doc.Limitations = append(doc.Limitations, "本报告使用虚构的演示数据或模拟模型生成，不对应任何真实公司或市场，仅用于演示。")
	}
	provs, err := e.providerNames(ctx)
	if err != nil {
		return doc, err
	}
	for _, ds := range slices.Sorted(func(yield func(string) bool) {
		for k := range sheet.Sources {
			if !yield(k) {
				return
			}
		}
	}) {
		doc.Sources = append(doc.Sources, DocSource{Provider: provs, DatasetID: ds, UpdatedAt: sheet.Sources[ds]})
	}
	return doc, nil
}

func (e *Engine) providerNames(ctx context.Context) (string, error) {
	var name string
	err := e.Pool.QueryRow(ctx, `SELECT coalesce(string_agg(name, '、' ORDER BY id), '') FROM providers WHERE enabled`).Scan(&name)
	return name, err
}

// deliver writes the report version, claims and COMPLETED in one transaction (HARNESS §10.1).
func (e *Engine) deliver(ctx context.Context, t *taskRow, doc Document, sheet *facts.Sheet, secs []Section, results []SectionResult, final []finalClaim) error {
	doc.DeliveredAt = time.Now().UTC().Truncate(time.Second)
	body, _ := json.Marshal(doc)
	series, _ := json.Marshal(doc.Series)
	h := sha256.Sum256(body)
	versions, _ := json.Marshal(map[string]string{"skill": doc.SkillVersion, "rules": check.RulesVersion, "formulas": facts.FormulaVersion,
		"prompts": PromptVersion, "policy": e.Policy.Version, "prices": e.priceVersion()})
	return db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
		var epoch int64
		var status, planHash string
		var snapshot *string
		if err := tx.QueryRow(ctx, `SELECT t.epoch, t.status, t.snapshot_hash, p.plan_hash FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id
			WHERE t.id=$1 FOR UPDATE OF t`, t.ID).Scan(&epoch, &status, &snapshot, &planHash); err != nil {
			return err
		}
		if epoch != t.Epoch || status != "ASSEMBLING" || snapshot == nil || planHash != t.PlanHash {
			return errStale
		}
		if err := event(ctx, tx, t.Workspace, t.ID, t.Epoch, "DELIVERING", nil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO report_versions(report_id,version,workspace_id,task_id,epoch,skill_id,subject_id,title,result_kind,synthetic,
			document,document_hash,snapshot_hash,plan_hash,versions,series,delivered_at) VALUES($1,1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			doc.ID, t.Workspace, t.ID, t.Epoch, doc.SkillID, doc.SubjectID, doc.Title, doc.ResultKind, doc.Synthetic, body, hex.EncodeToString(h[:]),
			*snapshot, planHash, versions, series, doc.DeliveredAt); err != nil {
			return err
		}
		for _, c := range final {
			sc, _ := json.Marshal(c.Claim.Scenario)
			if _, err := tx.Exec(ctx, `INSERT INTO claims(workspace_id,task_id,epoch,section_id,claim_ref,kind,text,refs,scenario,verdict)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'kept')`, t.Workspace, t.ID, t.Epoch, c.Section, c.Ref, c.Claim.Kind, c.Claim.Text, c.Claim.Refs, sc); err != nil {
				return err
			}
		}
		for i, s := range secs {
			for j, r := range results[i].Removed {
				if _, err := tx.Exec(ctx, `INSERT INTO claims(workspace_id,task_id,epoch,section_id,claim_ref,kind,text,refs,verdict,removed_reason)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8,'removed',$9)`, t.Workspace, t.ID, t.Epoch, s.ID, fmt.Sprintf("%s.x%d", s.ID, j+1),
					r.Claim.Kind, r.Claim.Text, r.Claim.Refs, r.Rule+" "+r.Reason); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE research_tasks SET status='COMPLETED', report_id=$2, revision=revision+1, updated_at=now() WHERE id=$1`,
			t.ID, doc.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE task_steps SET status='SUCCEEDED', finished_at=now() WHERE task_id=$1 AND epoch=$2 AND step_key='deliver'`,
			t.ID, t.Epoch); err != nil {
			return err
		}
		t.Status = "COMPLETED"
		return event(ctx, tx, t.Workspace, t.ID, t.Epoch, "COMPLETED", map[string]any{"report_id": doc.ID, "result_kind": doc.ResultKind})
	})
}

func (e *Engine) priceVersion() string {
	if e.Gateway != nil && e.Gateway.Prices != nil {
		return e.Gateway.Prices.Version
	}
	return ""
}
