package httpapi

import (
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/watchlist"
)

// Contract types (contracts/openapi.json components). Field order follows the schema.

type EntitySkill struct {
	SkillID   string  `json:"skill_id"`
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
}

type Entity struct {
	ID             uuid.UUID     `json:"id"`
	Kind           string        `json:"kind"`
	Name           string        `json:"name"`
	Symbol         *string       `json:"symbol"`
	Venue          *string       `json:"venue"`
	Status         string        `json:"status"`
	Classification *string       `json:"classification"`
	Skills         []EntitySkill `json:"skills"`
}

var skillOrder = []string{"stock_analysis", "capital_flow", "deep_research"}

func toEntity(e dataservice.EntityRow, skills map[string]*harness.Skill) Entity {
	out := Entity{ID: e.ID, Kind: e.Kind, Name: e.Name, Status: e.Status, Classification: e.Classification, Skills: []EntitySkill{}}
	if e.Kind == "stock" {
		code := e.Code
		out.Symbol = &code
		out.Venue = e.Exchange
	}
	for _, id := range skillOrder {
		sk := skills[id]
		es := EntitySkill{SkillID: id, Available: slices.Contains(sk.Subjects, e.Kind)}
		if (skills["stock_analysis"].Version == emquant.ObservationBriefVersion || skills["stock_analysis"].Version == harness.RealtimeBriefVersion) && id != "stock_analysis" {
			es.Available = false
		}
		if !es.Available {
			r := sk.Title + "不支持该类研究对象"
			es.Reason = &r
		} else if e.IsFinancial && id != "capital_flow" {
			es.Available = false
			r := "金融类公司报表结构不同，首版财务章节不适用"
			es.Reason = &r
		}
		out.Skills = append(out.Skills, es)
	}
	return out
}

type Quota = harness.Quota

type Me struct {
	ResearchProtocol string     `json:"research_protocol,omitempty"`
	UserID           uuid.UUID  `json:"user_id"`
	WorkspaceID      *uuid.UUID `json:"workspace_id"`
	CSRF             string     `json:"csrf_token"`
	Persona          string     `json:"persona"`
	Quota            Quota      `json:"quota"`
	Onboarded        bool       `json:"onboarded"`
	Synthetic        bool       `json:"synthetic"`
	DisplayName      string     `json:"display_name"`
}

type Skill struct {
	ID           string         `json:"id"`
	Version      string         `json:"version"`
	Title        string         `json:"title"`
	SubjectKinds []string       `json:"subject_kinds"`
	ParamsSchema map[string]any `json:"params_schema"`
	Available    bool           `json:"available"`
}

type PlanSection struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Required     bool    `json:"required"`
	Availability string  `json:"availability"`
	Reason       *string `json:"reason"`
}

type Estimate struct {
	P95Seconds int `json:"p95_seconds"`
	QuotaCost  int `json:"quota_cost"`
}

type ResearchPlan struct {
	SnapshotMode     string         `json:"snapshot_mode,omitempty"`
	QuoteRequirement *string        `json:"quote_requirement"`
	ID               uuid.UUID      `json:"id"`
	PlanHash         string         `json:"plan_hash"`
	SkillID          string         `json:"skill_id"`
	SkillVersion     string         `json:"skill_version"`
	Subject          Entity         `json:"subject"`
	Params           map[string]any `json:"params"`
	AsOf             time.Time      `json:"as_of"`
	Sections         []PlanSection  `json:"sections"`
	Executable       bool           `json:"executable"`
	ReasonCode       *string        `json:"reason_code"`
	Estimate         Estimate       `json:"estimate"`
	ExpiresAt        time.Time      `json:"expires_at"`
}

func paramsMap(p harness.Params, skill string) map[string]any {
	m := map[string]any{}
	switch skill {
	case "deep_research":
		m["mode"] = p.Mode
		if p.Focus != "" {
			m["focus"] = p.Focus
		}
	default:
		m["window"] = p.Window
	}
	return m
}

func (s *Server) toPlan(p *harness.Plan) ResearchPlan {
	out := ResearchPlan{SnapshotMode: p.SnapshotMode, ID: p.ID, PlanHash: p.Hash, SkillID: p.SkillID, SkillVersion: p.SkillVersion, Subject: toEntity(p.Subject, s.Engine.Skills),
		QuoteRequirement: p.QuoteRequirement, Params: paramsMap(p.Params, p.SkillID), AsOf: p.AsOf, Executable: p.Executable, ReasonCode: p.ReasonCode,
		Estimate: Estimate{P95Seconds: p.P95Seconds, QuotaCost: p.QuotaCost}, ExpiresAt: p.ExpiresAt, Sections: []PlanSection{}}
	if p.SnapshotMode == "collect_latest" {
		out.Params = map[string]any{}
	}
	for _, sec := range p.Sections {
		out.Sections = append(out.Sections, PlanSection{sec.ID, sec.Title, sec.Required, sec.Avail, sec.Reason})
	}
	return out
}

type Candidate struct {
	SkillID string `json:"skill_id"`
	Subject Entity `json:"subject"`
	Label   string `json:"label"`
}

type PlanResult struct {
	Kind              string        `json:"kind"`
	Plan              *ResearchPlan `json:"plan"`
	Candidates        []Candidate   `json:"candidates"`
	UnsupportedReason *string       `json:"unsupported_reason"`
	Suggestions       []string      `json:"suggestions"`
}

type TaskStep struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type ReportRef struct {
	ID      uuid.UUID `json:"id"`
	Version int       `json:"version"`
}

type Task struct {
	SnapshotMode     string     `json:"snapshot_mode,omitempty"`
	SnapshotAsOf     *time.Time `json:"snapshot_as_of"`
	QuoteRequirement *string    `json:"quote_requirement"`
	ID               uuid.UUID  `json:"id"`
	SkillID          string     `json:"skill_id"`
	Subject          Entity     `json:"subject"`
	Status           string     `json:"status"`
	Revision         int64      `json:"revision"`
	CreatedAt        time.Time  `json:"created_at"`
	Deadline         time.Time  `json:"deadline"`
	AsOf             time.Time  `json:"as_of"`
	Steps            []TaskStep `json:"steps"`
	ReportRef        *ReportRef `json:"report_ref"`
	ReasonCode       *string    `json:"reason_code"`
	ReasonMessage    *string    `json:"reason_message"`
}

type ReportSummary struct {
	ID          uuid.UUID `json:"id"`
	Version     int       `json:"version"`
	Title       string    `json:"title"`
	SkillID     string    `json:"skill_id"`
	Subject     Entity    `json:"subject"`
	ResultKind  string    `json:"result_kind"`
	DeliveredAt time.Time `json:"delivered_at"`
}

type Report struct {
	SnapshotMode    string                    `json:"snapshot_mode,omitempty"`
	SnapshotAsOf    *time.Time                `json:"snapshot_as_of,omitempty"`
	SourceFreshness []harness.SourceFreshness `json:"source_freshness,omitempty"`
	ID              uuid.UUID                 `json:"id"`
	Version         int                       `json:"version"`
	TaskID          uuid.UUID                 `json:"task_id"`
	Title           string                    `json:"title"`
	SkillID         string                    `json:"skill_id"`
	SkillVersion    string                    `json:"skill_version"`
	Subject         Entity                    `json:"subject"`
	AsOf            time.Time                 `json:"as_of"`
	DataAsOf        string                    `json:"data_as_of"`
	ResultKind      string                    `json:"result_kind"`
	Synthetic       bool                      `json:"synthetic"`
	AIGenerated     bool                      `json:"ai_generated"`
	SummaryRefs     []string                  `json:"summary_claim_refs"`
	Sections        []harness.DocSection      `json:"sections"`
	Claims          []harness.DocClaim        `json:"claims"`
	Limitations     []string                  `json:"limitations"`
	Sources         []harness.DocSource       `json:"sources"`
	Disclaimer      string                    `json:"disclaimer"`
	DeliveredAt     time.Time                 `json:"delivered_at"`
}

type Evidence struct {
	ReceivedAt  *time.Time `json:"received_at,omitempty"`
	EventAt     *time.Time `json:"event_at"`
	LatencyMode string     `json:"latency_mode,omitempty"`
	ID          uuid.UUID  `json:"id"`
	DatasetID   string     `json:"dataset_id"`
	Provider    string     `json:"provider"`
	Origin      string     `json:"origin"`
	SourceTier  string     `json:"source_tier"`
	Title       string     `json:"title"`
	Label       string     `json:"label"`
	Value       *string    `json:"value"`
	Unit        *string    `json:"unit"`
	Basis       *string    `json:"basis"`
	Period      *string    `json:"period"`
	PublishedAt *time.Time `json:"published_at"`
	AvailableAt time.Time  `json:"available_at"`
	Locator     *string    `json:"locator"`
	Excerpt     *string    `json:"excerpt"`
	ContentHash string     `json:"content_hash"`
}

type CalcInput struct {
	EvidenceID uuid.UUID `json:"evidence_id"`
	Value      string    `json:"value"`
	Unit       string    `json:"unit"`
}

type Calculation struct {
	ID             uuid.UUID   `json:"id"`
	FormulaID      string      `json:"formula_id"`
	FormulaVersion string      `json:"formula_version"`
	Inputs         []CalcInput `json:"inputs"`
	Result         *string     `json:"result"`
	Unit           string      `json:"unit"`
	Applicability  string      `json:"applicability"`
	Reason         string      `json:"reason"`
}

type WatchItem struct {
	ID         uuid.UUID           `json:"id"`
	Version    int64               `json:"version"`
	Entity     Entity              `json:"entity"`
	Tags       []string            `json:"tags"`
	Note       string              `json:"note"`
	Alerts     []watchlist.Alert   `json:"alerts"`
	AddedAt    time.Time           `json:"added_at"`
	Snapshot   *watchlist.Snapshot `json:"snapshot"`
	LastReport *ReportSummary      `json:"last_report"`
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
