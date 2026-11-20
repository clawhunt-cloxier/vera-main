package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

const Disclaimer = "本报告由 Vera 基于已接入数据自动生成（AI 生成内容），仅供研究参考，不构成任何投资建议。数据可能存在延迟或口径差异，请以原始披露为准。"

type summaryPoint struct {
	Text      string   `json:"text"`
	ClaimRefs []string `json:"claim_refs"`
}

// Document is the stored report (contracts Report minus the expanded subject entity).
type Document struct {
	ID              uuid.UUID         `json:"id"`
	Version         int               `json:"version"`
	TaskID          uuid.UUID         `json:"task_id"`
	Title           string            `json:"title"`
	SkillID         string            `json:"skill_id"`
	SkillVersion    string            `json:"skill_version"`
	SubjectID       uuid.UUID         `json:"subject_id"`
	AsOf            time.Time         `json:"as_of"`
	SnapshotMode    string            `json:"snapshot_mode"`
	SnapshotAsOf    *time.Time        `json:"snapshot_as_of,omitempty"`
	SourceFreshness []SourceFreshness `json:"source_freshness,omitempty"`
	DataAsOf        string            `json:"data_as_of"`
	ResultKind      string            `json:"result_kind"`
	Synthetic       bool              `json:"synthetic"`
	AIGenerated     bool              `json:"ai_generated"`
	SummaryRefs     []string          `json:"summary_claim_refs"`
	Sections        []DocSection      `json:"sections"`
	Claims          []DocClaim        `json:"claims"`
	Limitations     []string          `json:"limitations"`
	Sources         []DocSource       `json:"sources"`
	Disclaimer      string            `json:"disclaimer"`
	DeliveredAt     time.Time         `json:"delivered_at"`
	Series          []facts.Series    `json:"-"`
}

// SourceFreshness keeps the independent event, publication, receipt and
// availability times visible to readers. A dataset may have no applicable
// timestamp; nil values are represented explicitly in the 0.4 contract.
type SourceFreshness struct {
	DatasetID   string     `json:"dataset_id"`
	Provider    string     `json:"provider"`
	EventAt     *time.Time `json:"event_at"`
	PublishedAt *time.Time `json:"published_at"`
	ReceivedAt  *time.Time `json:"received_at"`
	AvailableAt *time.Time `json:"available_at"`
	Period      *string    `json:"period"`
	LatencyMode string     `json:"latency_mode"`
	Status      string     `json:"status"`
	Note        string     `json:"note"`
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
		raw, _, err := e.complete(ctx, t, model.Request{Workspace: t.Workspace, Task: &t.ID, StepKey: "summary", Profile: "writer",
			Messages: []model.Message{{Role: "system", Content: summaryRules}, {Role: "user", Content: "已核验结论：\n" + msg}}, MaxOutput: 4000,
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
	points, err = step(parent, e, t, "verify:summary", func() ([]summaryPoint, error) {
		return e.verifySummary(ctx, parent, t, sheet, points, by, final, required)
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
		SkillVersion: sk.Version, SubjectID: subject.ID, AsOf: t.AsOf, SnapshotMode: t.SnapshotMode, DataAsOf: t.AsOf.In(cst).Format("2006-01-02 15:04") + "（北京时间）",
		ResultKind: "full", Synthetic: e.Synthetic || subject.Synthetic, AIGenerated: true, Disclaimer: Disclaimer, Claims: []DocClaim{},
		Limitations: []string{}, Sources: []DocSource{}, SummaryRefs: []string{}}
	if t.Version == RealtimeBriefVersion {
		// Deterministic fact checks use t.AsOf as the frozen evaluation
		// cutoff; the public 0.4 as_of retains the user's plan preview time.
		doc.AsOf = t.PreviewAt
	}
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
		if (ds.Status == "ok" || ds.Status == "limited") && !r.Verified {
			doc.Limitations = append(doc.Limitations, fmt.Sprintf("%s %s：语义核验不可用，已删除推断与情景结论，仅保留经确定性检查的事实与计算结论。", s.ID, s.Title))
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
	if t.SnapshotMode == "collect_latest" {
		cutoff, freshness, freshnessErr := e.realtimeFreshness(ctx, t)
		if freshnessErr != nil {
			return doc, freshnessErr
		}
		if cutoff != nil {
			doc.SnapshotAsOf = cutoff
			doc.SourceFreshness = freshness
			doc.DataAsOf = "数据冻结截止时点：" + cutoff.In(cst).Format("2006-01-02 15:04:05") + "（北京时间）"
		}
	}
	if t.Version == emquant.ObservationBriefVersion {
		doc.ResultKind = "limited"
		doc.DataAsOf = "数据采集后的冻结时点：" + t.AsOf.In(cst).Format("2006-01-02 15:04:05") + "（北京时间；非实时）"
	}
	if doc.Synthetic {
		if subject.Synthetic {
			doc.Limitations = append(doc.Limitations, "本报告使用虚构的演示数据，不对应任何真实公司或市场，仅用于演示。")
		} else {
			doc.Limitations = append(doc.Limitations, "本报告使用模拟模型，仅用于工程验证。")
		}
	}
	if t.Version == CloseResearchVersion {
		doc.DataAsOf = t.AsOf.In(cst).Format("2006-01-02 15:04") + "（北京时间；收盘及已披露数据）"
		names := map[string]string{}
		for _, dataset := range slices.Sorted(maps.Keys(sheet.SourceProviders)) {
			for _, provider := range slices.Sorted(maps.Keys(sheet.SourceProviders[dataset])) {
				if _, exists := names[provider]; !exists {
					var name string
					if err := e.Pool.QueryRow(ctx, `SELECT name FROM providers WHERE id=$1 AND enabled AND NOT synthetic`, provider).Scan(&name); err != nil {
						return doc, err
					}
					names[provider] = name
				}
				doc.Sources = append(doc.Sources, DocSource{Provider: names[provider], DatasetID: dataset, UpdatedAt: sheet.SourceProviders[dataset][provider]})
			}
		}
		return doc, nil
	}
	provs, err := e.providerNames(ctx)
	if t.Version == emquant.ObservationBriefVersion && e.Brief != nil {
		err = e.Pool.QueryRow(ctx, `SELECT name FROM providers WHERE id=$1 AND enabled AND NOT synthetic`, e.Brief.ProviderID).Scan(&provs)
	}
	if t.Version == RealtimeBriefVersion && len(doc.SourceFreshness) > 0 {
		// A report must name only providers that contributed to its frozen
		// snapshot; the platform's enabled-provider aggregate is not lineage.
		err = e.Pool.QueryRow(ctx, `SELECT name FROM providers WHERE id=$1 AND enabled`, doc.SourceFreshness[0].Provider).Scan(&provs)
	}
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

// realtimeFreshness reads and reauthorizes the immutable quote bound to the
// task. It never chooses a newer record from a progress revision or wall clock.
func (e *Engine) realtimeFreshness(ctx context.Context, t *taskRow) (*time.Time, []SourceFreshness, error) {
	if t.Version == emquant.ObservationBriefVersion {
		if e.Brief == nil {
			return nil, nil, errors.New("harness: observation source unavailable")
		}
		var cutoff *time.Time
		var fresh []SourceFreshness
		err := e.Brief.WithSnapshot(ctx, t.Workspace, t.ID, dataservice.SourceDisplay, func(o emquant.BriefObservation) error {
			cutoff = timePtr(o.Cutoff)
			fresh = ObservationFreshness(o, e.Brief.ProviderID)
			return nil
		})
		return cutoff, fresh, err
	}
	if t.Version != RealtimeBriefVersion {
		return nil, nil, nil
	}
	if e.Brief == nil || e.Brief.Runtime == nil {
		return nil, nil, errors.New("harness: realtime source unavailable")
	}
	var cutoff *time.Time
	var fresh []SourceFreshness
	err := e.Brief.Runtime.WithTaskSnapshotQuote(ctx, t.Workspace, t.ID, dataservice.SourceDisplay, func(view emquant.QuoteView) error {
		cutoff = timePtr(view.Cutoff)
		for _, q := range view.Quotes {
			status := "fresh"
			if q.LatencyMode == "unknown" || q.MarketState == "unknown" {
				status = "unknown"
			}
			state := map[string]string{"continuous": "连续交易", "closed": "非交易状态", "suspended": "停牌", "unknown": "未确认"}[q.MarketState]
			fresh = append(fresh, SourceFreshness{DatasetID: "mkt.intraday_quote", Provider: q.ProviderID,
				EventAt: timePtr(q.EventAt), ReceivedAt: timePtr(q.ReceivedAt), AvailableAt: timePtr(q.AvailableAt),
				LatencyMode: q.LatencyMode, Status: status, Note: "冻结时已通过时点与新鲜度校验；交易状态：" + state})
		}
		if cutoff == nil || len(fresh) != 1 || len(view.Unavailable) != 0 {
			return errors.New("harness: realtime report snapshot is incomplete")
		}
		return nil
	})
	return cutoff, fresh, err
}

// ObservationFreshness keeps legacy brief metadata honest when a 0.4 client
// reads it: collection is known, but event/publication/availability times are
// not established by the returned date labels.
func ObservationFreshness(o emquant.BriefObservation, provider string) []SourceFreshness {
	var period *string
	if len(o.Rows) > 0 {
		last := o.Rows[len(o.Rows)-1].TradeDate
		period = &last
	}
	return []SourceFreshness{{DatasetID: "mkt.daily_bar", Provider: provider,
		ReceivedAt: timePtr(o.Manifest.Liquidity.Source.ReceivedAt), Period: period,
		LatencyMode: "unknown", Status: "unknown", Note: "非实时日成交观察；期间标签不证明事件、披露或首次可用时间，不提供历史 PIT 保证"}}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
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
	return e.deliverTransaction(ctx, t, func(tx pgx.Tx) error {
		var epoch, revision int64
		var status, planHash string
		var snapshot *string
		if err := tx.QueryRow(ctx, `SELECT t.epoch, t.status, coalesce(t.snapshot_hash,t.snapshot_manifest_hash), p.plan_hash, t.revision FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id
			WHERE t.id=$1 FOR UPDATE OF t`, t.ID).Scan(&epoch, &status, &snapshot, &planHash, &revision); err != nil {
			return err
		}
		if epoch != t.Epoch || status != "ASSEMBLING" || snapshot == nil || planHash != t.PlanHash || (t.SnapshotMode == "collect_latest" && (t.Snapshot == nil || *t.Snapshot != *snapshot)) {
			return errStale
		}
		if t.Version == CloseResearchVersion && (t.Snapshot == nil || *t.Snapshot != *snapshot) {
			return errStale
		}
		if t.Version == RealtimeBriefVersion && revision != t.Revision {
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

// verifySummary semantically checks summary points that rewrite claims (HARNESS §7): only points the verifier marks
// supported survive. Points that quote a single claim verbatim need no check. When the verifier is unavailable or
// fewer than three points survive, the deterministic fallback (verbatim claims) is used, so nothing unverified ships.
func (e *Engine) verifySummary(ctx, parent context.Context, t *taskRow, sheet *facts.Sheet, points []summaryPoint,
	by map[string]finalClaim, final []finalClaim, required []string) ([]summaryPoint, error) {
	verbatim := func(p summaryPoint) bool {
		return len(p.ClaimRefs) == 1 && by[p.ClaimRefs[0]].Claim.Text == p.Text
	}
	var b strings.Builder
	var pending []string
	for i, p := range points {
		if verbatim(p) {
			continue
		}
		ref := fmt.Sprintf("S1.%d", i+1)
		pending = append(pending, ref)
		fmt.Fprintf(&b, "要点 %s：%s\n依据的已核验结论：\n", ref, p.Text)
		for _, r := range p.ClaimRefs {
			c := by[r]
			fmt.Fprintf(&b, "  %s（%s）：%s\n", r, c.Claim.Kind, c.Claim.Text)
			for _, id := range c.Claim.Refs {
				if f, ok := sheet.Get(id); ok {
					b.WriteString("    " + factLine(*f) + "\n")
				}
			}
		}
	}
	if len(pending) == 0 {
		return points, nil
	}
	raw, _, err := e.complete(ctx, t, model.Request{Workspace: t.Workspace, Task: &t.ID, StepKey: "verify:summary", Profile: "verifier",
		Messages: []model.Message{{Role: "system", Content: summaryVerifyRules}, {Role: "user", Content: b.String()}}, MaxOutput: 2000,
		Schema: "verify-output", Budget: t.Budget, Simulate: func() (any, error) {
			vs := []map[string]string{}
			for _, r := range pending {
				vs = append(vs, map[string]string{"claim_ref": r, "verdict": "supported", "reason": "", "rewrite": "", "suggested_kind": "none"})
			}
			return map[string]any{"verdicts": vs}, nil
		}})
	if err != nil {
		if errors.Is(err, errStale) || errors.Is(err, context.Canceled) && parent.Err() != nil {
			return nil, err
		}
		return fallbackSummary(final, required), nil
	}
	var out verifyOut
	if json.Unmarshal(raw, &out) != nil {
		return fallbackSummary(final, required), nil
	}
	ok := map[string]bool{}
	for _, v := range out.Verdicts {
		if v.Verdict == "supported" {
			ok[v.ClaimRef] = true
		}
	}
	var kept []summaryPoint
	for i, p := range points {
		if verbatim(p) || ok[fmt.Sprintf("S1.%d", i+1)] {
			kept = append(kept, p)
		}
	}
	if len(kept) < 3 {
		return fallbackSummary(final, required), nil
	}
	return kept, nil
}
