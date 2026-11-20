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

type memberFin struct {
	id                 uuid.UUID
	rev, prevRev       *facts.Input
	np, prevNp, cost   *facts.Input
	yoy                facts.Calc
	period, prevPeriod string
}

// memberFins returns latest-FY figures for members sharing the most common latest fiscal year.
func (b *builder) memberFins(members []uuid.UUID) ([]memberFin, string) {
	var out []memberFin
	years := map[string]int{}
	for _, m := range members {
		ix := b.fin(m)
		fys := ix.fiscalYears()
		if len(fys) == 0 {
			continue
		}
		years[fys[len(fys)-1]]++
	}
	best := ""
	for y, n := range years {
		if n > years[best] || (n == years[best] && y > best) {
			best = y
		}
	}
	if best == "" {
		return nil, ""
	}
	prev := priorYear(best)
	for _, m := range members {
		ix := b.fin(m)
		mf := memberFin{id: m, period: best, prevPeriod: prev}
		get := func(p, item string) *facts.Input {
			if in, _, ok := ix.get(p, item); ok {
				return &in
			}
			return nil
		}
		mf.rev, mf.prevRev = get(best, "IS.revenue"), get(prev, "IS.revenue")
		mf.np, mf.prevNp, mf.cost = get(best, "IS.net_profit_parent"), get(prev, "IS.net_profit_parent"), get(best, "IS.operating_cost")
		if mf.rev == nil {
			continue
		}
		if mf.prevRev != nil {
			mf.yoy = facts.YoY(*mf.rev, *mf.prevRev)
		}
		out = append(out, mf)
	}
	return out, best
}

// composition builds ind.composition facts: member count, total market value, top-10 by market value.
func (b *builder) composition(ind dataservice.EntityRow, members []uuid.UUID, group, section string) {
	subj := ind.Name + "（" + ind.Code + "）"
	day := b.B.AsOf.Format("2006-01-02")
	var ev []uuid.UUID
	for _, m := range members {
		if rs := b.B.Rows("ref.industry", m); len(rs) > 0 {
			ev = append(ev, rs[len(rs)-1].ID)
			b.sources(rs)
			if vf := str(rs[len(rs)-1], "valid_from"); len(b.B.Days) > 0 && vf > b.B.Days[0] && vf <= day {
				b.S.Note(group, subj, b.B.Entities[m].Name+"调入行业日期", vf, facts.KindReported, vf, rs[len(rs)-1].ID)
				b.S.Limit(fmt.Sprintf("%s 于 %s 调入%s，行业聚合按 %s 的成分计算。", b.name(m), vf, ind.Name, day))
			}
		}
	}
	b.S.Value(group, subj, "成分股数量", "截至 "+day, facts.KindReported, "申万分类", "count:家", decimal.NewFromInt(int64(len(members))), ev...)
	type mv struct {
		id uuid.UUID
		in facts.Input
	}
	var mvs []mv
	var all []facts.Input
	for _, m := range members {
		rs := b.B.Rows("mkt.valuation_daily", m)
		if len(rs) == 0 {
			continue
		}
		b.sources(rs)
		if in, ok := input(rs[len(rs)-1], "total_mv", "CNY"); ok {
			mvs = append(mvs, mv{m, in})
			all = append(all, in)
		}
	}
	if len(mvs) < len(members) {
		b.S.Limit(fmt.Sprintf("%s 有 %d 家成分缺少估值数据，市值合计只含 %d 家。", subj, len(members)-len(mvs), len(mvs)))
	}
	if len(mvs) == 0 {
		return
	}
	total := facts.Sum("F27.event_sum", "CNY", all)
	b.S.Computed(group, subj, "成分股总市值合计", day, total, ind.Code+"|mv")
	slices.SortFunc(mvs, func(a, c mv) int {
		if d := c.in.Value.Cmp(a.in.Value); d != 0 {
			return d
		}
		return strings.Compare(b.B.Entities[a.id].Code, b.B.Entities[c.id].Code)
	})
	t := facts.Table{Title: subj + " 成分股市值排名", Columns: []string{"排名", "公司", "总市值（亿元）", "占比"}}
	for i, m := range mvs {
		if i >= 10 {
			break
		}
		share := facts.Ratio("F22.share_of_total", m.in, facts.Input{Value: *total.Result}, "合计不为正")
		if i < 3 {
			share.Inputs = append([]facts.Input{m.in}, all...)
			b.S.Computed(group, subj, b.name(m.id)+"市值占行业合计比例", day, share, ind.Code+"|mvshare|"+b.B.Entities[m.id].Code)
		}
		t.Rows = append(t.Rows, []*string{facts.Ptr(fmt.Sprint(i + 1)), facts.Ptr(b.name(m.id)),
			facts.Ptr(m.in.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)), facts.Ptr(share.Result.Mul(decimal.NewFromInt(100)).StringFixed(2) + "%")})
		t.Evidence = append(t.Evidence, m.in.EvidenceID)
	}
	b.S.Tables[section] = append(b.S.Tables[section], t)
}

// industryFin builds ind.fin / ind.concentration / ind.top facts.
func (b *builder) industryFin(ind dataservice.EntityRow, members []uuid.UUID, finGroup, concGroup, topGroup, topSection string) bool {
	subj := ind.Name + "（" + ind.Code + "）"
	mfs, fy := b.memberFins(members)
	if len(mfs) < 2 {
		return false
	}
	pl := periodLabel(fy) + " 对比 " + periodLabel(priorYear(fy))
	var cur, prev, npc, npp, revAll, costRev, cost []facts.Input
	profitable, both := 0, 0
	var yoys []facts.Input
	for _, m := range mfs {
		if m.prevRev != nil {
			cur, prev = append(cur, *m.rev), append(prev, *m.prevRev)
			both++
		}
		if m.np != nil && m.prevNp != nil {
			npc, npp = append(npc, *m.np), append(npp, *m.prevNp)
		}
		if m.np != nil && m.np.Value.IsPositive() {
			profitable++
		}
		revAll = append(revAll, *m.rev)
		if m.cost != nil && !b.B.Entities[m.id].IsFinancial {
			costRev, cost = append(costRev, *m.rev), append(cost, *m.cost)
		}
		if m.yoy.OK() {
			yoys = append(yoys, facts.Input{EvidenceID: m.rev.EvidenceID, Value: *m.yoy.Result})
		}
	}
	if both < len(mfs) {
		b.S.Limit(fmt.Sprintf("%s 有 %d 家成分缺少上年同期数据，增速按两期均有数据的 %d 家计算。", subj, len(mfs)-both, both))
	}
	b.S.Computed(finGroup, subj, "成分股营业收入合计", periodLabel(fy), facts.Sum("F27.event_sum", "CNY", revAll), ind.Code+"|revsum")
	b.S.Computed(finGroup, subj, "成分股营业收入合计同比（同口径）", pl, facts.AggregateYoY(cur, prev), ind.Code+"|revyoy")
	if len(npc) > 0 {
		b.S.Computed(finGroup, subj, "成分股归母净利润合计同比（同口径）", pl, facts.AggregateYoY(npc, npp), ind.Code+"|npyoy")
	}
	if len(cost) > 0 {
		gm := facts.Derived("F03.gross_margin_aggregate", "ratio", append(slices.Clone(costRev), cost...), func() (*decimal.Decimal, string) {
			r, c := decimal.Zero, decimal.Zero
			for i := range costRev {
				r, c = r.Add(costRev[i].Value), c.Add(cost[i].Value)
			}
			if !r.IsPositive() {
				return nil, "营业收入合计不为正"
			}
			v := r.Sub(c).Div(r)
			return &v, ""
		})
		b.S.Computed(finGroup, subj, "成分股毛利率（合计法，不含金融类）", periodLabel(fy), gm, ind.Code+"|gm")
	}
	b.S.Value(finGroup, subj, "归母净利润为正的成分股数量", periodLabel(fy), facts.KindComputed, "", "count:家", decimal.NewFromInt(int64(profitable)), idsOf(revAll)...)
	// concentration
	slices.SortFunc(mfs, func(a, c memberFin) int {
		if d := c.rev.Value.Cmp(a.rev.Value); d != 0 {
			return d
		}
		return strings.Compare(b.B.Entities[a.id].Code, b.B.Entities[c.id].Code)
	})
	if len(mfs) > 5 {
		ranked := make([]facts.Ranked, len(mfs))
		for i, m := range mfs {
			ranked[i] = facts.Ranked{Key: b.B.Entities[m.id].Code, Input: *m.rev}
		}
		top, _ := facts.TopN(ranked, 5, "CNY")
		cr5 := facts.Derived("F22.share_of_total", "ratio", revAll, func() (*decimal.Decimal, string) {
			t := decimal.Zero
			for _, x := range revAll {
				t = t.Add(x.Value)
			}
			if !t.IsPositive() || !top.OK() {
				return nil, "营业收入合计不为正"
			}
			v := top.Result.Div(t)
			return &v, ""
		})
		b.S.Computed(concGroup, subj, "营业收入前 5 名集中度 CR5", periodLabel(fy), cr5, ind.Code+"|cr5")
	}
	b.S.Computed(concGroup, subj, "成分股营业收入同比中位数", pl, facts.Derived("F16.median", "ratio", yoys, func() (*decimal.Decimal, string) {
		if len(yoys) < 3 {
			return nil, "有效样本不足 3 家"
		}
		vals := make([]decimal.Decimal, len(yoys))
		for i, y := range yoys {
			vals[i] = y.Value
		}
		slices.SortFunc(vals, func(a, c decimal.Decimal) int { return a.Cmp(c) })
		m := vals[len(vals)/2]
		if len(vals)%2 == 0 {
			m = vals[len(vals)/2-1].Add(vals[len(vals)/2]).Div(decimal.NewFromInt(2))
		}
		return &m, ""
	}), ind.Code+"|yoymed")
	pos := 0
	for _, y := range yoys {
		if y.Value.IsPositive() {
			pos++
		}
	}
	b.S.Value(concGroup, subj, "营业收入同比为正的成分股数量", pl, facts.KindComputed, "", "count:家", decimal.NewFromInt(int64(pos)), idsOf(yoys)...)
	// top companies
	t := facts.Table{Title: subj + " 营业收入前 5 名", Columns: []string{"公司", "营业收入（亿元）", "同比", "归母净利润（亿元）"}}
	for i := 0; i < len(mfs) && i < 5; i++ {
		m := mfs[i]
		name := b.name(m.id)
		b.S.Value(topGroup, name, "营业收入（合并）", periodLabel(fy), facts.KindReported, "合并报表", "CNY", m.rev.Value, m.rev.EvidenceID)
		yoyCell := "—"
		if m.prevRev != nil {
			f := b.S.Computed(topGroup, name, "营业收入（合并）同比", pl, m.yoy, ind.Code+"|top|"+b.B.Entities[m.id].Code)
			yoyCell = f.Display + f.Unit
		}
		npCell := "—"
		if m.np != nil {
			f := b.S.Value(topGroup, name, "归母净利润", periodLabel(fy), facts.KindReported, "归属于母公司股东", "CNY", m.np.Value, m.np.EvidenceID)
			npCell = m.np.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)
			_ = f
		}
		t.Rows = append(t.Rows, []*string{facts.Ptr(name), facts.Ptr(m.rev.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)), facts.Ptr(yoyCell), facts.Ptr(npCell)})
		t.Evidence = append(t.Evidence, m.rev.EvidenceID)
	}
	b.S.Tables[topSection] = append(b.S.Tables[topSection], t)
	return true
}

// industryValuation: median PE/PB of members and industry index performance.
func (b *builder) industryValuation(ind dataservice.EntityRow, members []uuid.UUID, n int, group, section string) bool {
	subj := ind.Name + "（" + ind.Code + "）"
	basis := b.basis["mkt.valuation_daily"]
	found := false
	for _, m := range []struct{ f, label string }{{"pe_ttm", "PE(TTM)"}, {"pb_mrq", "PB(MRQ)"}} {
		var in []facts.Input
		for _, mem := range members {
			if rs := b.B.Rows("mkt.valuation_daily", mem); len(rs) > 0 {
				b.sources(rs)
				if v, ok := input(rs[len(rs)-1], m.f, "times"); ok {
					in = append(in, v)
				}
			}
		}
		if len(in) == 0 {
			continue
		}
		found = true
		c := facts.Median("F16.median", "times", in, 3)
		b.S.Computed(group, subj, "成分股 "+m.label+" 中位数（仅正值）", "截至 "+b.B.AsOf.Format("2006-01-02"), c, ind.Code+"|med|"+m.f).Vendor(basis)
	}
	if idx := industryIndex(ind); idx != "" && len(b.B.Index(idx)) > 0 {
		days := b.B.Window(n)
		wl := windowLabel(n, days)
		ip := b.indexPoints(idx, days)
		ir := facts.PeriodReturn(ip, len(days))
		benchmark, benchName := b.benchmark()
		br := facts.PeriodReturn(b.indexPoints(benchmark, days), len(days))
		b.S.Computed(group, subj, "行业指数区间涨跌幅", wl, ir, ind.Code+"|idxret")
		b.S.Computed(group, benchName, "基准指数区间涨跌幅", wl, br, ind.Code+"|bench")
		b.S.Computed(group, subj, "行业指数相对"+benchName+"超额收益", wl, matchedExcess(ip, b.indexPoints(benchmark, days)), ind.Code+"|idxex")
		found = true
		s := facts.Series{ID: "industry_index", Title: "行业指数与基准", Kind: "line", X: "trade_date", Unit: "点", Section: section}
		for _, l := range []struct {
			name string
			pts  []facts.Point
		}{{ind.Name, ip}, {benchName, b.indexPoints(benchmark, days)}} {
			line := facts.SeriesLine{Name: l.name}
			for _, p := range l.pts {
				x := p.Date
				if p.Missing {
					line.Points = append(line.Points, [2]*string{&x, nil})
					continue
				}
				v := p.Value.StringFixed(2)
				line.Points = append(line.Points, [2]*string{&x, &v})
				s.Evidence = append(s.Evidence, p.EvidenceID)
			}
			s.Lines = append(s.Lines, line)
		}
		b.S.Charts = append(b.S.Charts, s)
	}
	return found
}

// groupMargin: aggregate margin balance change over members and the top movers.
func (b *builder) groupMargin(label, code string, members []uuid.UUID, n int, group string) bool {
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	var begins, ends []facts.Input
	type mv struct {
		id  uuid.UUID
		chg facts.Calc
	}
	var movers []mv
	for _, m := range members {
		rs := b.B.Rows("flow.margin", m)
		if len(rs) == 0 {
			continue
		}
		b.sources(rs)
		var first, last *dataservice.StoredRecord
		for i := range rs {
			if inWindow(rs[i], "trade_date", days) {
				if first == nil {
					first = &rs[i]
				}
				last = &rs[i]
			}
		}
		if first == nil || first == last {
			continue
		}
		bi, _ := input(*first, "financing_balance", "CNY")
		ei, _ := input(*last, "financing_balance", "CNY")
		begins, ends = append(begins, bi), append(ends, ei)
		movers = append(movers, mv{m, facts.BalanceChange(bi, ei)})
	}
	if len(ends) == 0 {
		return false
	}
	all := append(slices.Clone(begins), ends...)
	b.S.Computed(group, label, "融资余额合计", "截至 "+days[len(days)-1], facts.Sum("F27.event_sum", "CNY", ends), code+"|mbal")
	f := b.S.Computed(group, label, "融资余额合计变化", wl, facts.Derived("F18.balance_change", "CNY", all, func() (*decimal.Decimal, string) {
		t := decimal.Zero
		for i := range ends {
			t = t.Add(ends[i].Value.Sub(begins[i].Value))
		}
		return &t, ""
	}), code+"|mchg")
	f.Change = true
	b.S.Value(group, label, "纳入统计的融资标的数量", wl, facts.KindComputed, "", "count:家", decimal.NewFromInt(int64(len(ends))), idsOf(ends)...)
	slices.SortFunc(movers, func(a, c mv) int {
		if d := c.chg.Result.Cmp(*a.chg.Result); d != 0 {
			return d
		}
		return strings.Compare(b.B.Entities[a.id].Code, b.B.Entities[c.id].Code)
	})
	for i := 0; i < len(movers) && i < 3; i++ {
		m := movers[i]
		b.S.Computed(group, b.name(m.id), "融资余额变化（净增排名第 "+fmt.Sprint(i+1)+" 位）", wl, m.chg, code+"|mtop|"+b.B.Entities[m.id].Code)
	}
	return true
}

// groupEvents: dragon-tiger and block-trade counts across members.
func (b *builder) groupEvents(label, code string, members []uuid.UUID, n int, group string) bool {
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	var ib, is, bt []facts.Input
	var dtEv []uuid.UUID
	listed := map[uuid.UUID]int{}
	for _, m := range members {
		for _, r := range b.B.Rows("flow.dragon_tiger", m) {
			if !inWindow(r, "trade_date", days) {
				continue
			}
			b.source(r)
			listed[m]++
			dtEv = append(dtEv, r.ID)
			if str(r, "seat_type") == "institution" {
				bv, _ := input(r, "buy_amount", "CNY")
				sv, _ := input(r, "sell_amount", "CNY")
				ib, is = append(ib, bv), append(is, sv)
			}
		}
		for _, r := range b.B.Rows("flow.block_trade", m) {
			if inWindow(r, "trade_date", days) {
				b.source(r)
				a, _ := input(r, "amount", "CNY")
				bt = append(bt, a)
			}
		}
	}
	if len(listed) == 0 && len(bt) == 0 {
		return false
	}
	if len(ib) > 0 {
		b.S.Computed(group, label, "龙虎榜机构专用席位买入额合计", wl, facts.Sum("F27.event_sum", "CNY", ib), code+"|ib")
		b.S.Computed(group, label, "龙虎榜机构专用席位卖出额合计", wl, facts.Sum("F27.event_sum", "CNY", is), code+"|is")
	}
	if len(listed) > 0 {
		b.S.Value(group, label, "上榜龙虎榜的成分股数量", wl, facts.KindMarket, "交易所披露", "count:家", decimal.NewFromInt(int64(len(listed))), dtEv...)
	}
	if len(bt) > 0 {
		b.S.Value(group, label, "大宗交易笔数", wl, facts.KindMarket, "交易所披露", "count:笔", decimal.NewFromInt(int64(len(bt))), idsOf(bt)...)
		b.S.Computed(group, label, "大宗交易成交额合计", wl, facts.Sum("F27.event_sum", "CNY", bt), code+"|bt")
	}
	return true
}

// groupVendor: vendor order-size net sums across members (L4).
func (b *builder) groupVendor(label, code string, members []uuid.UUID, n int, group string) bool {
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	var xl []facts.Input
	for _, m := range members {
		for _, r := range b.B.Rows("flow.order_size", m) {
			if inWindow(r, "trade_date", days) {
				b.source(r)
				v, _ := input(r, "xl_net", "CNY")
				xl = append(xl, v)
			}
		}
	}
	if len(xl) == 0 {
		return false
	}
	f := b.S.Computed(group, label, "厂商口径超大单净额合计", wl, facts.Sum("F21.sum_over_window", "CNY", xl), code+"|xl").Vendor(b.basis["flow.order_size"])
	f.Change = true
	return true
}

// groupHoldings: quarterly fund holding count change across members.
func (b *builder) groupHoldings(label, code string, members []uuid.UUID, group string) bool {
	var cur, prev []facts.Input
	period := ""
	for _, m := range members {
		rs := b.B.Rows("fund.holding", m)
		if len(rs) < 2 {
			continue
		}
		b.sources(rs)
		c, p := rs[len(rs)-1], rs[len(rs)-2]
		period = str(p, "period_end") + " 至 " + str(c, "period_end")
		cur = append(cur, facts.Input{EvidenceID: c.ID, Value: decimal.NewFromInt(int64(num(c, "fund_count")))})
		prev = append(prev, facts.Input{EvidenceID: p.ID, Value: decimal.NewFromInt(int64(num(p, "fund_count")))})
	}
	if len(cur) == 0 {
		return false
	}
	all := append(slices.Clone(prev), cur...)
	f := b.S.Computed(group, label, "成分股被公募基金持有的家次合计变化", period, facts.Derived("F18.balance_change", "count", all, func() (*decimal.Decimal, string) {
		t := decimal.Zero
		for i := range cur {
			t = t.Add(cur[i].Value.Sub(prev[i].Value))
		}
		return &t, ""
	}), code+"|fund")
	if f.Value != nil {
		f.Display, f.Unit = facts.Display(*f.Value, "count:只")
		f.Change = true
	}
	return true
}

// position: subject's rank among industry members (SX).
func (b *builder) position(subject uuid.UUID, ind dataservice.EntityRow, members []uuid.UUID, group string) bool {
	mfs, fy := b.memberFins(members)
	idx := slices.IndexFunc(mfs, func(m memberFin) bool { return m.id == subject })
	if idx < 0 || len(mfs) < 3 {
		return false
	}
	subj := b.name(subject)
	me := mfs[idx]
	rank := func(val func(memberFin) *decimal.Decimal, evidence func(memberFin) []uuid.UUID, label string) {
		mv := val(me)
		if mv == nil {
			return
		}
		r, n := 1, 0
		var refs []uuid.UUID
		for _, m := range mfs {
			v := val(m)
			if v == nil {
				continue
			}
			n++
			refs = append(refs, evidence(m)...)
			if v.GreaterThan(*mv) {
				r++
			}
		}
		b.S.Note(group, subj, label+"在"+ind.Name+"已采集可比样本中的排位（由高到低）", periodLabel(fy), facts.KindComputed,
			fmt.Sprintf("第 %d 位（共 %d 家）", r, n), refs...)
	}
	rank(func(m memberFin) *decimal.Decimal { return &m.rev.Value },
		func(m memberFin) []uuid.UUID { return []uuid.UUID{m.rev.EvidenceID} }, "营业收入")
	rank(func(m memberFin) *decimal.Decimal {
		if m.np == nil {
			return nil
		}
		return &m.np.Value
	}, func(m memberFin) []uuid.UUID { return []uuid.UUID{m.np.EvidenceID} }, "归母净利润")
	rank(func(m memberFin) *decimal.Decimal {
		if !m.yoy.OK() {
			return nil
		}
		return m.yoy.Result
	}, func(m memberFin) []uuid.UUID { return []uuid.UUID{m.rev.EvidenceID, m.prevRev.EvidenceID} }, "营业收入同比")
	return true
}

// marketOverview: broad index, two-market turnover from index amount, connect turnover when present.
func (b *builder) marketOverview(n int, group, section string) bool {
	days := b.B.Window(n)
	if len(b.B.Days) < 2*n {
		return false
	}
	prevDays := b.B.Days[len(b.B.Days)-2*n : len(b.B.Days)-n]
	wl := windowLabel(n, days)
	benchmark, benchName := b.benchmark()
	ip := b.indexPoints(benchmark, days)
	b.S.Computed(group, benchName, "区间涨跌幅", wl, facts.PeriodReturn(ip, len(days)), "mkt|ret")
	amt := func(ds []string) []facts.Point {
		by := map[string]dataservice.StoredRecord{}
		for _, r := range b.B.Index(benchmark) {
			by[str(r, "trade_date")] = r
		}
		var out []facts.Point
		for _, d := range ds {
			r, ok := by[d]
			if !ok {
				out = append(out, facts.Point{Date: d, Missing: true})
				continue
			}
			v, _ := dec(r, "amount")
			out = append(out, facts.Point{Date: d, EvidenceID: r.ID, Value: v})
		}
		return out
	}
	cur := facts.SumWindow("F17.avg_daily_amount", amt(days), len(days), "CNY", true)
	prev := facts.SumWindow("F17.avg_daily_amount", amt(prevDays), len(prevDays), "CNY", true)
	b.S.Computed(group, benchName+"成分", "日均成交额", wl, cur, "mkt|amt")
	if cur.OK() && prev.OK() {
		yo := facts.YoY(facts.Input{Value: *cur.Result}, facts.Input{Value: *prev.Result})
		yo.FormulaID = "F02.window_over_window"
		yo.Inputs = append(slices.Clone(cur.Inputs), prev.Inputs...)
		b.S.Computed(group, benchName+"成分", "日均成交额较前一窗口变化率", wl+" 对比前 "+fmt.Sprint(n)+" 个交易日", yo, "mkt|amtchg")
	}
	if ct := b.B.Global("flow.connect_turnover"); len(ct) > 0 {
		var pts []facts.Input
		for _, r := range ct {
			if inWindow(r, "trade_date", days) {
				v, _ := input(r, "turnover", "CNY")
				pts = append(pts, v)
			}
		}
		b.S.Computed(group, "陆股通", "成交总额合计", wl, facts.Sum("F21.sum_over_window", "CNY", pts), "mkt|connect")
	}
	s := facts.Series{ID: "market_index", Title: benchName, Kind: "line", X: "trade_date", Unit: "点", Section: section}
	line := facts.SeriesLine{Name: benchName}
	for _, p := range ip {
		x := p.Date
		if p.Missing {
			line.Points = append(line.Points, [2]*string{&x, nil})
			continue
		}
		v := p.Value.StringFixed(2)
		line.Points = append(line.Points, [2]*string{&x, &v})
		s.Evidence = append(s.Evidence, p.EvidenceID)
	}
	s.Lines = []facts.SeriesLine{line}
	b.S.Charts = append(b.S.Charts, s)
	return true
}

// industryMargins ranks industries by aggregate margin balance change (market mode).
func (b *builder) industryMargins(industries []dataservice.EntityRow, membersOf map[uuid.UUID][]uuid.UUID, n int, group string) {
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	for _, ind := range industries {
		ms := membersOf[ind.ID]
		var all []facts.Input
		var t decimal.Decimal
		for _, m := range ms {
			var first, last *dataservice.StoredRecord
			rs := b.B.Rows("flow.margin", m)
			for i := range rs {
				if inWindow(rs[i], "trade_date", days) {
					if first == nil {
						first = &rs[i]
					}
					last = &rs[i]
				}
			}
			if first == nil {
				continue
			}
			bi, _ := input(*first, "financing_balance", "CNY")
			ei, _ := input(*last, "financing_balance", "CNY")
			all = append(all, bi, ei)
			t = t.Add(ei.Value.Sub(bi.Value))
		}
		if len(all) == 0 {
			continue
		}
		tt := t
		f := b.S.Computed(group, ind.Name+"（"+ind.Code+"）", "行业融资余额合计变化", wl,
			facts.Derived("F18.balance_change", "CNY", all, func() (*decimal.Decimal, string) { return &tt, "" }), "mkt|indm|"+ind.Code)
		f.Change = true
	}
}
