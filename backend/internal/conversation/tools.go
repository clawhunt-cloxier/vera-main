package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

const (
	dailyTradingTool = "inspect_daily_trading"
	financingTool    = "inspect_financing_balance"
	stockTool        = "analyze_stock"
	flowTool         = "analyze_capital_flow"
	deepTool         = "run_deep_research"
	comparisonTool   = "compare_stock_performance"
	referenceTool    = "reference_research"
	replyTool        = "respond_to_user"
)

type researchTool struct {
	Name, Skill, Topic, Description string
}

var researchTools = []researchTool{
	{dailyTradingTool, "stock_analysis", harness.DailyTradingTopic, "日成交：查看个股已公布的每日成交额、成交量或成交活跃度，提供图表与证据。"},
	{financingTool, "capital_flow", harness.FinancingBalanceTopic, "融资余额：查看个股已披露的未偿还融资余额及区间变化。用户在任何个股研究后改问融资余额时，调用此工具，沿用该标的。"},
	{stockTool, "stock_analysis", "", "Inspect a stock's recent market performance, including closing prices, and render an analysis from available evidence. For any relative share price comparison between stocks or against a same-industry sample, use compare_stock_performance instead. Use for recent performance (最近表现、近期走势), broad stock analysis or an explicit request for a full stock report. Use inspect_daily_trading for a query specifically about volume or turnover amount."},
	{flowTool, "capital_flow", "", "Generate the formal capital flow report. Use for broad questions about 资金面, 资金动向, or disclosed holdings and flows. Only registered data and supported sections are used."},
	{deepTool, "deep_research", "", "Generate a formal deep research report about a company or industry, as permitted by the listed scope. Use when the user requests deep research or analysis of the business, financials and industry together."},
}

func (s Service) agentTools() []model.ToolSpec {
	tools := []model.ToolSpec{
		{Name: replyTool, Schema: "conversation-answer-output", Description: "把你为本轮用户写的纯文本回复放在 message。问候 greeting，致谢 thanks，身份或功能 capabilities，通用金融知识 education，婉拒代做投资决策 decline_advice，非金融或不宜继续的话题 out_of_scope，需用户补充 clarify，缺能力或数据 unavailable。不创建研究。"},
		{Name: referenceTool, Schema: "conversation-reference-tool", Description: "引用本会话已交付的结果、证据或原公式。点选的结果优先。用户明确回到一轮时，从 delivered_scopes 选择对应的 result_turn。新指标使用各自的研究工具。"},
	}
	if s.Engine.Gateway.Profiles["router"].SupportsEducationSearch() {
		tools[0].Description += " 通用金融知识能归入登记公开主题时，使用 lookup_finance_knowledge。"
		tools = append(tools, model.ToolSpec{
			Name: knowledgeTool, Schema: "conversation-knowledge-tool",
			Description: "为已登记主题的通用金融概念、机制、交易术语及其追问查证公开资料。定义类问题也使用此工具。具体证券数据和研报使用各自的研究工具。",
		})
	}
	if spec, ok := s.Engine.ConversationResearchSpec("stock_analysis", harness.ComparisonTopic); ok {
		tools = append(tools, model.ToolSpec{Name: comparisonTool, Schema: "conversation-comparison-tool",
			Description: fmt.Sprintf("对比多只 A 股的历史收盘价、复权走势和同期涨跌幅。支持用户点名、比较当前标的与其申万二级同行样本、继续上一组。支持一只股票与指数比较，或在多股比较里加入申万二级行业指数、沪深300、中证500、中证1000。简短追问“大盘呢”沿用已有范围并默认加入沪深300。支持 %v 个交易日，默认 %d。", spec.Windows, spec.DefaultWindow)})
	}
	for _, tool := range researchTools {
		spec, ok := s.Engine.ConversationResearchSpec(tool.Skill, tool.Topic)
		if !ok {
			continue
		}
		description := fmt.Sprintf("%s Subject types: %s.", tool.Description, strings.Join(spec.Subjects, ", "))
		schema := "conversation-window-tool"
		if len(spec.Windows) > 0 {
			description += fmt.Sprintf("Trailing trading-day windows %v, default %d.", spec.Windows, spec.DefaultWindow)
		} else {
			schema = "conversation-depth-tool"
			if slices.Contains(spec.Modes, "company_industry") {
				description += " Use coverage=company_industry for an explicit request for company plus industry research or to explicitly keep that scope."
			} else {
				description += " Use coverage=subject."
			}
		}
		tools = append(tools, model.ToolSpec{Name: tool.Name, Schema: schema, Description: description})
	}
	return tools
}

type researchRequest struct {
	SubjectMentions []string `json:"subject_mentions"`
	SubjectSource   string   `json:"subject_source"`
	TradingDays     int      `json:"trading_days"`
	Coverage        string   `json:"coverage"`
	Advice          string   `json:"advice"`
}

type referenceRequest struct {
	Kind           string `json:"kind"`
	SubjectMention string `json:"subject_mention"`
	ResultTurn     int64  `json:"result_turn"`
	Advice         string `json:"advice"`
}

func (s Service) runTool(ctx context.Context, args resolveArgs, turn Turn, state agentContext, call model.ToolCall) error {
	if call.Function.Name == comparisonTool {
		var request comparisonRequest
		if err := json.Unmarshal([]byte(call.Function.Arguments), &request); err != nil {
			return s.fail(ctx, args, "本轮对比参数无效，请重新描述问题。")
		}
		return s.startComparison(ctx, args, turn, state, request)
	}
	if call.Function.Name == referenceTool {
		var request referenceRequest
		if err := json.Unmarshal([]byte(call.Function.Arguments), &request); err != nil {
			return s.fail(ctx, args, "本轮引用参数无效，请重新描述问题。")
		}
		return s.referenceResearch(ctx, args, turn, state.References, request)
	}
	var request researchRequest
	if err := json.Unmarshal([]byte(call.Function.Arguments), &request); err != nil {
		return s.fail(ctx, args, "本轮研究参数无效，请重新描述问题。")
	}
	for _, tool := range researchTools {
		if tool.Name == call.Function.Name {
			return s.startResearch(ctx, args, turn, state, tool, request)
		}
	}
	return s.fail(ctx, args, "该研究能力尚未开放。")
}

func (s Service) startResearch(ctx context.Context, args resolveArgs, turn Turn, state agentContext, tool researchTool, request researchRequest) error {
	previous := state.priorScope()
	scope := researchScope(tool, request, previous)
	sameTopic := scope.Skill == previous.Skill && scope.Topic == previous.Topic
	in := intent{Scope: scope, AdviceDeclined: request.Advice == "declined" || (sameTopic && state.Draft.AdviceDeclined)}
	clarify := func(message string) error {
		return s.update(ctx, args, "clarify", message, in)
	}
	spec, ok := s.Engine.ConversationResearchSpec(scope.Skill, scope.Topic)
	if !ok {
		return s.update(ctx, args, "unavailable", "当前环境尚未开放这项研究。", in)
	}

	if len(request.SubjectMentions) > 4 || (request.SubjectSource == "context" && len(request.SubjectMentions) > 0) {
		in.Scope.SubjectID = uuid.Nil
		return clarify("请明确这次要研究的证券、行业或市场。")
	}
	ids, unresolved, err := s.subjects(ctx, turn.Text, spec.Subjects, request.SubjectMentions, true)
	if errors.Is(err, errStockDirectory) {
		return s.update(ctx, args, "unavailable", "证券目录暂时不可用，请稍后重试。", in)
	}
	if err != nil {
		return err
	}
	if len(ids) == 1 && !unresolved {
		in.Scope.SubjectID = ids[0]
	}
	if len(ids) > 1 || unresolved {
		in.Scope.SubjectID = uuid.Nil
		in.Candidates = ids
	}
	if in.Scope.SubjectID != uuid.Nil {
		kinds := spec.Subjects
		if scope.Mode == "company_industry" {
			kinds = []string{"stock"}
		}
		conflicts, err := s.subjectConflicts(ctx, turn.Text, kinds, in.Scope.SubjectID)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			ids = append([]uuid.UUID{in.Scope.SubjectID}, conflicts...)
			in.Candidates = ids
			in.Scope.SubjectID = uuid.Nil
		}
	}

	sameStockBrief := request.SubjectSource == "context" && scope.Topic != "" && previous.Topic != ""
	canInheritWindow := (sameTopic || sameStockBrief) && slices.Contains(spec.Windows, previous.Window)
	if scope.Window == 0 {
		scope.Window = spec.DefaultWindow
		if canInheritWindow {
			scope.Window = previous.Window
		}
	}
	in.Scope.Window = scope.Window
	requestedDays := request.TradingDays
	if canInheritWindow && requestedDays == previous.Window {
		// A model may echo a verified window when the user only supplies the stock.
		// Explicit dates still have to match the text of this turn.
		requestedDays = 0
	}
	if (scope.Window != 0 && !slices.Contains(spec.Windows, scope.Window)) ||
		!matchesTradingDays(turn.Text, requestedDays, scope.Window) {
		// Keep the already verified window and subject for the clarification.
		in.Scope.Window = spec.DefaultWindow
		if slices.Contains(spec.Windows, previous.Window) && previous.Skill == scope.Skill && previous.Topic == scope.Topic {
			in.Scope.Window = previous.Window
		}
		if len(spec.Windows) == 0 {
			return clarify("这项研究按各章节的已披露数据生成。请说明要继续深度研究，或改查具体交易指标。")
		}
		values := make([]string, 0, len(spec.Windows))
		for _, days := range spec.Windows {
			values = append(values, strconv.Itoa(days))
		}
		return clarify("请选择 " + strings.Join(values, "、") + " 个交易日。")
	}
	if scope.Mode != "" && !slices.Contains(spec.Modes, scope.Mode) {
		in.Scope.Mode = ""
		return clarify("当前数据源无法生成公司与行业联合报告。可以改为公司深度研究。")
	}
	if in.Scope.SubjectID == uuid.Nil {
		in.NeedsSubject = true
		if len(ids) > 1 || (len(ids) > 0 && unresolved) {
			return clarify("本轮研究需要一个明确的研究对象。请选择或补充对象。")
		}
		return clarify("请补充或搜索研究对象的名称或代码。")
	}
	return s.plan(ctx, args, in, turn)
}

func researchScope(tool researchTool, request researchRequest, previous Scope) Scope {
	scope := Scope{Skill: tool.Skill, Topic: tool.Topic, Window: request.TradingDays}
	if request.Coverage == "company_industry" {
		scope.Mode = request.Coverage
	}
	if request.SubjectSource == "context" {
		scope.SubjectID = previous.SubjectID
	}
	return scope
}

func (s Service) referenceResearch(ctx context.Context, args resolveArgs, turn Turn, references []Turn, request referenceRequest) error {
	const missing = "请在已交付的一轮研究中选择追问，再查看原始结果、证据或计算。"
	in := intent{AdviceDeclined: request.Advice == "declined"}
	if len(references) == 0 {
		return s.update(ctx, args, "clarify", missing, in)
	}
	anchor := references[0]
	if request.ResultTurn != 0 && turn.ReplyTo == nil {
		selected := slices.IndexFunc(references, func(ref Turn) bool { return ref.Ordinal == request.ResultTurn })
		if selected < 0 {
			return s.update(ctx, args, "clarify", missing, in)
		}
		anchor = references[selected]
	}
	matches, err := s.referenceMatches(ctx, turn.Text, anchor.Scope(), request.SubjectMention)
	if err != nil {
		return err
	}
	if !matches && turn.ReplyTo == nil {
		// The tool chose to reference an existing result. If the proposed turn
		// conflicts with the stated subject, use only a unique compatible result.
		proposed := anchor.ID
		for _, candidate := range references {
			if candidate.ID == proposed || !matchesTradingDays(turn.Text, 0, candidate.Scope().Window) {
				continue
			}
			compatible, err := s.referenceMatches(ctx, turn.Text, candidate.Scope(), request.SubjectMention)
			if err != nil {
				return err
			}
			if compatible {
				if matches {
					return s.update(ctx, args, "clarify", missing, in)
				}
				matches = true
				anchor = candidate
			}
		}
	}
	if !matches {
		return s.update(ctx, args, "clarify", missing, in)
	}
	if !matchesTradingDays(turn.Text, 0, anchor.Scope().Window) {
		return s.update(ctx, args, "clarify", "消息中的时间范围与原报告不同。请说明要按新范围研究，还是查看原报告。", in)
	}
	in.Reference, in.ReferenceKind = &anchor.ID, request.Kind
	return s.update(ctx, args, "reference", "已找到该轮研究的原始结果。", in)
}

func (s Service) referenceMatches(ctx context.Context, question string, scope Scope, mention string) (bool, error) {
	selected := scopeSubjects(scope)
	conflicts, err := s.scopeConflicts(ctx, question, nil, selected)
	if err != nil {
		return false, err
	}
	if len(conflicts) != 0 {
		return false, nil
	}
	// Ignore a mention invented by the model. If the proposed name occurs in
	// the message, require an exact reference so a truncated ticker cannot
	// fall back to the old report.
	mention = strings.TrimSpace(mention)
	includedIndex := scope.Comparison != nil && containsSubject(question, mention) &&
		slices.Contains(scope.Comparison.BroadIndexes, dataservice.ResolveComparisonBroadIndex(mention))
	if !includedIndex && mention != "" && strings.Contains(strings.ToLower(question), strings.ToLower(mention)) {
		ids, unresolved, err := s.subjects(ctx, question, nil, []string{mention}, false)
		if err != nil {
			return false, err
		}
		return !unresolved && len(ids) == 1 && slices.Contains(selected, ids[0]), nil
	}
	return true, nil
}

func scopeSubjects(scope Scope) []uuid.UUID {
	selected := []uuid.UUID{scope.SubjectID}
	if scope.Comparison != nil {
		selected = append(selected, scope.Comparison.OtherIDs...)
	}
	return selected
}

// A model can select a tool, but must not silently ignore another explicit
// name or code. These catalog matches can only send the turn to clarification.
func (s Service) subjectConflicts(ctx context.Context, text string, kinds []string, selected uuid.UUID) ([]uuid.UUID, error) {
	return s.scopeConflicts(ctx, text, kinds, []uuid.UUID{selected})
}

func (s Service) scopeConflicts(ctx context.Context, text string, kinds []string, selected []uuid.UUID) ([]uuid.UUID, error) {
	references, err := s.catalog().ExplicitScopeReferences(ctx, text, kinds)
	if err != nil {
		return nil, err
	}
	var others []uuid.UUID
	for _, ref := range references {
		if !slices.Contains(selected, ref.ID) && !slices.Contains(others, ref.ID) && containsSubject(text, ref.Mention) {
			others = append(others, ref.ID)
			if len(others) == 9 {
				break
			}
		}
	}
	return others, nil
}

var errStockDirectory = errors.New("stock directory unavailable")

// subjects binds supplied spans to exact catalog identities. Only a new
// research request may discover an identity through the registered provider.
func (s Service) subjects(ctx context.Context, question string, kinds, mentions []string, discover bool) ([]uuid.UUID, bool, error) {
	var ids []uuid.UUID
	unresolved := false
	kind := ""
	if len(kinds) == 1 {
		kind = kinds[0]
	}
	for _, raw := range mentions {
		mention := strings.TrimSpace(raw)
		if !containsSubject(question, mention) {
			unresolved = true
			continue
		}
		matches, err := s.catalog().ResolveAlias(ctx, mention, kind)
		if err != nil {
			return nil, false, err
		}
		matched := false
		for _, entity := range matches {
			if len(kinds) > 0 && !slices.Contains(kinds, entity.Kind) {
				continue
			}
			matched = true
			if !slices.Contains(ids, entity.ID) && len(ids) < 10 {
				ids = append(ids, entity.ID)
			}
		}
		if !matched && discover && s.Engine.RealResearch && s.Engine.Brief != nil && slices.Contains(kinds, "stock") &&
			(emquant.IsStockName(mention) || emquant.IsBareStockCode(mention) || emquant.IsListedStockCode(strings.ToUpper(mention))) {
			err := s.Engine.WithChoiceResearchUse(ctx, func() error {
				candidates, err := s.Engine.Brief.DiscoverStocks(ctx, s.catalog(), mention)
				if err != nil {
					return err
				}
				for _, entity := range candidates {
					if emquant.ExactStockMatch(entity, mention) {
						matched = true
						if !slices.Contains(ids, entity.ID) && len(ids) < 10 {
							ids = append(ids, entity.ID)
						}
					}
				}
				return nil
			})
			if err != nil {
				return nil, false, fmt.Errorf("%w: %w", errStockDirectory, err)
			}
		}
		unresolved = unresolved || !matched
	}
	return ids, unresolved, nil
}

func containsSubject(question, mention string) bool {
	return subjectIndex(question, mention) >= 0
}

func subjectIndex(question, mention string) int {
	if mention == "" || utf8.RuneCountInString(mention) > 120 {
		return -1
	}
	// Do not accept an ASCII ticker embedded inside another ticker or word.
	isASCIIWord := func(r rune) bool {
		return r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.')
	}
	question, mention = strings.ToLower(question), strings.ToLower(mention)
	if strings.ContainsFunc(mention, func(r rune) bool { return !isASCIIWord(r) }) {
		return strings.Index(question, mention)
	}
	for start := 0; start < len(question); {
		index := strings.Index(question[start:], mention)
		if index < 0 {
			break
		}
		i := start + index
		before, _ := utf8.DecodeLastRuneInString(question[:i])
		suffix := question[i+len(mention):]
		after, _ := utf8.DecodeRuneInString(suffix)
		if after == '.' {
			following, _ := utf8.DecodeRuneInString(suffix[1:])
			if !isASCIIWord(following) {
				after = following
			}
		}
		if !isASCIIWord(before) && !isASCIIWord(after) {
			return i
		}
		start = i + 1
	}
	return -1
}
