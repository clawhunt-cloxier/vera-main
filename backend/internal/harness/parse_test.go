package harness

import (
	"context"
	"testing"
)

func TestParsePlan(t *testing.T) {
	e, pool := testEngine(t)
	ws := workspace(t, pool)
	ctx := context.Background()
	for _, tc := range []struct{ text, kind, skill, subject string }{
		{"分析演示制造", "plan", "stock_analysis", "DM0001.DEMO"},
		{"看看演示通用设备近一个月的资金面", "plan", "capital_flow", "demo-ind-machinery"},
		{"深度研究 DM0001", "plan", "deep_research", "DM0001.DEMO"},
		{"全市场资金动向", "plan", "capital_flow", "market"},
		{"演示制造能买吗", "unsupported", "", ""},
		{"演示制造明天会涨吗", "unsupported", "", ""},
		{"比较演示制造和演示亏损", "choose", "", ""},
		{"帮我写首诗", "unsupported", "", ""},
	} {
		r, err := e.ParsePlan(ctx, ws, tc.text)
		if err != nil {
			t.Fatalf("%s: %v", tc.text, err)
		}
		if r.Kind != tc.kind {
			t.Fatalf("%s: kind %s want %s (%+v)", tc.text, r.Kind, tc.kind, r)
		}
		if tc.kind == "plan" && (r.Plan.SkillID != tc.skill || r.Plan.Subject.Code != tc.subject) {
			t.Fatalf("%s: got %s %s", tc.text, r.Plan.SkillID, r.Plan.Subject.Code)
		}
	}
}
