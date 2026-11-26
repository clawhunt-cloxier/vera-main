package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

const (
	dailyTradingTool = "inspect_daily_trading"
	financingTool    = "inspect_financing_balance"
	stockTool        = "analyze_stock"
	flowTool         = "analyze_capital_flow"
	deepTool         = "run_deep_research"
	refineTool       = "refine_research"
	referenceTool    = "reference_research"
	replyTool        = "respond_to_user"
)

type researchTool struct {
	Name, Skill, Topic, Description string
}

var researchTools = []researchTool{
	{dailyTradingTool, "stock_analysis", harness.DailyTradingTopic, "Inspect one stock's published daily turnover amount and volume, with a chart and evidence. Use for 成交量, 成交额, or whether the stock traded more actively. Does not fetch an intraday quote or price."},
	{financingTool, "capital_flow", harness.FinancingBalanceTopic, "Inspect one stock's disclosed financing balance and first-to-last change. Use for 融资余额 or outstanding margin financing. Does not infer investors' intent."},
	{stockTool, "stock_analysis", "", "Generate a formal stock analysis report across available financials, valuation metrics, trading, capital flows and risks. Use for a broad stock analysis or an explicit request for a full stock report."},
	{flowTool, "capital_flow", "", "Generate the formal capital flow report. Use for broad questions about 资金面, 资金动向, or disclosed holdings and flows. Only registered data and supported sections are used."},
	{deepTool, "deep_research", "", "Generate a formal deep research report about a company or industry, as permitted by the listed scope. Use when the user requests deep research or analysis of the business, financials and industry together."},
}

func (s Service) agentTools() []model.ToolSpec {
	tools := []model.ToolSpec{
		{Name: replyTool, Schema: "conversation-answer-output", Description: "选择服务展示的回复。问候 greeting，致谢 thanks，身份或功能 capabilities，金融知识 education，禁止的投资建议 decline_advice，非金融问题 out_of_scope，缺少必要信息 clarify，无相关数据或能力 unavailable。不生成正文，不创建研究。"},
		{Name: refineTool, Schema: "conversation-window-tool", Description: "调整所选结果或上一轮研究的窗口，或补充、更换同主题的标的。沿用标的时用 subject_source=context 和 subject_mentions=[]。"},
		{Name: referenceTool, Schema: "conversation-reference-tool", Description: "解释所选或最近报告里的结果、出处或原公式。问某个已交付的数怎么计算时用 kind=calculation。仅查看已交付内容。"},
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
				description += " Use coverage=company_industry only for explicit company plus industry research."
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
	Advice         string `json:"advice"`
}

func (s Service) runTool(ctx context.Context, args resolveArgs, turn Turn, state agentContext, call model.ToolCall) error {
	if call.Function.Name == referenceTool {
		var request referenceRequest
		if err := json.Unmarshal([]byte(call.Function.Arguments), &request); err != nil {
			return s.fail(ctx, args, "本轮引用参数无效，请重新描述问题。")
		}
		return s.referenceResearch(ctx, args, turn, state.Anchor, request)
	}
	var request researchRequest
	if err := json.Unmarshal([]byte(call.Function.Arguments), &request); err != nil {
		return s.fail(ctx, args, "本轮研究参数无效，请重新描述问题。")
	}
	if call.Function.Name == refineTool {
		return s.startResearch(ctx, args, turn, state, researchTool{}, request)
	}
	for _, tool := range researchTools {
		if tool.Name == call.Function.Name {
			return s.startResearch(ctx, args, turn, state, tool, request)
		}
	}
	return s.fail(ctx, args, "该研究能力尚未开放。")
}

func (s Service) startResearch(ctx context.Context, args resolveArgs, turn Turn, state agentContext, tool researchTool, request researchRequest) error {
	refine := tool.Name == ""
	previous := state.priorScope()
	scope := researchScope(tool, request, previous)
	in := intent{Scope: scope, AdviceDeclined: request.Advice == "declined" || (refine && state.Draft.AdviceDeclined)}
	clarify := func(message string) error {
		return s.update(ctx, args, "clarify", message, in)
	}
	if scope.Skill == "" {
		return clarify("请补充想看的指标，例如日成交或融资余额。")
	}
	spec, ok := s.Engine.ConversationResearchSpec(scope.Skill, scope.Topic)
	if !ok {
		return s.update(ctx, args, "unavailable", "当前环境尚未开放这项研究。", in)
	}

	if len(request.SubjectMentions) > 4 || (request.SubjectSource == "context" && len(request.SubjectMentions) > 0) {
		in.Scope.SubjectID = uuid.Nil
		return clarify("请明确这次要研究的证券、行业或市场。")
	}
	ids, unresolved, err := s.subjects(ctx, turn.Text, spec.Subjects, request.SubjectMentions)
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

	if scope.Window == 0 {
		scope.Window = spec.DefaultWindow
	}
	in.Scope.Window = scope.Window
	if (scope.Window != 0 && !slices.Contains(spec.Windows, scope.Window)) ||
		!matchesTradingDays(turn.Text, request.TradingDays, scope.Window) {
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
	if tool.Name == "" {
		scope.Skill, scope.Topic, scope.Mode = previous.Skill, previous.Topic, previous.Mode
	}
	if request.Coverage == "company_industry" {
		scope.Mode = request.Coverage
	}
	if request.SubjectSource == "context" {
		scope.SubjectID = previous.SubjectID
	}
	if scope.Window == 0 && (tool.Name == "" || (scope.Skill == previous.Skill && scope.Topic == previous.Topic && request.SubjectSource == "context")) {
		scope.Window = previous.Window
	}
	return scope
}

func (s Service) referenceResearch(ctx context.Context, args resolveArgs, turn Turn, anchor *Turn, request referenceRequest) error {
	const missing = "请在已交付的一轮研究中选择追问，再查看原始结果、证据或计算。"
	in := intent{AdviceDeclined: request.Advice == "declined"}
	if anchor == nil || anchor.ReportID == nil || anchor.ReportVersion == nil {
		return s.update(ctx, args, "clarify", missing, in)
	}
	if !matchesTradingDays(turn.Text, 0, anchor.Scope().Window) {
		return s.update(ctx, args, "clarify", "消息中的时间范围与原报告不同。请说明要按新范围研究，还是查看原报告。", in)
	}
	conflicts, err := s.subjectConflicts(ctx, turn.Text, nil, anchor.Scope().SubjectID)
	if err != nil {
		return err
	}
	if len(conflicts) != 0 {
		return s.update(ctx, args, "clarify", missing, in)
	}
	if request.SubjectMention != "" {
		ids, unresolved, err := s.subjects(ctx, turn.Text, nil, []string{request.SubjectMention})
		if err != nil {
			return err
		}
		if unresolved || len(ids) != 1 || ids[0] != anchor.Scope().SubjectID {
			return s.update(ctx, args, "clarify", missing, in)
		}
	}
	in.Reference, in.ReferenceKind = &anchor.ID, request.Kind
	return s.update(ctx, args, "reference", "已找到该轮研究的原始结果。", in)
}

// A model can select a tool, but must not silently ignore another explicit
// name or code. These catalog matches can only send the turn to clarification.
func (s Service) subjectConflicts(ctx context.Context, text string, kinds []string, selected uuid.UUID) ([]uuid.UUID, error) {
	references, err := s.catalog().ExplicitScopeReferences(ctx, text, kinds)
	if err != nil {
		return nil, err
	}
	var others []uuid.UUID
	for _, ref := range references {
		if ref.ID != selected && !slices.Contains(others, ref.ID) && containsSubject(text, ref.Mention) {
			others = append(others, ref.ID)
			if len(others) == 9 {
				break
			}
		}
	}
	return others, nil
}

// subjects binds supplied spans to exact catalog identities.
func (s Service) subjects(ctx context.Context, question string, kinds, mentions []string) ([]uuid.UUID, bool, error) {
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
		unresolved = unresolved || !matched
	}
	return ids, unresolved, nil
}

func containsSubject(question, mention string) bool {
	if mention == "" || utf8.RuneCountInString(mention) > 120 {
		return false
	}
	// Do not accept an ASCII ticker embedded inside another ticker or word.
	isASCIIWord := func(r rune) bool {
		return r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.')
	}
	question, mention = strings.ToLower(question), strings.ToLower(mention)
	if strings.ContainsFunc(mention, func(r rune) bool { return !isASCIIWord(r) }) {
		return strings.Contains(question, mention)
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
			return true
		}
		start = i + 1
	}
	return false
}
