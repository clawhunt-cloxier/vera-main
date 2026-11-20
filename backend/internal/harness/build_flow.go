package harness

import (
	"slices"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func inWindow(r dataservice.StoredRecord, field string, days []string) bool {
	d := str(r, field)
	return len(days) > 0 && d >= days[0] && d <= days[len(days)-1]
}

// margin builds flow.margin facts; returns false when the stock has no margin data.
func (b *builder) margin(e uuid.UUID, n int, group, section string) bool {
	rs := b.B.Rows("flow.margin", e)
	if len(rs) == 0 {
		return false
	}
	b.sources(rs)
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	subj := b.name(e)
	k := func(s string) string { return b.B.Entities[e].Code + "|margin|" + s }
	by := map[string]dataservice.StoredRecord{}
	for _, r := range rs {
		by[str(r, "trade_date")] = r
	}
	var bal, buy []facts.Point
	for _, d := range days {
		r, ok := by[d]
		if !ok {
			bal = append(bal, facts.Point{Date: d, Missing: true})
			buy = append(buy, facts.Point{Date: d, Missing: true})
			continue
		}
		bal = append(bal, numericPoint(r, d, "financing_balance"))
		buy = append(buy, numericPoint(r, d, "financing_buy"))
	}
	var valid []facts.Point
	for _, p := range bal {
		if !p.Missing {
			valid = append(valid, p)
		}
	}
	if len(valid) < 2 {
		return false
	}
	first, last := valid[0], valid[len(valid)-1]
	b.S.Value(group, subj, "融资余额", last.Date, facts.KindMarket, "交易所披露", "CNY", last.Value, last.EvidenceID)
	b.S.Computed(group, subj, "融资余额变化", first.Date+" 至 "+last.Date, facts.BalanceChange(facts.Input{EvidenceID: first.EvidenceID, Value: first.Value, Unit: "CNY"},
		facts.Input{EvidenceID: last.EvidenceID, Value: last.Value, Unit: "CNY"}), k("chg"))
	b.S.Computed(group, subj, "融资买入额合计", wl, facts.SumWindow("F21.sum_over_window", buy, len(days), "CNY", false), k("buy"))
	cd := facts.ConsecutiveDays(bal)
	if cd.OK() && cd.Result.Abs().GreaterThanOrEqual(decimal.NewFromInt(3)) {
		word := "连续增加的交易日数"
		if cd.Result.IsNegative() {
			word = "连续减少的交易日数"
		}
		abs := cd.Result.Abs()
		c := cd
		c.Result = &abs
		c.Unit = "count:个交易日"
		f := b.S.Computed(group, subj, "融资余额"+word, "截至 "+last.Date, c, k("streak"))
		f.Change = false
	}
	if lv, ok := onTradingDay(b.B.Rows("mkt.valuation_daily", e), last.Date); ok {
		if fmv, ok := input(lv, "float_mv", "CNY"); ok {
			b.S.Computed(group, subj, "融资余额占流通市值比例", last.Date, facts.Ratio("F20.margin_to_float_cap",
				facts.Input{EvidenceID: last.EvidenceID, Value: last.Value, Unit: "CNY"}, fmv, "流通市值不为正"), k("ratio"))
		}
	}
	s := facts.Series{ID: "margin_balance", Title: "融资余额（亿元）", Kind: "area", X: "trade_date", Unit: "亿元", Section: section}
	line := facts.SeriesLine{Name: "融资余额"}
	for _, p := range bal {
		x := p.Date
		if p.Missing {
			line.Points = append(line.Points, [2]*string{&x, nil})
			continue
		}
		v := p.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)
		line.Points = append(line.Points, [2]*string{&x, &v})
		s.Evidence = append(s.Evidence, p.EvidenceID)
	}
	s.Lines = []facts.SeriesLine{line}
	b.S.Charts = append(b.S.Charts, s)
	return true
}

// seats builds dragon-tiger and block-trade facts over the window.
func (b *builder) seats(e uuid.UUID, n int, group, section string) bool {
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	subj := b.name(e)
	k := func(s string) string { return b.B.Entities[e].Code + "|seats|" + s }
	found := false
	var dtDays []string
	var instBuy, instSell []facts.Input
	t := facts.Table{Title: subj + " 龙虎榜上榜记录", Columns: []string{"交易日", "上榜原因", "席位", "席位类型", "买入额（万元）", "卖出额（万元）"}}
	dr := b.B.Rows("flow.dragon_tiger", e)
	b.sources(dr)
	for _, r := range dr {
		if !inWindow(r, "trade_date", days) {
			continue
		}
		found = true
		if !slices.Contains(dtDays, str(r, "trade_date")) {
			dtDays = append(dtDays, str(r, "trade_date"))
		}
		bv, _ := input(r, "buy_amount", "CNY")
		sv, _ := input(r, "sell_amount", "CNY")
		if str(r, "seat_type") == "institution" {
			instBuy = append(instBuy, bv)
			instSell = append(instSell, sv)
		}
		t.Rows = append(t.Rows, []*string{facts.Ptr(str(r, "trade_date")), facts.Ptr(str(r, "reason_text")), facts.Ptr(str(r, "seat_name")),
			facts.Ptr(str(r, "seat_type")), facts.Ptr(bv.Value.Div(decimal.NewFromInt(10000)).StringFixed(2)),
			facts.Ptr(sv.Value.Div(decimal.NewFromInt(10000)).StringFixed(2))})
		t.Evidence = append(t.Evidence, r.ID)
	}
	if len(dtDays) > 0 {
		var ev []uuid.UUID
		for _, r := range dr {
			if inWindow(r, "trade_date", days) {
				ev = append(ev, r.ID)
			}
		}
		b.S.Value(group, subj, "龙虎榜上榜交易日数", wl, facts.KindMarket, "交易所披露", "count:个交易日", decimal.NewFromInt(int64(len(dtDays))), ev...)
		if len(instBuy) > 0 {
			b.S.Computed(group, subj, "龙虎榜机构专用席位买入额合计", wl, facts.Sum("F27.event_sum", "CNY", instBuy), k("ib"))
			b.S.Computed(group, subj, "龙虎榜机构专用席位卖出额合计", wl, facts.Sum("F27.event_sum", "CNY", instSell), k("is"))
		}
		b.S.Tables[section] = append(b.S.Tables[section], t)
	}
	bt := b.B.Rows("flow.block_trade", e)
	b.sources(bt)
	bars := map[string]dataservice.StoredRecord{}
	for _, r := range b.B.Rows("mkt.daily_bar", e) {
		bars[str(r, "trade_date")] = r
	}
	var amts []facts.Input
	var lastBT *dataservice.StoredRecord
	for i, r := range bt {
		if !inWindow(r, "trade_date", days) {
			continue
		}
		found = true
		a, ok := input(r, "amount", "CNY")
		if !ok {
			continue
		}
		amts = append(amts, a)
		lastBT = &bt[i]
	}
	if len(amts) > 0 {
		b.S.Value(group, subj, "大宗交易笔数", wl, facts.KindMarket, "交易所披露", "count:笔", decimal.NewFromInt(int64(len(amts))), idsOf(amts)...)
		b.S.Computed(group, subj, "大宗交易成交额合计", wl, facts.Sum("F27.event_sum", "CNY", amts), k("bt"))
		if bar, ok := bars[str(*lastBT, "trade_date")]; ok {
			p, okP := input(*lastBT, "price", "CNY")
			c, okC := input(bar, "close", "CNY")
			if okP && okC {
				f := b.S.Computed(group, subj, "最近交易日的一笔大宗交易成交价相对当日收盘价", str(*lastBT, "trade_date"), facts.BlockPremium(p, c), k("prem"))
				f.Change = true
			}
		}
	}
	return found
}

func idsOf(in []facts.Input) []uuid.UUID {
	var out []uuid.UUID
	for _, i := range in {
		out = append(out, i.EvidenceID)
	}
	return out
}

// holdings builds quarterly connect and fund holding changes.
func (b *builder) holdings(e uuid.UUID, group string) bool {
	subj := b.name(e)
	k := func(s string) string { return b.B.Entities[e].Code + "|hold|" + s }
	found := false
	if rs := b.B.Rows("flow.connect_holding", e); len(rs) >= 2 {
		b.sources(rs)
		found = true
		cur, prev := rs[len(rs)-1], rs[len(rs)-2]
		pl := str(prev, "period_end") + " 至 " + str(cur, "period_end")
		c, _ := input(cur, "holding_shares", "shares")
		p, _ := input(prev, "holding_shares", "shares")
		b.S.Value(group, subj, "陆股通持股数量", str(cur, "period_end"), facts.KindMarket, "港交所季度披露", "shares", c.Value, cur.ID)
		f := b.S.Computed(group, subj, "陆股通持股数量季度变化", pl, facts.BalanceChange(p, c), k("cs"))
		f.Unit = "万股"
		f.Display, f.Unit = facts.Display(*f.Value, "shares")
		cr, _ := input(cur, "holding_ratio", "ratio")
		pr, _ := input(prev, "holding_ratio", "ratio")
		b.S.Value(group, subj, "陆股通持股占比", str(cur, "period_end"), facts.KindMarket, "港交所季度披露", "ratio", cr.Value, cur.ID)
		ch := cr.Value.Sub(pr.Value)
		b.S.Computed(group, subj, "陆股通持股占比季度变化", pl, facts.PPChange(
			facts.Calc{Applicability: facts.Applicable, Result: &cr.Value, Inputs: []facts.Input{cr}},
			facts.Calc{Applicability: facts.Applicable, Result: &pr.Value, Inputs: []facts.Input{pr}}), k("cr"))
		_ = ch
	}
	if rs := b.B.Rows("fund.holding", e); len(rs) >= 2 {
		b.sources(rs)
		found = true
		cur, prev := rs[len(rs)-1], rs[len(rs)-2]
		pl := str(prev, "period_end") + " 至 " + str(cur, "period_end")
		basis := "基金定期报告（" + str(cur, "disclosure_basis") + "）"
		b.S.Value(group, subj, "持有该股的公募基金数量", str(cur, "period_end"), facts.KindReported, basis, "count:只", decimal.NewFromInt(int64(num(cur, "fund_count"))), cur.ID)
		fc := b.S.Computed(group, subj, "持股公募基金数量季度变化", pl, facts.BalanceChange(
			facts.Input{EvidenceID: prev.ID, Value: decimal.NewFromInt(int64(num(prev, "fund_count")))},
			facts.Input{EvidenceID: cur.ID, Value: decimal.NewFromInt(int64(num(cur, "fund_count")))}), k("fc"))
		fc.Display, fc.Unit = facts.Display(*fc.Value, "count:只")
		cr, _ := input(cur, "holding_ratio_float", "ratio")
		pr, _ := input(prev, "holding_ratio_float", "ratio")
		b.S.Value(group, subj, "公募基金持股占流通股比例", str(cur, "period_end"), facts.KindReported, basis, "ratio", cr.Value, cur.ID)
		b.S.Computed(group, subj, "公募基金持股占流通股比例季度变化", pl, facts.PPChange(
			facts.Calc{Applicability: facts.Applicable, Result: &cr.Value, Inputs: []facts.Input{cr}},
			facts.Calc{Applicability: facts.Applicable, Result: &pr.Value, Inputs: []facts.Input{pr}}), k("fr"))
	}
	return found
}

// vendorFlow builds flow.vendor facts from order-size data (L4, basis required in prose).
func (b *builder) vendorFlow(e uuid.UUID, n int, group string) bool {
	rs := b.B.Rows("flow.order_size", e)
	if len(rs) == 0 {
		return false
	}
	b.sources(rs)
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	subj := b.name(e)
	basis := b.basis["flow.order_size"]
	by := map[string]dataservice.StoredRecord{}
	for _, r := range rs {
		by[str(r, "trade_date")] = r
	}
	for _, fld := range []struct{ f, label string }{{"xl_net", "超大单净额"}, {"l_net", "大单净额"}, {"s_net", "小单净额"}} {
		var pts []facts.Point
		for _, d := range days {
			r, ok := by[d]
			if !ok {
				pts = append(pts, facts.Point{Date: d, Missing: true})
				continue
			}
			pts = append(pts, numericPoint(r, d, fld.f))
		}
		c := facts.SumWindow("F21.sum_over_window", pts, len(days), "CNY", false)
		f := b.S.Computed(group, subj, "厂商口径"+fld.label+"累计", wl, c, b.B.Entities[e].Code+"|vendor|"+fld.f).Vendor(basis)
		f.Change = true
	}
	return true
}

// corp builds shareholder and corporate-event facts.
func (b *builder) corp(e uuid.UUID, group string) bool {
	subj := b.name(e)
	k := func(s string) string { return b.B.Entities[e].Code + "|corp|" + s }
	found := false
	var counts []dataservice.StoredRecord
	for _, r := range b.B.Rows("corp.holder", e) {
		if str(r, "holder_kind") == "count" {
			counts = append(counts, r)
		}
	}
	if len(counts) >= 2 {
		b.sources(counts)
		found = true
		cur, prev := counts[len(counts)-1], counts[len(counts)-2]
		c := facts.Input{EvidenceID: cur.ID, Value: decimal.NewFromInt(int64(num(cur, "holder_count")))}
		p := facts.Input{EvidenceID: prev.ID, Value: decimal.NewFromInt(int64(num(prev, "holder_count")))}
		b.S.Value(group, subj, "股东户数", str(cur, "period_end"), facts.KindReported, "", "count:户", c.Value, cur.ID)
		f := b.S.Computed(group, subj, "股东户数变化", str(prev, "period_end")+" 至 "+str(cur, "period_end"), facts.BalanceChange(p, c), k("hc"))
		f.Display, f.Unit = facts.Display(*f.Value, "count:户")
		b.S.Computed(group, subj, "股东户数变化率", str(prev, "period_end")+" 至 "+str(cur, "period_end"), facts.YoY(c, p), k("hcr"))
	}
	from := b.B.AsOf.AddDate(-1, 0, 0)
	labels := map[string]string{"increase": "增持", "decrease": "减持", "plan_increase": "增持计划", "plan_decrease": "减持计划"}
	for _, r := range b.B.Rows("corp.insider_trade", e) {
		if r.AvailableAt.Before(from) {
			continue
		}
		b.source(r)
		found = true
		sh, _ := input(r, "shares", "shares")
		period := str(r, "start_date") + " 至 " + str(r, "end_date")
		f := b.S.Value(group, subj, str(r, "holder_name")+labels[str(r, "direction")]+"股数（上限）", period, facts.KindReported,
			"公司公告 "+str(r, "announcement_id"), "shares", sh.Value, r.ID)
		if str(r, "end_date") > b.B.AsOf.Format("2006-01-02") {
			f.Flag("scheduled")
		}
	}
	for _, r := range b.B.Rows("corp.pledge", e) {
		b.source(r)
		found = true
		v, _ := input(r, "pledged_ratio_of_total", "ratio")
		b.S.Value(group, subj, str(r, "holder_name")+"质押股数占总股本比例", str(r, "as_of_date"), facts.KindReported, "", "ratio", v.Value, r.ID)
		v2, _ := input(r, "pledged_ratio_of_holding", "ratio")
		b.S.Value(group, subj, str(r, "holder_name")+"质押股数占其持股比例", str(r, "as_of_date"), facts.KindReported, "", "ratio", v2.Value, r.ID)
	}
	horizon := b.B.AsOf.AddDate(0, 6, 0).Format("2006-01-02")
	for _, r := range b.B.Rows("corp.unlock", e) {
		d := str(r, "unlock_date")
		if d < b.B.AsOf.Format("2006-01-02") || d > horizon {
			continue
		}
		b.source(r)
		found = true
		sh, _ := input(r, "shares", "shares")
		b.S.Value(group, subj, "限售股解禁股数（"+str(r, "holder_type")+"）", d, facts.KindReported, "公司公告", "shares", sh.Value, r.ID).Flag("scheduled")
	}
	if ds := b.B.Rows("corp.dividend", e); len(ds) > 0 {
		r := ds[len(ds)-1]
		b.source(r)
		found = true
		if c, ok := input(r, "cash_per_10_shares", "CNY"); ok {
			b.S.Value(group, subj, "每 10 股派现（含税）", str(r, "fiscal_year")+" 年度", facts.KindReported, "", "price", c.Value, r.ID)
		}
		if t, ok := input(r, "total_cash", "CNY"); ok {
			b.S.Value(group, subj, "现金分红总额", str(r, "fiscal_year")+" 年度", facts.KindReported, "", "CNY", t.Value, r.ID)
		}
	}
	return found
}

// groupInsider lists shareholder trade announcements of members disclosed within the window.
func (b *builder) groupInsider(label string, members []uuid.UUID, n int, group string) bool {
	days := b.B.Window(n)
	wl := windowLabel(n, days)
	if len(days) == 0 {
		return false
	}
	from := days[0]
	var ev []uuid.UUID
	firms := map[uuid.UUID]bool{}
	labels := map[string]string{"increase": "增持", "decrease": "减持", "plan_increase": "增持计划", "plan_decrease": "减持计划"}
	for _, m := range members {
		for _, r := range b.B.Rows("corp.insider_trade", m) {
			if r.AvailableAt.Format("2006-01-02") < from {
				continue
			}
			b.source(r)
			ev = append(ev, r.ID)
			firms[m] = true
			sh, _ := input(r, "shares", "shares")
			f := b.S.Value(group, b.name(m), str(r, "holder_name")+labels[str(r, "direction")]+"股数（上限）",
				str(r, "start_date")+" 至 "+str(r, "end_date"), facts.KindReported, "公司公告 "+str(r, "announcement_id"), "shares", sh.Value, r.ID)
			if str(r, "end_date") > b.B.AsOf.Format("2006-01-02") {
				f.Flag("scheduled")
			}
		}
	}
	b.S.Value(group, label, "窗口内披露股东增减持公告的成分股数量", wl, facts.KindReported, "公司公告", "count:家",
		decimal.NewFromInt(int64(len(firms))), ev...)
	return true
}
