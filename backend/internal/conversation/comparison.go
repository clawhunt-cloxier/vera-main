package conversation

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

const (
	compareStocks   = "stocks"
	compareIndustry = "industry_peers"
	comparePrevious = "previous"
)

var comparisonTicker = regexp.MustCompile(`(?i)[0-9]{6}\.(SH|SZ|BJ)`)

type comparisonRequest struct {
	Kind          string   `json:"kind"`
	StockMentions []string `json:"stock_mentions"`
	SubjectSource string   `json:"subject_source"`
	TradingDays   int      `json:"trading_days"`
	Advice        string   `json:"advice"`
}

type comparisonScope struct {
	Kind        string      `json:"kind"`
	OtherIDs    []uuid.UUID `json:"other_ids,omitempty"`
	SampleDate  string      `json:"sample_date,omitempty"`
	SampleProof string      `json:"sample_proof,omitempty"`
}

func (s Service) startComparison(ctx context.Context, args resolveArgs, turn Turn, state agentContext, request comparisonRequest) error {
	scope := Scope{Skill: "stock_analysis", Topic: harness.ComparisonTopic}
	in := intent{Scope: scope, AdviceDeclined: request.Advice == "declined"}
	clarify := func(message string) error { return s.update(ctx, args, "clarify", message, in) }
	spec, ok := s.Engine.ConversationResearchSpec(scope.Skill, scope.Topic)
	if !ok {
		return s.update(ctx, args, "unavailable", "当前环境尚未开放股价横向对比。", in)
	}
	previous := state.priorScope()
	if previous.SubjectID == uuid.Nil {
		previous = state.Previous
	}
	useContext := request.SubjectSource == "context"
	window := request.TradingDays
	if window == 0 {
		window = spec.DefaultWindow
		if useContext && slices.Contains(spec.Windows, previous.Window) {
			window = previous.Window
		}
	}
	if len(request.StockMentions) > dataservice.ComparisonSubjectLimit {
		return clarify("每次可比较 2 至 5 只股票，请缩小范围。")
	}
	var ids []uuid.UUID
	kinds := []string{"stock"}
	if useContext && previous.SubjectID != uuid.Nil {
		ids = append(ids, previous.SubjectID)
	}
	mentions := slices.Clone(request.StockMentions)
	slices.SortStableFunc(mentions, func(a, b string) int {
		return cmp.Compare(subjectIndex(turn.Text, strings.TrimSpace(a)), subjectIndex(turn.Text, strings.TrimSpace(b)))
	})
	for _, mention := range mentions {
		matches, unresolved, err := s.subjects(ctx, turn.Text, kinds, []string{mention}, true)
		if errors.Is(err, errStockDirectory) {
			return s.update(ctx, args, "unavailable", "证券目录暂时不可用，请稍后重试。", in)
		}
		if err != nil {
			return err
		}
		if unresolved || len(matches) != 1 {
			return clarify("有比较对象无法唯一识别，请补充准确的股票名称或带市场后缀的代码。")
		}
		if !slices.Contains(ids, matches[0]) {
			ids = append(ids, matches[0])
		}
	}
	comparison := &comparisonScope{Kind: request.Kind}
	switch request.Kind {
	case compareIndustry:
		if len(ids) != 1 || (useContext && len(request.StockMentions) != 0) {
			return clarify("请指明一只基准股票，我可以比较它与所属行业的同行样本。")
		}
	case compareStocks:
		if len(ids) < 2 || len(ids) > dataservice.ComparisonSubjectLimit {
			return clarify("请补充要比较的股票名称或代码，每次可比较 2 至 5 只。")
		}
		comparison.OtherIDs = ids[1:]
	case comparePrevious:
		if !useContext || len(request.StockMentions) != 0 || previous.SubjectID == uuid.Nil || previous.Comparison == nil ||
			(previous.Comparison.Kind != compareIndustry && len(previous.Comparison.OtherIDs) == 0) {
			return clarify("请点名要比较的股票，或从已有对比结果继续追问。")
		}
		*comparison = *previous.Comparison
		ids = append([]uuid.UUID{previous.SubjectID}, comparison.OtherIDs...)
	default:
		return clarify("请明确要比较的股票范围。")
	}
	conflicts, err := s.scopeConflicts(ctx, turn.Text, kinds, ids)
	if err != nil {
		return err
	}
	if len(conflicts) != 0 {
		return clarify("请一次写清要比较的股票名称或代码。")
	}
	// A code written with its exchange still counts when it is not yet in the
	// catalog. The check only rejects a partial scope selected by the model.
	tickers := comparisonTicker.FindAllString(turn.Text, -1)
	if len(tickers) > 0 {
		entities, err := s.Entities(ctx, ids)
		if err != nil {
			return err
		}
		for _, code := range tickers {
			if containsSubject(turn.Text, code) && !slices.ContainsFunc(ids, func(id uuid.UUID) bool {
				return strings.EqualFold(entities[id].Code, code)
			}) {
				return clarify("有带市场后缀的股票代码未纳入对比，请核对代码。")
			}
		}
	}
	in.Scope = Scope{SubjectID: ids[0], Skill: scope.Skill, Topic: scope.Topic, Comparison: comparison}
	if !slices.Contains(spec.Windows, window) || !matchesTradingDays(turn.Text, request.TradingDays, window) {
		in.Scope.Window = spec.DefaultWindow
		if slices.Contains(spec.Windows, previous.Window) {
			in.Scope.Window = previous.Window
		}
		return clarify("请选择 5、10、20 或 60 个交易日。")
	}
	in.Scope.Window = window
	return s.planComparison(ctx, args, in, turn)
}

func (s Service) planComparison(ctx context.Context, args resolveArgs, in intent, turn Turn) error {
	scope := in.Scope
	comparison := scope.Comparison
	if comparison == nil || scope.SubjectID == uuid.Nil {
		return s.fail(ctx, args, "股价对比范围缺失，请重新描述问题。")
	}
	ids := append([]uuid.UUID{scope.SubjectID}, comparison.OtherIDs...)
	entities, err := s.Entities(ctx, ids)
	if err != nil {
		return err
	}
	subject := entities[scope.SubjectID]
	for _, id := range ids {
		if entity, ok := entities[id]; !ok || entity.Kind != "stock" || entity.Synthetic != subject.Synthetic {
			return s.fail(ctx, args, "有对比对象当前不可用，请选择已登记的股票。")
		}
	}
	p := harness.ComparisonParams{OtherIDs: comparison.OtherIDs, SampleDate: comparison.SampleDate, SampleProof: comparison.SampleProof}
	if s.Engine.PrepareTuShareComparison != nil {
		if time.Since(turn.CreatedAt) > 5*time.Minute {
			return s.fail(ctx, args, "准备对比行情超过时限，请重新发起。")
		}
		codes := make([]string, 0, len(comparison.OtherIDs))
		for _, id := range comparison.OtherIDs {
			codes = append(codes, entities[id].Code)
		}
		prep, sample, err := s.Engine.PrepareComparison(ctx, args.Workspace, args.Turn, args.Revision, subject.Code, codes)
		switch {
		case err != nil:
			return s.fail(ctx, args, errs.As(err).Message)
		case prep.Status == "failed":
			return s.fail(ctx, args, prep.Message)
		case prep.Status != "ready":
			if err := s.setPreparing(ctx, args, in); err != nil {
				if errors.Is(err, errTurnMoved) {
					return nil
				}
				return err
			}
			return errPreparing
		}
		if comparison.Kind == compareIndustry && len(comparison.OtherIDs) == 0 {
			p, err = s.resolveComparisonSample(ctx, sample)
			if err != nil {
				return s.fail(ctx, args, "已登记行业没有可用的同行样本，请点名要比较的股票。")
			}
		}
	} else if subject.Synthetic && comparison.Kind == compareIndustry && len(comparison.OtherIDs) == 0 {
		p, err = s.Engine.ComparisonFixtureSample(ctx, subject)
		if err != nil {
			return err
		}
	}
	if len(p.OtherIDs) == 0 {
		return s.update(ctx, args, "clarify", "已登记行业没有可用的同行样本，请点名要比较的股票。", in)
	}
	in.Scope.Comparison = &comparisonScope{Kind: comparison.Kind, OtherIDs: p.OtherIDs, SampleDate: p.SampleDate, SampleProof: p.SampleProof}
	return s.launchResearch(ctx, args, in, turn, func(launch harness.ConversationLaunch) (*harness.Plan, error) {
		return s.Engine.StartStockComparison(ctx, args.Workspace, subject, scope.Window, p, launch)
	})
}

func (s Service) resolveComparisonSample(ctx context.Context, sample dataservice.ComparisonSample) (harness.ComparisonParams, error) {
	p := harness.ComparisonParams{SampleDate: sample.Date, SampleProof: sample.ProofSHA256}
	if sample.Date == "" || sample.IndustryCode == "" || sample.ProofSHA256 == "" || len(sample.Codes) == 0 {
		return p, errors.New("empty industry sample")
	}
	for _, code := range sample.Codes {
		entity, err := s.Engine.Store.EntityByCode(ctx, code)
		if err != nil {
			return p, err
		}
		p.OtherIDs = append(p.OtherIDs, entity.ID)
	}
	return p, nil
}
