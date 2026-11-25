package conversation

import (
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
)

const (
	financeOnlyReply = "我是 Vera 金融投研助手。你可以问我金融概念、指标口径，或查询已接入的历史数据。"
	researchReply    = "请补充要研究的证券、行业或市场，以及想看的指标和时间范围。"
	unavailableReply = "当前工作台没有可用于回答这个问题的研究能力。你可以查询已开放的历史指标或追问已有报告的来源。"
	educationReply   = "我的金融知识库暂未收录这个概念。你可以换一个指标，或指定证券、指标和时间范围查询已经披露的数据。"
	AdviceBoundary   = "我可以展示可核验的历史数据和计算过程。个股推荐、买卖或仓位建议、评级、目标价及未来收益判断超出服务范围。"
)

type educationTopic struct {
	ID, Label, Definition, Method, Limits string
}

var educationTopics = []educationTopic{
	{"pe", "市盈率", "市盈率衡量公司市值相对于盈利的倍数。", "比较时应统一盈利口径，例如区分上一完整财年与滚动期间。", "亏损时市盈率通常不具备同样的比较意义。一次性损益和行业差异也会影响指标。"},
	{"pb", "市净率", "市净率衡量公司市值相对于归属普通股股东账面净资产的倍数。", "比较时先对齐净资产口径、报告期和对应的市场时点。", "资产质量、资本结构和盈利能力都会影响其可比性。"},
	{"ps", "市销率", "市销率衡量公司市值相对于营业收入的倍数。", "对齐收入期间、会计口径和市值日期后再比较。", "收入规模无法反映利润率、资金消耗和债务负担。"},
	{"ev_ebitda", "企业价值倍数 EV/EBITDA", "企业价值倍数可以用企业价值与息税折旧摊销前利润的比值表示。", "比较时统一企业价值的债务、现金等调整项以及利润口径和期间。", "该指标不反映资本开支和营运资金变化，可比性还受行业及租赁处理影响。"},
	{"roe", "净资产收益率 ROE", "净资产收益率反映归属股东的利润相对于股东权益的水平。", "核对净利润的归属范围、股东权益的平均口径和报告期间。", "高财务杠杆和较薄的净资产都可能放大该指标。"},
	{"gross_margin", "毛利率", "毛利率是营业收入减去相应营业成本后的毛利占收入的比重。", "比较前对齐成本归集、分部范围和报告期间。", "毛利率未扣除期间费用、利息和税费，收入结构变化也会影响它。"},
	{"net_margin", "净利率", "净利率反映一定期间内净利润相对于营业收入的比例。", "先区分归母净利润与合并净利润口径，再统一报告期间。", "一次性损益和税率变化可能改变净利率，和主营业务经营变化需要分别核对。"},
	{"cash_flow", "经营现金流", "经营活动现金流反映主营经营及相关活动带来的现金收付。", "可以将同期经营现金流、净利润和营运资金变动并列观察。", "结算季节性和预收、预付变化可能使单期现金流偏离长期状况。"},
	{"free_cash_flow", "自由现金流", "自由现金流描述经营活动产生的现金扣除维持和扩张经营所需投资后的金额。", "以经营现金流减去资本开支估算时，应注明资本开支的归集范围。", "有多种定义，跨公司比较前需要统一对利息、租赁和资本开支的处理。"},
	{"statements", "三张财务报表", "资产负债表记录期末财务状况，利润表记录期间经营成果，现金流量表记录期间现金收付。", "对照报表先确认报告期、币种和合并范围，再查看科目及附注。", "季度与累计期间、合并与母公司报表的数字不可直接混用。"},
	{"growth", "同比与环比", "同比与上年同期比，环比与相邻的上一个同长度期间比。", "确认两个期间一致且统计口径相同，再比较金额和相对变化。", "基期为零、负数或异常时，百分比可能难以解释，宜同时展示原始值。"},
	{"trading", "成交量与成交额", "成交量记录成交的证券数量，成交额记录相应交易金额。", "比较时应区分股与手、元与万元，并对齐实际交易日期。", "成交活跃反映历史交易规模，无法单独说明资金身份或未来价格方向。"},
	{"turnover", "换手率", "换手率反映一定期间成交量相对于相应流通股数的比例。", "比较时应核对流通股本范围和统计期间。", "除权、流通范围变化和停牌会影响可比性。"},
	{"margin", "融资余额", "融资余额反映截至披露时点尚未偿还的融资金额。融资余额与融券余量采用不同口径。", "观察变化时需要对照同一证券和统计范围下的期初、期末余额。", "单一余额变化无法说明参与者意图，也不能代替对成交活跃度和整体市场的观察。"},
	{"capital_flow", "资金流口径", "资金流可以指融资、披露持仓变化或厂商基于成交规则分类的动态指标，定义随数据源而异。", "先确认数据来源、统计范围、分类方法和期间，再比较同口径指标。", "厂商推算的净流入不能直接当成已识别的资金主体、真实转账或交易意图。"},
	{"adjustment", "复权", "复权按约定处理分红、送转等引起的价格序列跳变。", "分析历史区间表现时先统一复权基准和期初、期末的价格口径。", "复权价用于可比性分析，可能与当时的实际成交价格不同。"},
	{"return", "历史收益率", "历史收益率描述给定区间内资产价值相对于期初的变化比例。", "先说明是否计入分红再投资、费用和税负，以及区间起止及价格口径。", "历史表现受期间选择影响，无法说明未来收益。"},
	{"volatility", "波动率", "波动率度量一段期间内收益变化的离散程度。", "比较前对齐收益频率、样本区间和年化方式。", "波动率同时衡量向上和向下的变化，无法单独概括流动性或极端风险。"},
	{"drawdown", "回撤", "回撤描述资产价值从此前峰值回落的幅度，最大回撤是所观察期间最深的一次。", "比较前统一净值口径、时间区间和观测频率。", "历史最大回撤依赖选定区间，未覆盖期间外可能出现的损失。"},
	{"dividend", "股息率与分红", "股息率把选定期间的每股现金分红和相应价格进行比较。", "查看方案、实施公告和除权日，明确历史分红统计区间与价格日期。", "历史分红无法代表未来分配，特殊分红和税费也会影响解释。"},
	{"etf", "ETF", "交易型开放式基金的份额可以在交易所交易。很多此类基金以跟踪指数为目标。", "研究时可以核对跟踪标的、费用、跟踪误差和成交流动性。", "交易价格可能偏离基金净值，成分和集中度也随产品而异。"},
	{"bond", "债券与收益率", "债券约定利息和本金的偿付条件。到期收益率是在给定价格和假设下折算的现金流回报率。", "比较时核对当前报价、现金流日期、信用状况、期限和可赎回条款。", "信用事件、流动性及提前偿付都可能使实际结果偏离估算。"},
	{"benchmark", "基准与相对表现", "基准是评价区间表现时使用的参照，例如覆盖范围明确的指数。", "将研究对象与基准对齐到同一期间、币种和分红口径。", "基准选择会影响相对表现的解释，历史相关性也可能变化。"},
	{"valuation", "估值方法", "估值方法通常包括用可比数据测量相对价格，以及用假设下的现金流折现测量价值。", "先列明估值的输入、比较对象和时间节点，再检验假设对结果的影响。", "估值高度依赖口径、数据覆盖和假设，本身无法确定未来价格。"},
	{"leverage", "财务杠杆", "财务杠杆描述企业使用债务等固定偿付资金安排对权益结果的放大作用。", "研究时核对有息债务、现金、融资成本和偿付到期结构。", "资产负债率无法单独描述偿债压力，现金流及债务期限同样重要。"},
	{"reading_reports", "阅读财报与研究证据", "分析财报时可从收入、利润、现金流和财务结构入手。", "核对报告期、会计口径、来源原文和计算输入。结合附注识别口径变更和一次性事项。", "数据缺失、修订或可比公司口径差异都会限制结论。"},
}

type educationSelection struct {
	Topic  string `json:"topic"`
	Aspect string `json:"aspect"`
}

type modelAnswer struct {
	Kind     string               `json:"kind"`
	Concepts []educationSelection `json:"concepts"`
}

func educationLabels() string {
	labels := make([]string, 0, len(educationTopics))
	for _, topic := range educationTopics {
		labels = append(labels, topic.ID+"="+topic.Label)
	}
	return strings.Join(labels, "，")
}

func checkedAnswer(answer modelAnswer, capabilities string) (string, bool) {
	switch answer.Kind {
	case "greeting":
		return "你好，我是 Vera。" + capabilities, false
	case "thanks":
		return "不客气。你可以继续追问指标、来源或计算过程。", false
	case "capabilities":
		return capabilities, false
	case "decline_advice":
		return AdviceBoundary, false
	case "clarify":
		return researchReply, false
	case "unavailable":
		return unavailableReply, false
	case "education":
		if len(answer.Concepts) == 0 || len(answer.Concepts) > 3 {
			return educationReply, false
		}
		parts := make([]string, 0, len(answer.Concepts))
		for _, selection := range answer.Concepts {
			text := educationText(selection)
			if text == "" || check.Forbidden(text) != nil || check.Suspicious(text) {
				return educationReply, false
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n"), true
	default:
		return financeOnlyReply, false
	}
}

func educationText(selection educationSelection) string {
	for _, topic := range educationTopics {
		if topic.ID != selection.Topic {
			continue
		}
		switch selection.Aspect {
		case "definition":
			return topic.Definition
		case "method":
			return topic.Method
		case "limits":
			return topic.Limits
		case "overview":
			return topic.Definition + topic.Method + topic.Limits
		}
		break
	}
	return ""
}
