package facts

import (
	"testing"

	"github.com/shopspring/decimal"
)

func want(t *testing.T, name string, c Calc, result string) {
	t.Helper()
	if result == "NA" {
		if c.OK() || c.Applicability != NotApplicable || c.Reason == "" || c.Result != nil {
			t.Fatalf("%s: want NOT_APPLICABLE with reason, got %+v", name, c)
		}
		return
	}
	if !c.OK() || c.Result.String() != result {
		t.Fatalf("%s: want %s, got %+v", name, result, c)
	}
	if c.FormulaVer != FormulaVersion || c.FormulaID == "" {
		t.Fatalf("%s: formula id/version missing: %+v", name, c)
	}
}

func TestFormulaApplicability(t *testing.T) {
	// F01 / F05 / F18: plain differences, defined for any sign.
	want(t, "F01 negative", AmountChange(n("-5"), n("3")), "-8")
	want(t, "F05 zero", CashProfitGap(n("0"), n("0")), "0")
	want(t, "F18 down", BalanceChange(n("10.5"), n("7.25")), "-3.25")
	// F02 positive base only, and no sign change.
	want(t, "F02 zero base", YoY(n("10"), n("0")), "NA")
	want(t, "F02 negative base", YoY(n("10"), n("-1")), "NA")
	want(t, "F02 to zero", YoY(n("0"), n("10")), "-1")
	// F03 gross margin: revenue must be positive; negative margin is a valid result.
	want(t, "F03 zero revenue", GrossMargin(n("0"), n("1")), "NA")
	want(t, "F03 loss", GrossMargin(n("100"), n("125")), "-0.25")
	// Ratio family (F06, F07, F08, F11, F19, F20, F22): positive denominator.
	want(t, "ratio zero den", Ratio("F07.debt_ratio", n("1"), n("0"), "总资产不为正"), "NA")
	want(t, "ratio negative den", Ratio("F11.goodwill_to_equity", n("1"), n("-2"), "归母净资产不为正"), "NA")
	want(t, "ratio negative num", Ratio("F06.cash_to_profit", n("-3"), n("2"), "净利润不为正"), "-1.5")
	// F04 pp change needs both ratios.
	want(t, "F04", PPChange(Ratio("x", n("3"), n("10"), "-"), Ratio("x", n("1"), n("4"), "-")), "0.05")
	want(t, "F04 one NA", PPChange(Ratio("x", n("3"), n("0"), "-"), Ratio("x", n("1"), n("4"), "-")), "NA")
	// F09 average equity; F10 positive profit.
	want(t, "F09", ROE(n("15"), n("100"), n("200")), "0.1")
	want(t, "F09 zero avg", ROE(n("15"), n("-100"), n("100")), "NA")
	want(t, "F10 loss", NonrecurringShare(n("-1"), n("-2")), "NA")
	want(t, "F10", NonrecurringShare(n("10"), n("12")), "-0.2")
	// F12 / F14 coverage ≥ 80% (suspension = missing points); F12 needs a positive start.
	want(t, "F12 suspended 1/5", PeriodReturn(pts("10", "", "11", "12", "12.5"), 5, "CNY/share"), "0.25")
	want(t, "F12 suspended 2/5", PeriodReturn(pts("10", "", "", "12", "12.5"), 5, "CNY/share"), "NA")
	want(t, "F12 zero start", PeriodReturn(pts("0", "1", "2", "3"), 4, "CNY/share"), "NA")
	want(t, "F12 one point", PeriodReturn(pts("10"), 1, "CNY/share"), "NA")
	want(t, "F12 no expected days", PeriodReturn(pts("10", "11"), 0, "CNY/share"), "NA")
	dd, _, _ := MaxDrawdown(pts("10", "11", "12"), 3)
	want(t, "F14 monotonic", dd, "0")
	// F13 needs both returns.
	want(t, "F13", Excess(PeriodReturn(pts("10", "12"), 2, "CNY/share"), PeriodReturn(pts("10", "11"), 2, "index_point")), "0.1")
	want(t, "F13 bench NA", Excess(PeriodReturn(pts("10", "12"), 2, "CNY/share"), PeriodReturn(pts("10", ""), 2, "index_point")), "NA")
	// F15 excludes non-positive history; window ≥ 60%.
	want(t, "F15 zero current", Percentile(Point{Value: decimal.Zero}, pts("1", "2"), 2), "NA")
	want(t, "F15 sparse", Percentile(Point{Value: decimal.RequireFromString("5")}, pts("1", "", "", "-3", "6"), 5), "NA")
	want(t, "F15 lowest", Percentile(Point{Value: decimal.RequireFromString("1")}, pts("1", "2", "3"), 3), "0")
	// F16 needs ≥ 5 positive peers; even count takes the mean of the middle two.
	var peers []PeerValue
	for _, v := range []string{"1", "2", "3", "4"} {
		peers = append(peers, PeerValue{Input: n(v)})
	}
	m, _, _ := PeerMedianRank(n("2"), peers)
	want(t, "F16 four peers", m, "NA")
	m, rank, total := PeerMedianRank(n("9"), append(peers, PeerValue{Input: n("5")}, PeerValue{Input: n("6")}))
	want(t, "F16 even", m, "3.5")
	if rank != 1 || total != 7 {
		t.Fatalf("F16 rank %d/%d", rank, total)
	}
	// F17 / F21 missing ≤ 10%.
	want(t, "F21 one of ten missing", SumWindow("F21.sum_over_window", pts("1", "1", "1", "1", "", "1", "1", "1", "1", "1"), 10, "CNY", false), "9")
	want(t, "F21 two of ten missing", SumWindow("F21.sum_over_window", pts("1", "1", "1", "", "", "1", "1", "1", "1", "1"), 10, "CNY", false), "NA")
	want(t, "F17 avg", SumWindow("F17.avg_daily_amount", pts("1", "2", "4"), 3, "CNY", true), "2.3333333333333333")
	want(t, "F17 nothing", SumWindow("F17.avg_daily_amount", nil, 0, "CNY", true), "NA")
	// F23 positive close.
	want(t, "F23 discount", BlockPremium(n("9"), n("10")), "-0.1")
	want(t, "F23 zero close", BlockPremium(n("9"), n("0")), "NA")
	// F24 flat last day ends the run; suspended days are skipped.
	want(t, "F24 flat", ConsecutiveDays(pts("1", "2", "2")), "0")
	want(t, "F24 suspended", ConsecutiveDays(pts("1", "2", "", "3")), "2")
	want(t, "F24 one day", ConsecutiveDays(pts("1", "")), "NA")
	// F25 top n with ties broken by key, independent of input order.
	items := []Ranked{{"600002", n("5")}, {"600001", n("5")}, {"600003", n("9")}, {"600004", n("1")}}
	c, top := TopN(items, 2, "CNY")
	want(t, "F25", c, "14")
	if top[0].Key != "600003" || top[1].Key != "600001" || items[0].Key != "600002" {
		t.Fatalf("F25 order %+v (input mutated: %v)", top, items[0].Key != "600002")
	}
	c, _ = TopN(items, 5, "CNY")
	want(t, "F25 too few", c, "NA")
	// F26 / F27 sums; F28 positive base over the same members.
	want(t, "F27", Sum("F27.event_sum", "CNY", []Input{n("1.1"), n("-0.1")}), "1")
	want(t, "F28", AggregateYoY([]Input{n("110"), n("22")}, []Input{n("100"), n("20")}), "0.1")
	want(t, "F28 zero base", AggregateYoY([]Input{n("1")}, []Input{n("0")}), "NA")
	want(t, "median too few", Median("F16.median", "ratio", []Input{n("1"), n("-1"), n("2")}, 3), "NA")
}

// Inputs keep full precision; rounding happens only in Display, half away from zero.
func TestDecimalPrecisionAndRounding(t *testing.T) {
	c := YoY(n("100"), n("3"))
	if c.Result.String() != "32.3333333333333333" {
		t.Fatalf("division precision: %s", c.Result)
	}
	// 0.1 + 0.2 is exact in decimal.
	if s := Sum("F27.event_sum", "CNY", []Input{n("0.1"), n("0.2")}); s.Result.String() != "0.3" {
		t.Fatalf("decimal sum %s", s.Result)
	}
	for _, tc := range []struct{ v, unit, d string }{
		{"0.12345", "ratio", "12.35"}, {"-0.12345", "ratio", "-12.35"}, {"0.00004", "ratio", "0.00"},
		{"99999999.995", "CNY", "10000.00"}, {"100000000", "CNY", "1.00"}, {"9999", "CNY", "9999.00"},
		{"2.5", "count", "3"}, {"7", "count:家", "7"},
	} {
		if d, _ := Display(decimal.RequireFromString(tc.v), tc.unit); d != tc.d {
			t.Errorf("Display(%s,%s)=%s want %s", tc.v, tc.unit, d, tc.d)
		}
	}
}
