package conversation

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

var shortStockResearchLead = regexp.MustCompile(
	"^(?:(?:请|我想|想|帮我|继续|接着|本轮|这轮|这次|现在|还是|仍|只|再|先|改|就|换(?:一家|一只)(?:股票|公司)?)[[:space:]　]*)*(?:查询|查看|看看|研究|分析|查|看)(?:一下)?[[:space:]　]*",
)

// Find an affirmative request for one short indicator. Merely mentioning a
// metric, or asking for its definition, cannot create a research draft.
func requestedShortStockTool(question string) (researchTool, bool) {
	var requested researchTool
	for _, clause := range strings.FieldsFunc(question, isScopeClauseBoundary) {
		clause = trimQuestion(clause)
		lead := shortStockResearchLead.FindStringIndex(clause)
		if lead == nil {
			continue
		}
		object := strings.TrimSpace(clause[lead[1]:])
		window, valid := parseTradingDayWindow(object)
		if valid && window.days > 0 && windowLeadIn.MatchString(strings.TrimSpace(object[:window.start])) {
			object = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(object[window.end:]), "的"))
		}
		for _, tool := range researchTools {
			if !isIndicatorOnlyQuestion(object, tool.Topic) {
				continue
			}
			if requested.Topic != "" && requested.Topic != tool.Topic {
				return researchTool{}, false
			}
			requested = tool
		}
	}
	return requested, requested.Topic != ""
}

// A clarification saves verified fields, even if the model would have asked
// for the stock in a generic chat reply. The normal validator handles the
// remaining window and subject checks and cannot start research without a stock.
func (s Service) clarifyMissingStock(ctx context.Context, args resolveArgs, turn Turn, state agentContext, requireExclusion bool) (bool, error) {
	previous := state.priorScope()
	if requireExclusion && (previous.SubjectID == uuid.Nil || !isStockIndicator(previous.Topic)) {
		return false, nil
	}
	tool, found := requestedShortStockTool(turn.Text)
	if !found {
		return false, nil
	}
	spec, available := s.Engine.ConversationResearchSpec(tool.Skill, tool.Topic)
	if !available {
		return false, nil
	}
	roles, err := s.stockResearchSubjectRoles(ctx, strings.ToLower(turn.Text), spec.Subjects)
	if err != nil {
		return false, err
	}
	if roles.conflicted || len(roles.possible) > 0 || previous.SubjectID != uuid.Nil && !roles.excludes(previous.SubjectID) {
		return false, nil
	}
	days, _ := statedTradingDays(turn.Text)
	return true, s.startResearch(ctx, args, turn, state, tool, researchRequest{
		SubjectSource: "message",
		TradingDays:   days,
	})
}

// A bare, unique catalog identifier supplies the missing stock only. Preserve
// the checked indicator and window in the immediately preceding draft.
func (s Service) fillPendingStock(ctx context.Context, args resolveArgs, turn Turn, state agentContext) (bool, error) {
	scope := state.Draft.Scope
	if !state.Draft.NeedsSubject || state.Draft.NeedsWindow || !isStockIndicator(scope.Topic) {
		return false, nil
	}
	spec, available := s.Engine.ConversationResearchSpec(scope.Skill, scope.Topic)
	if !available || !slices.Contains(spec.Windows, scope.Window) {
		return false, nil
	}
	mention := strings.TrimSpace(strings.TrimRight(turn.Text, "。！？!?；;，,."))
	mentions := []string{mention}
	if strings.ContainsAny(mention, "(（") {
		question := strings.ToLower(turn.Text)
		roles, err := s.stockResearchSubjectRoles(ctx, question, spec.Subjects)
		if err != nil || roles.conflicted {
			return false, err
		}
		mentions = roles.normalizeMentions(question, mentions)
	}
	ids, unresolved, err := s.subjects(ctx, turn.Text, spec.Subjects, mentions, false)
	if err != nil || unresolved || len(ids) != 1 {
		return false, err
	}
	for _, tool := range researchTools {
		if tool.Topic == scope.Topic && tool.Skill == scope.Skill {
			return true, s.startResearch(ctx, args, turn, state, tool, researchRequest{
				SubjectSource:   "message",
				SubjectMentions: mentions,
			})
		}
	}
	return false, nil
}
