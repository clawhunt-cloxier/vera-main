package conversation

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Match a known window reference before a number. In 同一个交易日窗口, 一
// is part of 同一个 (the same), rather than a count of trading sessions.
const tradingDayWindowReference = `(?:同[[:space:]　]*[一1１](?:[[:space:]　]*个)?|同样的|原(?:来|先)?的|[这那][[:space:]　]*个)[[:space:]　]*交易日[[:space:]　]*(?:窗口|范围|区间|周期)`

const rejectTradingDayWindow = `(?:(?:不|别|不要|不能|不再|不必|不用|无需|无须|不需要|不想|不应|不可|不允许|避免)[[:space:]　]*(?:(?:再|继续)[[:space:]　]*)?(?:沿用|使用|复用|采用|用|按|照|选用|保留|保持)|不用)`

var windowReferenceRejectBefore = regexp.MustCompile(rejectTradingDayWindow + `$`)
var windowReferenceRejectAfter = regexp.MustCompile(`^(?:(?:这轮|这一轮|本轮|这次|先|暂时)[[:space:]　]*)*` + rejectTradingDayWindow + `(?:了|吧)?$`)
var windowReferenceKeepBefore = regexp.MustCompile(`(?:沿用|使用|复用|采用|用|按|照|选用|保留|保持|维持|以)$`)
var windowReferenceKeepAfter = regexp.MustCompile(`^(?:(?:这轮|本轮|这次|仍|也|还是|就|继续)[[:space:]　]*)*(?:不变|照旧|沿用|保持不变|保留)(?:了|吧)?$`)

var tradingDayExpression = regexp.MustCompile(tradingDayWindowReference + `|` +
	`([+\-−负]?[0-9０１２３４５６７８９零〇一二两三四五六七八九十百千]+(?:[[:space:]　]*[.．～~至到－-][[:space:]　]*[0-9０１２３４５６７８９零〇一二两三四五六七八九十百千]+)?)[[:space:]　]*(?:个[[:space:]　]*)?交易日`)
var calendarPeriod = regexp.MustCompile(`[0-9０１２３４５６７８９]{4}[-年/]|[0-9０１２３４５６７８９零〇一二两三四五六七八九十百]+[[:space:]　]*(?:个[[:space:]　]*)?(?:自然日|日|天|周|月|季度|年)|(?:上|本|这)(?:一?周|个月|月|半年|季度|年)|[昨前]天|去年|至今|到现在|上市以来|从上市`)
var stockSwitchCode = regexp.MustCompile(`(?i)^(?:请[[:space:]　]*)?(?:换成|换到|改查|改看|切换到)[[:space:]　]*([0-9]{6}\.(?:SH|SZ|BJ))`)
var excludeStockAction = regexp.MustCompile("^(?:(?:这轮|这一轮|本轮|这次|暂时|先|就)|[[:space:]　])*(?:排除|(?:不要|不用|别|不)(?:再)?(?:查询|查看|查|看|研究|分析|考虑|换成|换到|改看|改查|切换到))(?:了|吧|[[:space:]　])*$")
var stockAlternative = regexp.MustCompile("^(?:只是|仅是|只作(?:为)?|仅作(?:为)?|仅供|是|作为)(?:一个)?(?:备选|候选|参考)(?:项|对象)?$")
var deferredStockAction = regexp.MustCompile("^(?:留到|留待|放到)(?:下次|下一轮)(?:再)?(?:查询|查看|查|看|研究|分析|考虑)$")
var historicalStockLeadIn = regexp.MustCompile("^(?:之前|此前|上一轮|上次|先前|刚才)(?:(?:选|查询|查看|查|看|研究)的)?是$")
var windowLeadIn = regexp.MustCompile("^(?:(?:那(?:么)?|就|则|请|只|先)[[:space:]　]*)*(?:把[[:space:]　]*)?(?:(?:窗口|时间(?:窗口|范围)?|范围)[[:space:]　]*)?(?:(?:改成|改为|改用|改到|换成|调到|调整到|调整为|设为|拉长到|拉长至|缩短到|缩短至)[[:space:]　]*)?(?:(?:查看|查一下|看一下|查|看|按)[[:space:]　]*)?(?:近|最近|过去)?$")
var unchangedResearchFields = regexp.MustCompile("^(?:(?:股票|标的|指标|(?:股票|标的)(?:和|与|、)指标|其他|其余)(?:都)?(?:不变|照旧|保持不变)|同一只股票|沿用原标的)$")

var writtenWindows = map[string]int{
	"一": 1, "五": 5, "十": 10, "二十": 20, "六十": 60, "一百二十": 120, "二百五十": 250, "两百五十": 250,
}

func matchesTradingDays(text string, requested, effective int) bool {
	stated, valid := statedTradingDays(text)
	if !valid {
		return false
	}
	if stated == 0 {
		return requested <= 0
	}
	return stated == effective
}

type tradingDayWindow struct {
	days            int
	start, end      int
	referencesPrior bool
	keepsPrior      bool
	rejectsPrior    bool
}

func statedTradingDays(text string) (int, bool) {
	window, valid := parseTradingDayWindow(text)
	return window.days, valid
}

func parseTradingDayWindow(text string) (tradingDayWindow, bool) {
	if calendarPeriod.MatchString(tradingDayExpression.ReplaceAllString(text, "")) {
		return tradingDayWindow{}, false
	}
	var window tradingDayWindow
	for _, match := range tradingDayExpression.FindAllStringSubmatchIndex(text, -1) {
		if match[2] < 0 {
			window.referencesPrior = true
			before := trimQuestion(text[:match[0]])
			after, _ := splitScopeClause(text[match[1]:])
			after = trimQuestion(after)
			switch {
			case windowReferenceRejectBefore.MatchString(before) || windowReferenceRejectAfter.MatchString(after):
				window.rejectsPrior = true
			case windowReferenceKeepBefore.MatchString(before) || windowReferenceKeepAfter.MatchString(after):
				window.keepsPrior = true
			}
			continue
		}
		number := strings.Map(func(r rune) rune {
			if r >= '０' && r <= '９' {
				return '0' + r - '０'
			}
			return r
		}, text[match[2]:match[3]])
		days, err := strconv.Atoi(number)
		if err != nil {
			days = writtenWindows[number]
		}
		if days <= 0 || window.days != 0 && days != window.days {
			return tradingDayWindow{}, false
		}
		if window.days == 0 {
			window.days, window.start, window.end = days, match[0], match[1]
		}
	}
	return window, true
}

// A window-only follow-up has no new subject or indicator to resolve.
// Leave mixed requests to the router and the regular scope checks.
func windowOnlyTradingDays(text string) int {
	window, valid := parseTradingDayWindow(text)
	if !valid || window.days == 0 {
		return 0
	}
	before := strings.FieldsFunc(text[:window.start], isScopeClauseBoundary)
	for i, clause := range before {
		clause = trimQuestion(clause)
		if clause != "" && !unchangedResearchFields.MatchString(clause) &&
			(i != len(before)-1 || !windowLeadIn.MatchString(clause)) {
			return 0
		}
	}
	for _, clause := range strings.FieldsFunc(text[window.end:], isScopeClauseBoundary) {
		clause = trimQuestion(clause)
		if !unchangedResearchFields.MatchString(clause) && !slices.Contains([]string{"", "吧", "即可", "就行"}, clause) {
			return 0
		}
	}
	return window.days
}

func trimQuestion(text string) string {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}

type stockSubjectRoles struct {
	possible   []subjectSpan
	excluded   []subjectSpan
	context    []subjectSpan
	groups     []stockIdentityGroup
	conflicted bool
}

func (s Service) stockResearchSubjectRoles(ctx context.Context, question string, kinds []string) (stockSubjectRoles, error) {
	var roles stockSubjectRoles
	spans, err := s.explicitSubjectSpans(ctx, question, kinds)
	if err != nil {
		return roles, err
	}
	roles.groups, roles.conflicted = groupStockIdentities(question, spans)
	for _, group := range roles.groups {
		spans = append(spans, group.subjectSpan)
	}
	for _, span := range spans {
		if slices.ContainsFunc(roles.groups, func(group stockIdentityGroup) bool {
			return group.strictlyContains(span)
		}) {
			continue
		}
		before, after, next := stockMentionClauses(question, span)
		switch {
		case explicitlyExcludedStock(before, after, next):
			roles.excluded = append(roles.excluded, span)
		case historicalStockLeadIn.MatchString(before) && after == "":
			roles.context = append(roles.context, span)
		default:
			roles.possible = append(roles.possible, span)
		}
	}
	return roles, nil
}

func (roles stockSubjectRoles) excludes(id uuid.UUID) bool {
	matches := func(span subjectSpan) bool { return span.id == id }
	return slices.ContainsFunc(roles.excluded, matches) && !slices.ContainsFunc(roles.possible, matches)
}

func (roles stockSubjectRoles) nonTargetMention(question, mention string) bool {
	found := false
	width := len(strings.ToLower(mention))
	for start := range subjectOccurrences(question, mention) {
		contains := func(span subjectSpan) bool { return span.start <= start && start+width <= span.end }
		if !slices.ContainsFunc(roles.excluded, contains) && !slices.ContainsFunc(roles.context, contains) {
			return false
		}
		found = true
	}
	return found
}

// A catalog match gives an identity, not its role. Bind exclusions to the
// occurrence they qualify. A vague mention or ambiguous group remains a
// possible target, and a separate positive occurrence still counts.
func explicitlyExcludedStock(before, after, next string) bool {
	if excludeStockAction.MatchString(before) && slices.Contains([]string{"", "了", "吧"}, after) {
		return true
	}
	if before != "" && before != "至于" {
		return false
	}
	if excludeStockAction.MatchString(after) || deferredStockAction.MatchString(after) {
		return true
	}
	return stockAlternative.MatchString(after) &&
		(excludeStockAction.MatchString(next) || deferredStockAction.MatchString(next))
}

func stockMentionClauses(question string, span subjectSpan) (before, after, next string) {
	before = question[:span.start]
	if at := strings.LastIndexFunc(before, isScopeClauseBoundary); at >= 0 {
		_, size := utf8.DecodeRuneInString(before[at:])
		before = before[at+size:]
	}
	after, next = splitScopeClause(question[span.end:])
	next, _ = splitScopeClause(next)
	return trimQuestion(before), trimQuestion(after), trimQuestion(next)
}

func isScopeClauseBoundary(r rune) bool {
	return strings.ContainsRune("，,；;。！？!?\n", r)
}

func splitScopeClause(text string) (string, string) {
	at := strings.IndexFunc(text, isScopeClauseBoundary)
	if at < 0 {
		return text, ""
	}
	_, size := utf8.DecodeRuneInString(text[at:])
	return text[:at], text[at+size:]
}
