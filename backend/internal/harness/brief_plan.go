package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// BriefDemoVersion is distinct from the complete stock-analysis skill. The
// demo cannot claim the full skill's data coverage or acceptance criteria.
const BriefDemoVersion = "1.1.0-demo.1"

var mainlandStockCode = regexp.MustCompile(`^[0-9]{6}\.(SH|SZ|BJ)$`)

type BriefDataset struct {
	ID       string `json:"dataset_id"`
	Required bool   `json:"required"`
}

// BriefPlanDefinition is a deterministic input to the eventual 0.4 plan
// confirmation. It does not assert that any provider, data or model is ready.
type BriefPlanDefinition struct {
	SkillID          string         `json:"skill_id"`
	SkillVersion     string         `json:"skill_version"`
	SubjectID        uuid.UUID      `json:"subject_id"`
	SecurityCode     string         `json:"security_code"`
	PreviewAt        time.Time      `json:"preview_at"`
	SnapshotMode     string         `json:"snapshot_mode"`
	QuoteRequirement string         `json:"quote_requirement"`
	Datasets         []BriefDataset `json:"datasets"`
	Sections         []PlanSection  `json:"sections"`
}

// NewRealtimeBriefPlan defines only the user-approved single-stock demo. A
// non-synthetic entity and a valid-looking code do not establish coverage or
// real-time rights; the caller must separately verify the registered source,
// policy, account and each actual quote before declaring the plan executable.
// quoteReady is a trusted capability check, never user or model input; it is
// not a promise that a later quote will meet freshness or trading-state rules.
func NewRealtimeBriefPlan(subject dataservice.EntityRow, preview time.Time, quoteReady bool) (BriefPlanDefinition, error) {
	if subject.ID == uuid.Nil || subject.Kind != "stock" || subject.Synthetic || !mainlandStockCode.MatchString(subject.Code) || preview.IsZero() {
		return BriefPlanDefinition{}, errors.New("harness: real-data brief requires a non-synthetic A-share subject")
	}
	quoteState := "available"
	var quoteReason *string
	if !quoteReady {
		quoteState = "unavailable"
		reason := "实时行情能力尚未核实"
		quoteReason = &reason
	}
	optionalReason := "公司与财务数据尚未接入可验证的快照"
	return BriefPlanDefinition{
		SkillID: "stock_analysis", SkillVersion: BriefDemoVersion, SubjectID: subject.ID, SecurityCode: subject.Code,
		PreviewAt: preview.UTC().Truncate(time.Microsecond), SnapshotMode: "collect_latest", QuoteRequirement: "realtime_trade",
		Datasets: []BriefDataset{{ID: "fin.statement"}, {ID: "mkt.intraday_quote", Required: true}, {ID: "ref.security"}},
		Sections: []PlanSection{
			{ID: "S1", Title: "核心要点", Required: true, Avail: quoteState, Reason: quoteReason},
			{ID: "S2", Title: "行情与市场事实", Required: true, Avail: quoteState, Reason: quoteReason},
			{ID: "S3", Title: "公司与财务事实", Required: false, Avail: "unavailable", Reason: &optionalReason},
		},
	}, nil
}

// Hash binds every server-chosen data requirement and quote requirement. The
// final confirmation must rederive this definition from the stored plan and
// compare the hash in a transaction; a hash alone does not authorize a task.
func (p BriefPlanDefinition) Hash() (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte("vera.stock-brief-plan.v1\x00"), body...))
	return hex.EncodeToString(h[:]), nil
}
