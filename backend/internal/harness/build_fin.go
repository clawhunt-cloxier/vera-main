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

// builder wraps the sheet and bundle with helpers shared by all skills.
type builder struct {
	S     *facts.Sheet
	B     *Bundle
	basis map[string]string // provider basis by dataset
}

func input(r dataservice.StoredRecord, f, unit string) (facts.Input, bool) {
	v, ok := dec(r, f)
	return facts.Input{EvidenceID: r.ID, Value: v, Unit: unit}, ok
}

func (b *builder) name(e uuid.UUID) string {
	en := b.B.Entities[e]
	return en.Name + "（" + en.Code + "）"
}

func (b *builder) source(r dataservice.StoredRecord) {
	at := r.AvailableAt.Format("2006-01-02T15:04:05Z07:00")
	if cur := b.S.Sources[r.DatasetID]; at > cur {
		b.S.Sources[r.DatasetID] = at
	}
}

func (b *builder) sources(rs []dataservice.StoredRecord) {
	for _, r := range rs {
		b.source(r)
	}
}

// ---- financial statements ----

type finIndex struct {
	byPeriod map[string]map[string]dataservice.StoredRecord // "2025-12-31|FY" → item → record
	periods  []string                                       // sorted keys
}

func (b *builder) fin(e uuid.UUID) finIndex {
	ix := finIndex{byPeriod: map[string]map[string]dataservice.StoredRecord{}}
	rs := b.B.Rows("fin.statement", e)
	b.sources(rs)
	for _, r := range rs {
		if str(r, "scope") != "consolidated" {
			continue
		}
		k := str(r, "report_period") + "|" + str(r, "report_type")
		if ix.byPeriod[k] == nil {
			ix.byPeriod[k] = map[string]dataservice.StoredRecord{}
			ix.periods = append(ix.periods, k)
		}
		ix.byPeriod[k][str(r, "item_code")] = r
	}
	slices.Sort(ix.periods)
	return ix
}

func (ix finIndex) fiscalYears() []string {
	var out []string
	for _, p := range ix.periods {
		if strings.HasSuffix(p, "|FY") {
			out = append(out, p)
		}
	}
	return out
}

func (ix finIndex) latestInterim() string {
	for i := len(ix.periods) - 1; i >= 0; i-- {
		if !strings.HasSuffix(ix.periods[i], "|FY") {
			last := ix.fiscalYears()
			if len(last) == 0 || ix.periods[i] > last[len(last)-1] {
				return ix.periods[i]
			}
			return ""
		}
	}
	return ""
}

func (ix finIndex) get(period, item string) (facts.Input, dataservice.StoredRecord, bool) {
	r, ok := ix.byPeriod[period][item]
	if !ok {
		return facts.Input{}, r, false
	}
	in, ok := input(r, "value", "CNY")
	return in, r, ok
}

func priorYear(period string) string {
	// "2025-12-31|FY" → "2024-12-31|FY"
	y := period[:4]
	var n int
	_, _ = fmt.Sscanf(y, "%d", &n)
	return fmt.Sprintf("%04d%s", n-1, period[4:])
}

// periodLabel renders "2025-12-31|FY" as "2025 年度（2025-12-31）" and interim as "2026 年一季度（2026-03-31）".
func periodLabel(p string) string {
	date, typ, _ := strings.Cut(p, "|")
	y := date[:4]
	names := map[string]string{"FY": "年度", "Q1": "年一季度", "H1": "年半年度", "Q3": "年前三季度"}
	return fmt.Sprintf("%s %s（%s）", y, names[typ], date)
}

var itemLabel = map[string]string{
	"IS.revenue": "营业收入", "IS.total_revenue": "营业总收入", "IS.net_profit_parent": "归母净利润",
	"IS.net_profit_parent_deducted": "扣非归母净利润", "CF.operating_net": "经营活动现金流量净额", "BS.total_assets": "总资产",
	"BS.total_liabilities": "总负债", "BS.equity_parent": "归母净资产", "BS.accounts_receivable": "应收账款", "BS.inventory": "存货",
	"BS.goodwill": "商誉", "IS.operating_cost": "营业成本",
}

func (b *builder) restated(r dataservice.StoredRecord, f *facts.Fact) {
	if r.Version > 1 {
		f.Flag("restated")
		b.S.Limit(fmt.Sprintf("%s %s 数据经会计更正，按更正后版本使用。", b.B.Entities[*r.EntityID].Name, f.Period))
	}
}

// financials builds fin.annual / fin.interim / fin.quality facts and the S-table for one company.
// years is the number of fiscal years shown in the table.
func (b *builder) financials(e uuid.UUID, years int, tableSection string) bool {
	ix := b.fin(e)
	fys := ix.fiscalYears()
	if len(fys) < 2 {
		return false
	}
	subj := b.name(e)
	cur, prev := fys[len(fys)-1], fys[len(fys)-2]
	pl, ppl := periodLabel(cur), periodLabel(prev)
	add := func(group, item, period string) (*facts.Fact, facts.Input, bool) {
		in, r, ok := ix.get(period, item)
		if !ok {
			return nil, in, false
		}
		f := b.S.Value(group, subj, itemLabel[item]+"（合并）", periodLabel(period), facts.KindReported, "合并报表", "CNY", in.Value, r.ID)
		if item == "IS.net_profit_parent" || item == "IS.net_profit_parent_deducted" || item == "BS.equity_parent" {
			f.Label = itemLabel[item]
			f.Basis = "归属于母公司股东"
		}
		b.restated(r, f)
		return f, in, true
	}
	key := func(k string) string { return b.B.Entities[e].Code + "|" + k }
	for _, item := range []string{"IS.revenue", "IS.net_profit_parent", "IS.net_profit_parent_deducted", "CF.operating_net"} {
		_, c, ok1 := add("fin.annual", item, cur)
		_, p, ok2 := add("fin.annual", item, prev)
		if !ok1 || !ok2 {
			continue
		}
		label := itemLabel[item] + "同比"
		if item == "IS.revenue" {
			label = "营业收入（合并）同比"
		}
		b.S.Computed("fin.annual", subj, label, pl+" 对比 "+ppl, facts.YoY(c, p), key("yoy|"+item))
		if (c.Value.IsNegative()) != (p.Value.IsNegative()) && item != "CF.operating_net" {
			word := "由盈转亏"
			if !c.Value.IsNegative() {
				word = "由亏转盈"
			}
			b.S.Note("fin.annual", subj, itemLabel[item]+"盈亏变化", ppl+" 至 "+pl, facts.KindComputed, word, c.EvidenceID, p.EvidenceID).Flag("sign_change")
		}
	}
	margin := func(period string) (facts.Calc, facts.Calc) {
		rev, _, _ := ix.get(period, "IS.revenue")
		cost, _, _ := ix.get(period, "IS.operating_cost")
		np, _, _ := ix.get(period, "IS.net_profit_parent")
		gm := facts.GrossMargin(rev, cost)
		nm := facts.Ratio("F19.net_margin", np, rev, "营业收入不为正")
		return gm, nm
	}
	gm, nm := margin(cur)
	pgm, pnm := margin(prev)
	b.S.Computed("fin.annual", subj, "毛利率", pl, gm, key("gm|"+cur))
	b.S.Computed("fin.annual", subj, "毛利率变化", pl+" 对比 "+ppl, facts.PPChange(gm, pgm), key("gmchg"))
	b.S.Computed("fin.annual", subj, "归母净利率", pl, nm, key("nm|"+cur))
	b.S.Computed("fin.annual", subj, "归母净利率变化", pl+" 对比 "+ppl, facts.PPChange(nm, pnm), key("nmchg"))
	if np, _, ok := ix.get(cur, "IS.net_profit_parent"); ok {
		e1, _, ok1 := ix.get(prev, "BS.equity_parent")
		e2, _, ok2 := ix.get(cur, "BS.equity_parent")
		if ok1 && ok2 {
			b.S.Computed("fin.annual", subj, "ROE（归母，平均净资产）", pl, facts.ROE(np, e1, e2), key("roe"))
		}
	}
	if ta, _, ok := ix.get(cur, "BS.total_assets"); ok {
		tl, _, _ := ix.get(cur, "BS.total_liabilities")
		b.S.Computed("fin.annual", subj, "资产负债率", pl, facts.Ratio("F07.debt_ratio", tl, ta, "总资产不为正"), key("dr"))
	}
	// interim
	if q := ix.latestInterim(); q != "" {
		ql, pq := periodLabel(q), priorYear(q)
		for _, item := range []string{"IS.revenue", "IS.net_profit_parent"} {
			_, c, ok1 := add("fin.interim", item, q)
			p, _, ok2 := ix.get(pq, item)
			if ok1 && ok2 {
				b.S.Computed("fin.interim", subj, itemLabel[item]+"同比", ql+" 对比 "+periodLabel(pq), facts.YoY(c, p), key("qyoy|"+item))
			}
		}
	}
	// quality
	if np, _, ok := ix.get(cur, "IS.net_profit_parent"); ok {
		if ocf, _, ok := ix.get(cur, "CF.operating_net"); ok {
			b.S.Computed("fin.quality", subj, "经营现金流净额 / 归母净利润", pl, facts.Ratio("F06.cash_profit_ratio", ocf, np, "归母净利润不为正"), key("cpr"))
			b.S.Computed("fin.quality", subj, "经营现金流净额与归母净利润之差", pl, facts.CashProfitGap(ocf, np), key("gap"))
		}
		if d, _, ok := ix.get(cur, "IS.net_profit_parent_deducted"); ok {
			b.S.Computed("fin.quality", subj, "非经常性损益占归母净利润比例", pl, facts.NonrecurringShare(np, d), key("nrs"))
		}
	}
	for _, item := range []string{"BS.accounts_receivable", "BS.inventory"} {
		c, _, ok1 := ix.get(cur, item)
		p, _, ok2 := ix.get(prev, item)
		if ok1 && ok2 {
			b.S.Computed("fin.quality", subj, itemLabel[item]+"同比", pl+" 对比 "+ppl, facts.YoY(c, p), key("qyoy2|"+item))
		}
	}
	if gw, _, ok := ix.get(cur, "BS.goodwill"); ok {
		eq, _, _ := ix.get(cur, "BS.equity_parent")
		b.S.Computed("fin.quality", subj, "商誉 / 归母净资产", pl, facts.Ratio("F11.goodwill_to_equity", gw, eq, "归母净资产不为正"), key("gw"))
	}
	var debt []facts.Input
	for _, item := range []string{"BS.short_term_borrowing", "BS.long_term_borrowing", "BS.bonds_payable", "BS.current_portion_noncurrent_liab"} {
		if in, _, ok := ix.get(cur, item); ok {
			debt = append(debt, in)
		}
	}
	if len(debt) > 0 {
		b.S.Computed("fin.quality", subj, "有息负债合计", pl, facts.Sum("F26.interest_bearing_debt", "CNY", debt), key("ibd"))
	}
	if tableSection != "" {
		b.finTable(ix, fys, years, tableSection, subj)
	}
	return true
}

func (b *builder) finTable(ix finIndex, fys []string, years int, section, subj string) {
	if len(fys) > years {
		fys = fys[len(fys)-years:]
	}
	t := facts.Table{Title: subj + " 主要财务数据（合并报表，亿元）", Columns: []string{"指标"}}
	for _, p := range fys {
		t.Columns = append(t.Columns, p[:4])
	}
	for _, item := range []string{"IS.revenue", "IS.net_profit_parent", "IS.net_profit_parent_deducted", "CF.operating_net", "BS.total_assets", "BS.equity_parent"} {
		row := []*string{facts.Ptr(itemLabel[item])}
		for _, p := range fys {
			in, r, ok := ix.get(p, item)
			if !ok {
				row = append(row, nil)
				continue
			}
			row = append(row, facts.Ptr(in.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)))
			t.Evidence = append(t.Evidence, r.ID)
		}
		t.Rows = append(t.Rows, row)
	}
	b.S.Tables[section] = append(b.S.Tables[section], t)
	b.S.Charts = append(b.S.Charts, b.finSeries(ix, fys, section))
}

func (b *builder) finSeries(ix finIndex, fys []string, section string) facts.Series {
	s := facts.Series{ID: "fin_revenue_profit", Title: "营业收入与归母净利润（亿元）", Kind: "bar", X: "report_period", Unit: "亿元", Section: section}
	for _, item := range []string{"IS.revenue", "IS.net_profit_parent"} {
		line := facts.SeriesLine{Name: itemLabel[item]}
		for _, p := range fys {
			in, r, ok := ix.get(p, item)
			x := p[:10]
			if !ok {
				line.Points = append(line.Points, [2]*string{&x, nil})
				continue
			}
			v := in.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)
			line.Points = append(line.Points, [2]*string{&x, &v})
			s.Evidence = append(s.Evidence, r.ID)
		}
		s.Lines = append(s.Lines, line)
	}
	return s
}
