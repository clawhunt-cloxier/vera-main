package conversation

import "testing"

func TestTradingDayWindowExpressions(t *testing.T) {
	for _, tc := range []struct {
		name, text           string
		days                 int
		valid, refersToPrior bool
	}{
		{"same window", "标的改为演示银行，融资余额按同一个交易日窗口看。", 0, true, true},
		{"same range", "改看日成交，沿用同一交易日范围。", 0, true, true},
		{"same interval with digits", "按同 1 个交易日区间。", 0, true, true},
		{"previous window", "按原来的交易日窗口。", 0, true, true},
		{"ordinary one-day window", "查演示银行近一个交易日窗口的日成交。", 1, true, false},
		{"explicit digits", "只看近 １ 个交易日。", 1, true, false},
		{"explicit value with reference", "此前同一个交易日窗口，本轮改用五个交易日。", 5, true, true},
		{"literal range is unsupported", "改成 3 至 5 个交易日。", 0, false, false},
		{"plain calendar period", "按 2025 年 2 月。", 0, false, false},
		{"reference and calendar period", "按同一个交易日窗口，并且限定上个月。", 0, false, false},
		{"explicit value and calendar period", "只能看上周的 5 个交易日。", 0, false, false},
		{"reference and conflicting counts", "同一个交易日范围，改成5个交易日或10个交易日。", 0, false, false},
		{"nonpositive count", "改为 0 个交易日。", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := parseTradingDayWindow(tc.text)
			if got.days != tc.days || valid != tc.valid || got.referencesPrior != tc.refersToPrior {
				t.Errorf("parseTradingDayWindow(%q) = %+v, %t; want days=%d, refs=%t, valid=%t",
					tc.text, got, valid, tc.days, tc.refersToPrior, tc.valid)
			}
		})
	}
}

func TestWindowOnlyTradingDaysUsesExplicitCount(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"按同一个交易日窗口。", 0},
		{"那就看 1 个交易日。", 1},
		{"同一只股票，改用最近５个交易日。", 5},
		{"换成演示银行，改用 5 个交易日。", 0},
		{"看 5 个交易日，限定上周。", 0},
	} {
		if got := windowOnlyTradingDays(tc.text); got != tc.want {
			t.Errorf("windowOnlyTradingDays(%q) = %d; want %d", tc.text, got, tc.want)
		}
	}
}

func TestTradingDayWindowReferenceSelection(t *testing.T) {
	for _, tc := range []struct {
		text       string
		keep, drop bool
		wantDays   int
	}{
		{"按同一个交易日窗口看融资余额。", true, false, 0},
		{"这个交易日窗口照旧。", true, false, 0},
		{"请继续沿用原来的交易日区间。", true, false, 0},
		{"原来的交易日窗口是多少？", false, false, 0},
		{"不要沿用同一个交易日窗口。", false, true, 0},
		{"这轮不再用原来的交易日窗口。", false, true, 0},
		{"同一个交易日窗口这轮先别用。", false, true, 0},
		{"不要继续用同一个交易日窗口，改成 5 个交易日。", false, true, 5},
		{"按原来的交易日窗口；等等，不要沿用同一个交易日范围。", true, true, 0},
	} {
		window, valid := parseTradingDayWindow(tc.text)
		if !valid || window.keepsPrior != tc.keep || window.rejectsPrior != tc.drop || window.days != tc.wantDays {
			t.Errorf("parseTradingDayWindow(%q) = %+v, %t; want keep=%t, reject=%t, days=%d",
				tc.text, window, valid, tc.keep, tc.drop, tc.wantDays)
		}
	}
}
