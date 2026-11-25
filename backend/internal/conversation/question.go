package conversation

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
)

var (
	windowPattern  = regexp.MustCompile(`([0-9]+|[零〇一二两三四五六七八九十百千万]+)\s*(?:个\s*)?交易日`)
	calendarWindow = regexp.MustCompile(`[0-9]{4}[-年/]|[0-9零〇一二两三四五六七八九十百]+\s*(?:个\s*)?(?:自然日|日|天|周|月|季度|年)|(?:上|本|这)(?:一?周|个月|月|半年|季度|年)|[昨前]天|去年|至今|到现在|上市以来|从上市`)
	advice         = regexp.MustCompile(`目标价|投资评级|情景概率|低估吗|高估吗|推荐.{0,5}(个股|股票)|能不能买|能买[吗么]|可以买[吗么]|可以卖[吗么]|该不该|要不要(买|卖|入|加仓|减仓)|值不值得|建议.{0,4}(买|卖|加仓|减仓)|买入点|卖出点|买入价|卖出价|止损|抄底|仓位|怎么(买|卖)|现在.{0,4}(买|卖)|给.{0,5}评级|操作建议|买卖建议|^\s*(买入|卖出|加仓|减仓)(?:\s|$)`)
	prediction     = regexp.MustCompile(`会[不否]?(涨|跌)|会不会(涨|跌)|预测|涨到|跌到|(明天|下周|下个月|明年).{0,6}(涨|跌|走势|点位)`)
	outsideMarket  = regexp.MustCompile(`(?i)港股|美股|港交所|纳斯达克|比特币|期货|外汇|\b[A-Z]{1,5}\.(HK|US)\b|\b[0-9]{4,5}\.HK\b`)
	// Reuse a confirmed object only for shorthand without an unidentified subject.
	contextFiller = regexp.MustCompile(`同一个标的|同一只|同样的|同样|这只股票|这只|这支|该股|这家公司|这个|它的|它|上次的|上次|上一轮|刚才的|刚才|最近|过去|近|再查|再看|再来|重新|更新|继续|看一下|查一下|看看|查下|一下|换成|改成|改为|改到|改|缩短到|拉长到|用|请|能否|能看|的|变化趋势|怎么变|变化|趋势|怎么样|如何|多少|是什么|情况|呢|吗|吧|显示|帮我|生成|给我|来一份|完整个股分析|完整分析|完整研报|个股研报|深度研究|融资余额|融资变化|融资情况|日成交|成交额|成交量|成交数据|成交情况|交易量|[\s？，。！、；：?!,.]`)
)

var referenceFiller = regexp.MustCompile(`这个数|刚才那条|刚才的结论|这些数据|这组数据|数据来源|原始数据|来源|证据|出处|溯源|哪来的|哪里来的|哪儿来的|怎么算|如何计算|怎么计算|公式|计算过程|计算依据|上次结果|原结果|复述|解释一下|解读一下|来自哪里|在哪里|怎么得出的|怎么来的|数字|结果|结论|有|数|看`)

type question struct {
	scope             Scope
	reference         string
	referenceImplicit bool
	inherit           bool
	differentSubject  bool
	windowSpecified   bool
	missing           string
}

func hasAny(text string, terms ...string) bool {
	return slices.ContainsFunc(terms, func(term string) bool { return strings.Contains(text, term) })
}

func parseQuestion(text string) question {
	q := question{differentSubject: hasAny(text, "换成", "换只", "换一只", "换个标的", "改看", "看看别的")}
	switch {
	case advice.MatchString(text) || prediction.MatchString(text):
		q.missing = "Vera 提供可核验的历史事实和计算。请改问已披露数据、交易记录或融资余额。"
	case outsideMarket.MatchString(text):
		q.missing = "当前对话研究覆盖 A 股。请提供一个已登记的 A 股证券。"
	case hasAny(text, "用原来的截止", "同一截止", "保持截止", "原快照", "沿用原数据", "相同快照"):
		q.missing = "可以查看原报告及其冻结数据。若需要新指标，请重新确认其数据截止。"
	case hasAny(text, "回测", "量化策略", "组合优化", "因子检验", "专家会诊", "多专家"):
		q.missing = "该研究能力尚未开放。你可以查询已披露的交易和融资数据。"
	}
	if q.missing != "" {
		return q
	}

	remainder, window, specified, problem := readWindow(text)
	if problem != "" {
		q.missing = problem
		return q
	}
	q.scope.Window, q.windowSpecified = window, specified
	q.inherit = strings.TrimSpace(contextFiller.ReplaceAllString(remainder, "")) == ""
	if specified && q.inherit {
		q.differentSubject = false
	}

	if !specified && !q.differentSubject && !hasAny(text, "重新", "更新", "新取", "再算") {
		switch {
		case hasAny(text, "来源", "证据", "出处", "溯源", "哪来的", "哪里来的", "哪儿来的"):
			q.reference = "evidence"
		case hasAny(text, "怎么算", "如何计算", "怎么计算", "公式", "计算过程", "计算依据"):
			q.reference = "calculation"
		case hasAny(text, "刚才的结论", "上次结果", "原结果", "复述", "解释一下", "解读一下", "刚才那条"):
			q.reference = "result"
		}
	}

	full := hasAny(text, "研报", "报告", "完整个股", "完整分析", "深度研究", "公司研究", "基本面", "财报", "公告", "估值", "分红", "行业研究", "产业链", "龙虎榜", "资金流向", "资金动向", "机构持仓", "财务专家", "资金专家", "行业专家")
	margin := hasAny(text, "融资余额", "融资变化", "融资情况")
	trade := hasAny(text, "日成交", "成交额", "成交量", "成交情况", "成交数据", "交易量")
	otherMetric := hasAny(text, "融券", "融资买入", "两融余额", "融资净买入", "杠杆资金", "股价", "涨跌幅", "换手", "收盘价", "日线", "实时", "盘中", "股息率")
	switch {
	case q.reference != "":
		q.referenceImplicit = strings.TrimSpace(contextFiller.ReplaceAllString(referenceFiller.ReplaceAllString(remainder, ""), "")) == ""
		if margin && !trade {
			q.scope.Topic = harness.FinancingBalanceTopic
		} else if trade && !margin {
			q.scope.Topic = harness.DailyTradingTopic
		}
		return q
	case full:
		switch {
		case hasAny(text, "深度", "行业", "公司研究", "产业链"):
			q.scope.Skill = "deep_research"
		case margin || hasAny(text, "资金", "龙虎榜", "两融", "持仓"):
			q.scope.Skill = "capital_flow"
		case hasAny(text, "个股", "财报", "估值", "分红", "公告", "基本面", "财务专家"):
			q.scope.Skill = "stock_analysis"
		default:
			q.scope.Skill = "full"
		}
	case otherMetric:
		q.missing = "该指标不在短研究覆盖内。请改问成交量、成交额或融资余额，或从工作台发起完整研报。"
	case margin && trade:
		q.missing = "请先选择一个主题，日成交或融资余额可分别发起研究。"
	case margin:
		q.scope.Topic, q.scope.Skill = harness.FinancingBalanceTopic, "capital_flow"
	case trade:
		q.scope.Topic, q.scope.Skill = harness.DailyTradingTopic, "stock_analysis"
	}
	return q
}

var windowAliases = []struct {
	words string
	days  int
}{
	{"今天|最新一日|最近一日", 1},
	{"最近一周|近一周|过去一周", 5},
	{"最近一个月|近一个月|过去一个月", 20},
	{"最近三个月|近三个月|过去三个月|近一季度", 60},
	{"最近半年|近半年|过去半年", 120},
}

func readWindow(text string) (remainder string, days int, specified bool, problem string) {
	matches := windowPattern.FindAllStringSubmatch(text, -1)
	if len(matches) > 1 {
		return "", 0, false, "请指定一个交易日窗口。"
	}
	remainder = windowPattern.ReplaceAllString(text, "")
	if len(matches) == 1 {
		token := matches[0][1]
		days, _ = strconv.Atoi(token)
		if days == 0 {
			days = map[string]int{"一": 1, "五": 5, "十": 10, "二十": 20, "六十": 60, "一百二十": 120, "二百五十": 250}[token]
		}
		if days < 1 || days > 250 {
			return "", 0, false, "请使用支持的交易日窗口，例如近 20 个交易日。"
		}
		specified = true
	}
	for _, alias := range windowAliases {
		for _, term := range strings.Split(alias.words, "|") {
			if !strings.Contains(remainder, term) {
				continue
			}
			if specified {
				return "", 0, false, "请指定一个交易日窗口。"
			}
			remainder = strings.Replace(remainder, term, "", 1)
			days, specified = alias.days, true
		}
	}
	if calendarWindow.MatchString(remainder) {
		return "", 0, false, "请给出一个交易日窗口，例如近 20 个交易日。"
	}
	return remainder, days, specified, ""
}
