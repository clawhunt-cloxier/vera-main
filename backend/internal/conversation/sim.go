package conversation

import (
	"encoding/json"
	"slices"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

// simulateAgent plays scripted examples through the tool protocol. The
// simulation adapter is only available to synthetic deployments.
func simulateAgent(text string, state agentContext, history bool, tools []model.ToolSpec) (any, error) {
	const manufacture = "演示制造"
	education := func(topic, aspect string) modelAnswer {
		return modelAnswer{Kind: "education", Concepts: []educationSelection{{Topic: topic, Aspect: aspect}}}
	}
	research := func(tool string, request researchRequest) (any, error) {
		if !slices.ContainsFunc(tools, func(spec model.ToolSpec) bool { return spec.Name == tool }) {
			return modelAnswer{Kind: "unavailable", Concepts: []educationSelection{}}, nil
		}
		if request.SubjectMentions == nil {
			request.SubjectMentions = []string{}
		}
		if request.SubjectSource == "" {
			request.SubjectSource = "message"
		}
		if request.Advice == "" {
			request.Advice = "none"
		}
		if tool == deepTool {
			return simulatedCall(tool, map[string]any{
				"subject_mentions": request.SubjectMentions, "subject_source": request.SubjectSource, "advice": request.Advice,
				"coverage": "subject",
			})
		}
		return simulatedCall(tool, map[string]any{
			"subject_mentions": request.SubjectMentions, "subject_source": request.SubjectSource, "advice": request.Advice,
			"trading_days": request.TradingDays,
		})
	}
	switch text {
	case "你好", "您好", "嗨", "hello":
		return modelAnswer{Kind: "greeting", Concepts: []educationSelection{}}, nil
	case "你是谁", "你能做哪些研究报告":
		return modelAnswer{Kind: "capabilities", Concepts: []educationSelection{}}, nil
	case "谢谢":
		return modelAnswer{Kind: "thanks", Concepts: []educationSelection{}}, nil
	case "什么是市盈率", "解释一下市盈率":
		return education("pe", "overview"), nil
	case "什么是市净率":
		return education("pb", "overview"), nil
	case "解释一下换手率的含义":
		return education("turnover", "definition"), nil
	case "那它有什么局限":
		if history {
			return education("pe", "limits"), nil
		}
	case "演示制造近 20 个交易日的融资余额怎么变", "演示制造近 20 个交易日融资余额":
		return research(financingTool, researchRequest{SubjectMentions: []string{manufacture}, TradingDays: 20})
	case "演示制造近 5 个交易日融资余额":
		return research(financingTool, researchRequest{SubjectMentions: []string{manufacture}, TradingDays: 5})
	case "演示制造的融资余额", "演示制造的融资余额走势", "演示制造的融资余额怎么变":
		return research(financingTool, researchRequest{SubjectMentions: []string{manufacture}})
	case "融资余额怎么变":
		return research(financingTool, researchRequest{})
	case manufacture:
		if state.Draft.Scope.Skill != "" {
			return research(refineTool, researchRequest{SubjectMentions: []string{manufacture}})
		}
	case "这个数怎么算的":
		return simulatedCall(referenceTool, referenceRequest{Kind: "calculation", Advice: "none"})
	case "改成 60 个交易日":
		return research(refineTool, researchRequest{SubjectSource: "context", TradingDays: 60})
	case "这只股票的日成交呢":
		return research(dailyTradingTool, researchRequest{SubjectSource: "context"})
	case "演示制造的日成交", "分析演示制造", "演示制造近 20 个交易日的成交量", "看看演示制造近 20 个交易日的日成交":
		return research(dailyTradingTool, researchRequest{SubjectMentions: []string{manufacture}})
	case "演示制造和演示设备甲的成交额":
		return research(dailyTradingTool, researchRequest{SubjectMentions: []string{manufacture, "演示设备甲"}})
	case "600000.SH 的日成交":
		return research(dailyTradingTool, researchRequest{SubjectMentions: []string{"600000.SH"}})
	case "为演示制造生成完整个股研报", "演示制造的完整个股分析":
		return research(stockTool, researchRequest{SubjectMentions: []string{manufacture}})
	case "看看演示制造的资金动向":
		return research(flowTool, researchRequest{SubjectMentions: []string{manufacture}})
	case "深度研究演示制造":
		return research(deepTool, researchRequest{SubjectMentions: []string{manufacture}})
	case "现在该不该买演示制造", "给我一只可以上车的票":
		return modelAnswer{Kind: "decline_advice", Concepts: []educationSelection{}}, nil
	case "查演示制造近 20 个交易日的成交额并给目标价":
		return research(dailyTradingTool, researchRequest{SubjectMentions: []string{manufacture}, TradingDays: 20, Advice: "declined"})
	}
	return modelAnswer{Kind: "out_of_scope", Concepts: []educationSelection{}}, nil
}

func simulatedCall(name string, args any) (model.ToolCall, error) {
	b, err := json.Marshal(args)
	return model.ToolCall{ID: "synthetic-call", Type: "function", Function: model.ToolArguments{Name: name, Arguments: string(b)}}, err
}
