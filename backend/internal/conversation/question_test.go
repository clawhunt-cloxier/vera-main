package conversation

import (
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
)

func TestQuestionRouting(t *testing.T) {
	for _, tc := range []struct {
		text, topic, skill, reference string
		window                        int
		inherit, blocked              bool
	}{
		{text: "演示制造近 20 个交易日的融资余额怎么变", topic: harness.FinancingBalanceTopic, skill: "capital_flow", window: 20},
		{text: "600519.SH最近一个月成交量", topic: harness.DailyTradingTopic, skill: "stock_analysis", window: 20},
		{text: "这个数怎么算的", reference: "calculation"},
		{text: "改成 60 个交易日", window: 60, inherit: true},
		{text: "再看融资余额", topic: harness.FinancingBalanceTopic, skill: "capital_flow", inherit: true},
		{text: "陌生证券近20个交易日融资余额", topic: harness.FinancingBalanceTopic, skill: "capital_flow", window: 20},
		{text: "演示制造近 20 个交易日龙虎榜买入额", skill: "capital_flow", window: 20},
		{text: "演示制造财务专家完整分析", skill: "stock_analysis"},
		{text: "生成完整研报", skill: "full", inherit: true},
		{text: "持有 0700.hk 吗", blocked: true},
		{text: "过去 10001 个交易日融资余额", blocked: true},
		{text: "过去零个交易日的融资余额", blocked: true},
		{text: "近60天成交额", blocked: true},
		{text: "最近两周融资余额", blocked: true},
		{text: "2019-04-01 至 2025-01-01 的20个交易日成交额", blocked: true},
		{text: "过去一周和近20个交易日融资余额", blocked: true},
		{text: "给出目标价并查融资余额", blocked: true},
		{text: "演示制造两融余额", blocked: true},
		{text: "演示制造融资余额和成交量", blocked: true},
		{text: "请做因子检验", blocked: true},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got := parseQuestion(tc.text)
			if (got.missing != "") != tc.blocked {
				t.Fatalf("unexpected clarification: %+v", got)
			}
			if !tc.blocked && (got.scope.Topic != tc.topic || got.scope.Skill != tc.skill ||
				got.reference != tc.reference || got.scope.Window != tc.window || got.inherit != tc.inherit) {
				t.Fatalf("unexpected intent: %+v", got)
			}
		})
	}
}
