package harness

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func (b *builder) benchmark() (code, name string) {
	if b.B.Subject.Synthetic {
		return "DM300.DEMO", "演示 300 指数"
	}
	return "000300.SH", "沪深 300 指数"
}

func numericPoint(r dataservice.StoredRecord, date, field string) facts.Point {
	v, ok := dec(r, field)
	return facts.Point{Date: date, EvidenceID: r.ID, Value: v, Missing: !ok}
}

func onTradingDay(rows []dataservice.StoredRecord, date string) (dataservice.StoredRecord, bool) {
	i, ok := slices.BinarySearchFunc(rows, date, func(r dataservice.StoredRecord, target string) int {
		return strings.Compare(str(r, "trade_date"), target)
	})
	if !ok {
		return dataservice.StoredRecord{}, false
	}
	return rows[i], true
}

// adjusted close points for the window; suspended days are Missing.
func (b *builder) closes(e uuid.UUID, days []string) ([]facts.Point, map[string]dataservice.StoredRecord) {
	rs := b.B.Rows("mkt.daily_bar", e)
	b.sources(rs)
	by := map[string]dataservice.StoredRecord{}
	for _, r := range rs {
		by[str(r, "trade_date")] = r
	}
	var pts []facts.Point
	for _, d := range days {
		r, ok := by[d]
		if !ok || boolean(r, "suspended") {
			pts = append(pts, facts.Point{Date: d, Missing: true})
			continue
		}
		c, hasClose := dec(r, "close")
		adj, ok := dec(r, "adj_factor")
		if !hasClose || !c.IsPositive() || !ok || !adj.IsPositive() {
			pts = append(pts, facts.Point{Date: d, Missing: true})
			continue
		}
		pts = append(pts, facts.Point{Date: d, EvidenceID: r.ID, Value: c.Mul(adj)})
	}
	return pts, by
}

func (b *builder) indexPoints(code string, days []string) []facts.Point {
	rs := b.B.Index(code)
	b.sources(rs)
	by := map[string]dataservice.StoredRecord{}
	for _, r := range rs {
		by[str(r, "trade_date")] = r
	}
	var pts []facts.Point
	for _, d := range days {
		r, ok := by[d]
		if !ok {
			pts = append(pts, facts.Point{Date: d, Missing: true})
			continue
		}
		pts = append(pts, numericPoint(r, d, "close"))
	}
	return pts
}

// Both legs must cover the same dates. Missing observations in either series
// exclude that session from both returns and from their joint coverage.
func matchedExcess(a, b []facts.Point) facts.Calc {
	left, right := slices.Clone(a), slices.Clone(b)
	for i := range left {
		if left[i].Missing || right[i].Missing {
			left[i].Missing, right[i].Missing = true, true
		}
	}
	return facts.Excess(facts.PeriodReturn(left, len(left)), facts.PeriodReturn(right, len(right)))
}

func windowLabel(n int, days []string) string {
	if len(days) == 0 {
		return "缺少交易日历"
	}
	return fmt.Sprintf("近 %d 个交易日（%s 至 %s）", n, days[0], days[len(days)-1])
}

func industryIndex(en dataservice.EntityRow) string {
	if c, ok := en.Attrs["index_code"].(string); ok {
		return c
	}
	if ind, ok := en.Attrs["industry_code"].(string); en.Synthetic && ok {
		return "IDX-" + ind
	}
	return ""
}

// market builds mkt.* facts for a stock over a window and the price charts.
func (b *builder) market(e uuid.UUID, n int, group, section string) {
	days := b.B.Window(n)
	if len(days) < 2 {
		return
	}
	subj := b.name(e)
	wl := windowLabel(n, days)
	k := func(s string) string { return b.B.Entities[e].Code + "|mkt|" + s }
	pts, by := b.closes(e, days)
	stockRet := facts.PeriodReturn(pts, len(days))
	b.S.Computed(group, subj, "区间涨跌幅（复权）", wl, stockRet, k("ret"))
	if idx := industryIndex(b.B.Entities[e]); idx != "" {
		b.S.Computed(group, subj, "相对行业指数超额收益", wl, matchedExcess(pts, b.indexPoints(idx, days)), k("ex_ind"))
	}
	benchmark, name := b.benchmark()
	benchmarkPoints := b.indexPoints(benchmark, days)
	br := facts.PeriodReturn(benchmarkPoints, len(days))
	b.S.Computed(group, name, "基准指数区间涨跌幅", wl, br, k("bench"))
	b.S.Computed(group, subj, "相对"+name+"超额收益", wl, matchedExcess(pts, benchmarkPoints), k("ex_bench"))
	dd, peak, trough := facts.MaxDrawdown(pts, len(days))
	period := wl
	if peak != "" {
		period = fmt.Sprintf("%s，高点 %s，低点 %s", wl, peak, trough)
	}
	b.S.Computed(group, subj, "区间最大回撤", period, dd, k("dd"))
	var amt, tr []facts.Point
	suspended := 0
	for _, d := range days {
		r, ok := by[d]
		if !ok || boolean(r, "suspended") {
			if ok && boolean(r, "suspended") {
				suspended++
			}
			amt = append(amt, facts.Point{Date: d, Missing: true})
			tr = append(tr, facts.Point{Date: d, Missing: true})
			continue
		}
		amt = append(amt, numericPoint(r, d, "amount"))
		tr = append(tr, numericPoint(r, d, "turnover_rate"))
	}
	b.S.Computed(group, subj, "日均成交额", wl, facts.SumWindow("F17.avg_daily_amount", amt, len(days), "CNY", true), k("amt"))
	b.S.Computed(group, subj, "日均换手率", wl, facts.SumWindow("F17.avg_daily_turnover", tr, len(days), "ratio", true), k("tr"))
	if suspended > 0 {
		var ev []uuid.UUID
		for _, d := range days {
			if r, ok := by[d]; ok && boolean(r, "suspended") {
				ev = append(ev, r.ID)
			}
		}
		b.S.Value(group, subj, "区间内停牌或无交易的交易日数", wl, facts.KindMarket, "", "count:个交易日", decimal.NewFromInt(int64(suspended)), ev...)
		b.S.Limit(fmt.Sprintf("%s 在%s内有 %d 个交易日停牌或无交易，区间指标按适用条件处理。", subj, wl, suspended))
	}
	// latest close (unadjusted) as a market fact
	for i := len(days) - 1; i >= 0; i-- {
		if r, ok := by[days[i]]; ok && !boolean(r, "suspended") {
			c, ok := dec(r, "close")
			if !ok || !c.IsPositive() {
				continue
			}
			b.S.Value(group, subj, "最近收盘价", days[i], facts.KindMarket, "不复权", "price", c, r.ID)
			if !b.B.Subject.Synthetic {
				b.S.Limit(subj + "的最近已取得收盘价对应交易日为 " + days[i] + "。")
			}
			break
		}
	}
	b.priceCharts(e, days, pts, section)
}

func (b *builder) priceCharts(e uuid.UUID, days []string, pts []facts.Point, section string) {
	price := facts.Series{ID: "price_adj", Title: "复权收盘价", Kind: "line", X: "trade_date", Unit: "元", Section: section}
	line := facts.SeriesLine{Name: b.B.Entities[e].Name}
	for _, p := range pts {
		x := p.Date
		if p.Missing {
			line.Points = append(line.Points, [2]*string{&x, nil})
			continue
		}
		v := p.Value.StringFixed(2)
		line.Points = append(line.Points, [2]*string{&x, &v})
		price.Evidence = append(price.Evidence, p.EvidenceID)
	}
	price.Lines = []facts.SeriesLine{line}
	rel := facts.Series{ID: "relative_perf", Title: "相对表现（各序列首个有效交易日 = 100）", Kind: "line", X: "trade_date", Unit: "点", Section: section}
	add := func(name string, ps []facts.Point) {
		var base *decimal.Decimal
		l := facts.SeriesLine{Name: name}
		for _, p := range ps {
			x := p.Date
			if p.Missing || !p.Value.IsPositive() {
				l.Points = append(l.Points, [2]*string{&x, nil})
				continue
			}
			if base == nil {
				v := p.Value
				base = &v
			}
			v := p.Value.Div(*base).Mul(decimal.NewFromInt(100)).StringFixed(2)
			l.Points = append(l.Points, [2]*string{&x, &v})
			rel.Evidence = append(rel.Evidence, p.EvidenceID)
		}
		rel.Lines = append(rel.Lines, l)
	}
	add(b.B.Entities[e].Name, pts)
	if idx := industryIndex(b.B.Entities[e]); idx != "" {
		add("行业指数", b.indexPoints(idx, days))
	}
	benchmark, name := b.benchmark()
	add(name, b.indexPoints(benchmark, days))
	b.S.Charts = append(b.S.Charts, price, rel)
}

// valuation builds val.* facts: current multiples, 3y/5y percentile, peer median and rank.
func (b *builder) valuation(e uuid.UUID, peers []uuid.UUID, group string) bool {
	rs := b.B.Rows("mkt.valuation_daily", e)
	if len(rs) == 0 {
		return false
	}
	b.sources(rs)
	subj := b.name(e)
	last := rs[len(rs)-1]
	d := str(last, "trade_date")
	basis := b.basis["mkt.valuation_daily"]
	k := func(s string) string { return b.B.Entities[e].Code + "|val|" + s }
	for _, m := range []struct{ f, label string }{{"pe_ttm", "PE(TTM)"}, {"pb_mrq", "PB(MRQ)"}, {"ps_ttm", "PS(TTM)"}} {
		cur, ok := input(last, m.f, "times")
		if !ok {
			continue
		}
		b.S.Value(group, subj, m.label, d, facts.KindVendorDerived, basis, "times", cur.Value, last.ID)
		for _, yrs := range []int{3, 5} {
			hist := b.histPoints(rs, m.f, yrs)
			expected := expectedDays(b.B, yrs)
			// A newly provisioned calendar cannot establish that a short series
			// represents the requested three- or five-year history.
			c := facts.Percentile(facts.Point{EvidenceID: last.ID, Value: cur.Value}, hist, expected)
			if !b.B.Subject.Synthetic {
				start := b.B.AsOf.AddDate(-yrs, 0, 0).AddDate(0, 0, 10).Format("2006-01-02")
				if len(b.B.Days) == 0 || b.B.Days[0] > start {
					c = missingRatio("F15.percentile_rank", "交易日历未覆盖所需历史区间")
				} else if c.OK() {
					for _, obs := range hist {
						if !obs.Missing && obs.Value.IsPositive() && obs.EvidenceID != last.ID {
							c.Inputs = append(c.Inputs, facts.Input{EvidenceID: obs.EvidenceID, Value: obs.Value, Unit: "times"})
						}
					}
				}
			}
			b.S.Computed(group, subj, fmt.Sprintf("%s 近 %d 年历史分位", m.label, yrs), fmt.Sprintf("近 %d 年至 %s", yrs, d), c, k(fmt.Sprintf("pct%d|%s", yrs, m.f))).Vendor(basis)
		}
		if len(peers) > 0 {
			var pv []facts.PeerValue
			for _, p := range peers {
				prs := b.B.Rows("mkt.valuation_daily", p)
				if len(prs) == 0 {
					continue
				}
				b.sources(prs)
				r, ok := onTradingDay(prs, d)
				if !ok {
					continue
				}
				if in, ok := input(r, m.f, "times"); ok && in.Value.IsPositive() {
					pv = append(pv, facts.PeerValue{Code: b.B.Entities[p].Code, Input: in})
				}
			}
			med, rank, total := facts.PeerMedianRank(cur, pv)
			mf := b.S.Computed(group, subj, m.label+" 可比公司中位数", d+"，可比公司 "+fmt.Sprint(len(pv))+" 家", med, k("peer|"+m.f)).Vendor(basis)
			if med.OK() && cur.Value.IsPositive() {
				b.S.Note(group, subj, m.label+" 在可比公司中的排位（由高到低）", d, facts.KindComputed,
					fmt.Sprintf("第 %d 位（共 %d 家）", rank, total), append(slices.Clone(mf.Evidence), last.ID)...).Vendor(basis)
			}
		}
	}
	if mv, ok := input(last, "total_mv", "CNY"); ok {
		b.S.Value(group, subj, "总市值", d, facts.KindMarket, "", "CNY", mv.Value, last.ID)
	}
	return true
}

func (b *builder) histPoints(rs []dataservice.StoredRecord, f string, years int) []facts.Point {
	from := b.B.AsOf.AddDate(-years, 0, 0).Format("2006-01-02")
	var out []facts.Point
	for _, r := range rs {
		if str(r, "trade_date") < from {
			continue
		}
		v, ok := dec(r, f)
		out = append(out, facts.Point{Date: str(r, "trade_date"), EvidenceID: r.ID, Value: v, Missing: !ok})
	}
	return out
}

func expectedDays(b *Bundle, years int) int {
	from := b.AsOf.AddDate(-years, 0, 0).Format("2006-01-02")
	i, _ := slices.BinarySearch(b.Days, from)
	return len(b.Days) - i
}
