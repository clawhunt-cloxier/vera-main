// Package facts holds deterministic formulas and the FactSheet: the only information a model sees (HARNESS §5).
package facts

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Applicability values mirror the Calculation schema.
const (
	Applicable    = "APPLICABLE"
	NotApplicable = "NOT_APPLICABLE"
	MissingInput  = "MISSING_INPUT"
)

// FormulaVersion is bumped whenever any formula's semantics change; it is frozen into reports.
const FormulaVersion = "1.1.0"

// Input is one evidence-bound operand.
type Input struct {
	EvidenceID uuid.UUID       `json:"evidence_id"`
	Value      decimal.Decimal `json:"value"`
	Unit       string          `json:"unit"`
}

// Calc is a calculation record (contracts Calculation). Inputs keep full precision; no early rounding.
type Calc struct {
	ID            uuid.UUID        `json:"id"`
	FormulaID     string           `json:"formula_id"`
	FormulaVer    string           `json:"formula_version"`
	Inputs        []Input          `json:"inputs"`
	Result        *decimal.Decimal `json:"result"`
	Unit          string           `json:"unit"`
	Applicability string           `json:"applicability"`
	Reason        string           `json:"reason"`
}

func (c Calc) OK() bool { return c.Applicability == Applicable && c.Result != nil }

func newCalc(id, unit string, in []Input) Calc {
	return Calc{FormulaID: id, FormulaVer: FormulaVersion, Inputs: in, Unit: unit}
}

func (c Calc) ok(v decimal.Decimal) Calc { c.Result, c.Applicability = &v, Applicable; return c }
func (c Calc) na(reason string) Calc     { c.Applicability, c.Reason = NotApplicable, reason; return c }

var one = decimal.NewFromInt(1)

// F01 amount_change = cur - prev
func AmountChange(cur, prev Input) Calc {
	return newCalc("F01.amount_change", cur.Unit, []Input{cur, prev}).ok(cur.Value.Sub(prev.Value))
}

// F02 yoy_growth = cur/prev - 1, only on a positive base. Sign changes are described with the raw values instead.
func YoY(cur, prev Input) Calc {
	c := newCalc("F02.yoy_positive_base", "ratio", []Input{cur, prev})
	if !prev.Value.IsPositive() {
		return c.na("上年同期值不为正，同比增长率不适用")
	}
	if cur.Value.IsNegative() {
		return c.na("本期由正转负，同比增长率不适用，以两期原值描述")
	}
	return c.ok(cur.Value.Div(prev.Value).Sub(one))
}

// Ratio implements F03 gross margin, F06 cash/profit, F07 debt ratio, F09–F11, F20, F22 with a positive denominator.
func Ratio(id string, num, den Input, naReason string) Calc {
	c := newCalc(id, "ratio", []Input{num, den})
	if !den.Value.IsPositive() {
		return c.na(naReason)
	}
	return c.ok(num.Value.Div(den.Value))
}

// F03 gross_margin = (revenue - cost) / revenue
func GrossMargin(rev, cost Input) Calc {
	c := newCalc("F03.gross_margin", "ratio", []Input{rev, cost})
	if !rev.Value.IsPositive() {
		return c.na("营业收入不为正")
	}
	return c.ok(rev.Value.Sub(cost.Value).Div(rev.Value))
}

// F04 pp_change: difference between two ratios, reported in percentage points.
func PPChange(cur, prev Calc) Calc {
	c := newCalc("F04.pp_change", "pp", nil)
	if !cur.OK() || !prev.OK() {
		return c.na("比较的比率之一不适用")
	}
	c.Inputs = append(slices.Clone(cur.Inputs), prev.Inputs...)
	return c.ok(cur.Result.Sub(*prev.Result))
}

// F05 cash_profit_gap = operating cash flow - net profit
func CashProfitGap(ocf, np Input) Calc {
	return newCalc("F05.cash_profit_gap", "CNY", []Input{ocf, np}).ok(ocf.Value.Sub(np.Value))
}

// F10 nonrecurring_share = (np - deducted) / np, np > 0
func NonrecurringShare(np, deducted Input) Calc {
	c := newCalc("F10.nonrecurring_share", "ratio", []Input{np, deducted})
	if !np.Value.IsPositive() {
		return c.na("归母净利润不为正")
	}
	return c.ok(np.Value.Sub(deducted.Value).Div(np.Value))
}

// F09 roe_simple = np / avg(equity_begin, equity_end)
func ROE(np, eqBegin, eqEnd Input) Calc {
	c := newCalc("F09.roe_simple", "ratio", []Input{np, eqBegin, eqEnd})
	avg := eqBegin.Value.Add(eqEnd.Value).Div(decimal.NewFromInt(2))
	if !avg.IsPositive() {
		return c.na("平均归母净资产不为正")
	}
	return c.ok(np.Value.Div(avg))
}

// Point is one observation of a daily series.
type Point struct {
	Date       string
	EvidenceID uuid.UUID
	Value      decimal.Decimal
	Missing    bool // suspended / no data
}

func coverage(ps []Point, expected int) (valid []Point, ok bool) {
	for _, p := range ps {
		if !p.Missing {
			valid = append(valid, p)
		}
	}
	return valid, expected > 0 && len(valid)*10 >= expected*8 && len(valid) >= 2
}

func edges(ps []Point, unit string) []Input {
	return []Input{{ps[0].EvidenceID, ps[0].Value, unit}, {ps[len(ps)-1].EvidenceID, ps[len(ps)-1].Value, unit}}
}

// F12 period_return over adjusted closes; needs ≥80% coverage of the expected trading days.
func PeriodReturn(ps []Point, expected int) Calc {
	valid, ok := coverage(ps, expected)
	c := newCalc("F12.period_return", "ratio", nil)
	if !ok {
		return c.na(fmt.Sprintf("区间内有效交易日 %d/%d，不足 80%%", len(valid), expected))
	}
	c.Inputs = edges(valid, "CNY")
	if !valid[0].Value.IsPositive() {
		return c.na("期初价格不为正")
	}
	return c.ok(valid[len(valid)-1].Value.Div(valid[0].Value).Sub(one))
}

// F13 excess_return = stock - benchmark
func Excess(stock, bench Calc) Calc {
	c := newCalc("F13.excess_return", "pp", nil)
	if !stock.OK() || !bench.OK() {
		return c.na("个股或基准区间收益不适用")
	}
	c.Inputs = append(slices.Clone(stock.Inputs), bench.Inputs...)
	return c.ok(stock.Result.Sub(*bench.Result))
}

// F14 max_drawdown (returned as a non-positive ratio) and the trough date in Reason.
func MaxDrawdown(ps []Point, expected int) (Calc, string, string) {
	valid, ok := coverage(ps, expected)
	c := newCalc("F14.max_drawdown", "ratio", nil)
	if !ok {
		return c.na("区间有效交易日不足 80%"), "", ""
	}
	peak, worst := valid[0], decimal.Zero
	var wp, wt Point
	for _, p := range valid {
		if p.Value.GreaterThan(peak.Value) {
			peak = p
		}
		if peak.Value.IsPositive() {
			dd := p.Value.Div(peak.Value).Sub(one)
			if dd.LessThan(worst) {
				worst, wp, wt = dd, peak, p
			}
		}
	}
	if worst.IsZero() {
		c.Inputs = edges(valid, "CNY")
		return c.ok(worst), "", ""
	}
	c.Inputs = []Input{{wp.EvidenceID, wp.Value, "CNY"}, {wt.EvidenceID, wt.Value, "CNY"}}
	return c.ok(worst), wp.Date, wt.Date
}

// F15 percentile_rank: share of valid history strictly below the current value. Negative values are excluded.
func Percentile(cur Point, hist []Point, expected int) Calc {
	c := newCalc("F15.percentile_rank", "ratio", []Input{{cur.EvidenceID, cur.Value, "times"}})
	if !cur.Value.IsPositive() {
		return c.na("当前值为负或为零，不计算历史分位")
	}
	var valid []decimal.Decimal
	for _, p := range hist {
		if !p.Missing && p.Value.IsPositive() {
			valid = append(valid, p.Value)
		}
	}
	if expected == 0 || len(valid)*10 < expected*6 {
		return c.na(fmt.Sprintf("历史窗口有效样本 %d/%d，不足 60%%", len(valid), expected))
	}
	below := 0
	for _, v := range valid {
		if v.LessThan(cur.Value) {
			below++
		}
	}
	return c.ok(decimal.NewFromInt(int64(below)).Div(decimal.NewFromInt(int64(len(valid)))))
}

// PeerValue is one comparable company's metric.
type PeerValue struct {
	Code  string
	Input Input
}

// F16 peer_median_rank: median of peers (positive values) and the subject's rank (1 = highest) among subject+peers.
func PeerMedianRank(subject Input, peers []PeerValue) (median Calc, rank int, total int) {
	c := newCalc("F16.peer_median", subject.Unit, nil)
	var vals []decimal.Decimal
	for _, p := range peers {
		if p.Input.Value.IsPositive() {
			vals = append(vals, p.Input.Value)
			c.Inputs = append(c.Inputs, p.Input)
		}
	}
	if len(vals) < 5 {
		return c.na(fmt.Sprintf("有效可比公司 %d 家，不足 5 家", len(vals))), 0, 0
	}
	slices.SortFunc(vals, func(a, b decimal.Decimal) int { return a.Cmp(b) })
	n := len(vals)
	m := vals[n/2]
	if n%2 == 0 {
		m = vals[n/2-1].Add(vals[n/2]).Div(decimal.NewFromInt(2))
	}
	rank = 1
	for _, v := range vals {
		if v.GreaterThan(subject.Value) {
			rank++
		}
	}
	return c.ok(m), rank, n + 1
}

// F17 avg_daily_amount / F21 sum_over_window
func SumWindow(id string, ps []Point, expected int, unit string, avg bool) Calc {
	c := newCalc(id, unit, nil)
	valid := 0
	sum := decimal.Zero
	for _, p := range ps {
		if !p.Missing {
			valid++
			sum = sum.Add(p.Value)
			c.Inputs = append(c.Inputs, Input{p.EvidenceID, p.Value, unit})
		}
	}
	if expected == 0 || (expected-valid)*10 > expected {
		return c.na(fmt.Sprintf("缺失交易日 %d/%d，超过 10%%", expected-valid, expected))
	}
	if avg {
		return c.ok(sum.Div(decimal.NewFromInt(int64(valid))))
	}
	return c.ok(sum)
}

// F18 balance_change = end - begin (both ends must have values)
func BalanceChange(begin, end Input) Calc {
	return newCalc("F18.balance_change", end.Unit, []Input{begin, end}).ok(end.Value.Sub(begin.Value))
}

// F23 block_premium = price / close - 1
func BlockPremium(price, closePx Input) Calc {
	c := newCalc("F23.block_premium", "ratio", []Input{price, closePx})
	if !closePx.Value.IsPositive() {
		return c.na("当日收盘价不为正")
	}
	return c.ok(price.Value.Div(closePx.Value).Sub(one))
}

// F24 consecutive_days: trailing run of same-sign day-over-day changes. Returns signed count (+up / -down).
func ConsecutiveDays(ps []Point) Calc {
	c := newCalc("F24.consecutive_days", "count", nil)
	var valid []Point
	for _, p := range ps {
		if !p.Missing {
			valid = append(valid, p)
		}
	}
	if len(valid) < 2 {
		return c.na("有效数据不足两天")
	}
	dir, n := 0, 0
	for i := len(valid) - 1; i > 0; i-- {
		d := valid[i].Value.Cmp(valid[i-1].Value)
		if d == 0 || (dir != 0 && d != dir) {
			break
		}
		dir = d
		n++
		c.Inputs = append(c.Inputs, Input{valid[i].EvidenceID, valid[i].Value, ""})
	}
	return c.ok(decimal.NewFromInt(int64(dir * n)))
}

// Ranked is one member of a set ranked by a metric. Key (e.g. the security code) breaks ties so the order does
// not depend on input order.
type Ranked struct {
	Key   string
	Input Input
}

// RankDesc sorts by value descending, then key ascending.
func RankDesc(items []Ranked) {
	slices.SortFunc(items, func(a, b Ranked) int {
		if c := b.Input.Value.Cmp(a.Input.Value); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
}

// F25 top_n: the first n members by value (descending, ties by key). Applicable only with at least n members.
// The result is the sum of the top n values; the ranked members are returned for tables and F22 shares.
func TopN(items []Ranked, n int, unit string) (Calc, []Ranked) {
	c := newCalc("F25.top_n", unit, nil)
	if n <= 0 || len(items) < n {
		return c.na(fmt.Sprintf("有效成分 %d 个，不足 %d 个", len(items), n)), nil
	}
	ranked := slices.Clone(items)
	RankDesc(ranked)
	top := ranked[:n]
	sum := decimal.Zero
	for _, r := range top {
		sum = sum.Add(r.Input.Value)
		c.Inputs = append(c.Inputs, r.Input)
	}
	return c.ok(sum), top
}

// StableID derives calculation IDs from the task and a key so recomputation is idempotent.
func StableID(task uuid.UUID, epoch int64, key string) uuid.UUID {
	return uuid.NewSHA1(task, fmt.Appendf(nil, "%d|%s", epoch, key))
}

// Derived records an aggregate calculation (industry sums, medians, shares) over many evidence inputs.
// fn returns the result or a not-applicable reason. Used for F22 share_of_total, F25 top_n, F27 event_sum,
// F28 aggregate_yoy, F19 net_margin and F26 interest_bearing_debt.
func Derived(id, unit string, in []Input, fn func() (*decimal.Decimal, string)) Calc {
	c := newCalc(id, unit, in)
	v, reason := fn()
	if v == nil {
		return c.na(reason)
	}
	return c.ok(*v)
}

// Sum adds inputs (F27 event_sum / F26 interest_bearing_debt).
func Sum(id, unit string, in []Input) Calc {
	return Derived(id, unit, in, func() (*decimal.Decimal, string) {
		t := decimal.Zero
		for _, i := range in {
			t = t.Add(i.Value)
		}
		return &t, ""
	})
}

// AggregateYoY = sum(cur)/sum(prev) - 1 over the same member set, positive base only (F28).
func AggregateYoY(cur, prev []Input) Calc {
	all := append(slices.Clone(cur), prev...)
	return Derived("F28.aggregate_yoy", "ratio", all, func() (*decimal.Decimal, string) {
		a, b := decimal.Zero, decimal.Zero
		for _, i := range cur {
			a = a.Add(i.Value)
		}
		for _, i := range prev {
			b = b.Add(i.Value)
		}
		if !b.IsPositive() {
			return nil, "上年同期合计不为正"
		}
		r := a.Div(b).Sub(one)
		return &r, ""
	})
}

// Median of positive values (F16 variant for industry medians).
func Median(id, unit string, in []Input, minN int) Calc {
	return Derived(id, unit, in, func() (*decimal.Decimal, string) {
		var vals []decimal.Decimal
		for _, i := range in {
			if i.Value.IsPositive() {
				vals = append(vals, i.Value)
			}
		}
		if len(vals) < minN {
			return nil, fmt.Sprintf("有效样本 %d 个，不足 %d 个", len(vals), minN)
		}
		slices.SortFunc(vals, func(a, b decimal.Decimal) int { return a.Cmp(b) })
		n := len(vals)
		m := vals[n/2]
		if n%2 == 0 {
			m = vals[n/2-1].Add(vals[n/2]).Div(decimal.NewFromInt(2))
		}
		return &m, ""
	})
}
