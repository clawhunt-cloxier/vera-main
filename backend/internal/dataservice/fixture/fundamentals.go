package fixture

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type finYear struct {
	revenue, netProfit, netParent, deducted, equity, opCash float64
}

var quarterFrac = map[string]float64{"Q1": 0.22, "H1": 0.47, "Q3": 0.72, "FY": 1}

type period struct {
	typ       string
	end       time.Time
	published time.Time
}

func (g *gen) periods() []period {
	var out []period
	asOf := g.m.AsOf
	for y := 2021; y <= asOf.Year(); y++ {
		for _, p := range []period{
			{"Q1", date(y, 3, 31), at(date(y, 4, 28), 18)},
			{"H1", date(y, 6, 30), at(date(y, 8, 28), 18)},
			{"Q3", date(y, 9, 30), at(date(y, 10, 28), 18)},
			{"FY", date(y, 12, 31), at(date(y+1, 4, 25), 18)},
		} {
			if !p.published.After(asOf) {
				out = append(out, p)
			}
		}
	}
	return out
}

func (g *gen) fundamentals() {
	for _, s := range g.m.Securities {
		p := profileFor(s)
		g.fin[s.Code] = map[string]finYear{}
		if p.noStatements {
			continue
		}
		short := strings.TrimSuffix(s.Code, ".DEMO")
		for _, per := range g.periods() {
			yi := per.end.Year() - 2021
			rev := p.revenue2021
			for i := 1; i <= yi && i < len(p.growth); i++ {
				rev *= 1 + p.growth[i]
			}
			nm := p.netMargin[min(yi, len(p.netMargin)-1)]
			f := quarterFrac[per.typ]
			revenue := rev * f
			net := revenue * nm
			parent := net * 0.96
			deducted := parent * 0.92
			if short == "DM0002" && yi >= 4 {
				deducted = parent * 1.15
			}
			assets := rev * 1.6 * (1 + 0.02*float64(yi))
			liab := assets * 0.45
			if s.IsFinancial {
				assets, liab = rev*22, rev*20.2
			}
			equity := (assets - liab) * 0.95
			ar := rev * 0.18
			if short == "DM0002" {
				ar = rev * (0.18 + 0.06*float64(yi))
			}
			opCash := net * 1.1
			if short == "DM0002" && yi >= 4 {
				opCash = -revenue * 0.03
			}
			goodwill := equity * 0.04
			if short == "DM0002" {
				goodwill = equity * 0.32
			}
			items := map[string]float64{
				"IS.total_revenue": revenue, "IS.revenue": revenue,
				"IS.selling_expense": revenue * 0.05, "IS.admin_expense": revenue * 0.04, "IS.rd_expense": revenue * 0.035,
				"IS.finance_expense": revenue * 0.008, "IS.operating_profit": net * 1.18, "IS.total_profit": net * 1.17,
				"IS.net_profit": net, "IS.net_profit_parent": parent, "IS.net_profit_parent_deducted": deducted,
				"BS.total_assets": assets, "BS.total_liabilities": liab, "BS.equity_parent": equity,
				"BS.cash": assets * 0.12, "BS.accounts_receivable": ar, "BS.notes_receivable": rev * 0.02,
				"BS.short_term_borrowing": liab * 0.15, "BS.long_term_borrowing": liab * 0.2, "BS.bonds_payable": liab * 0.05,
				"BS.current_portion_noncurrent_liab": liab * 0.04,
				"CF.operating_net":                   opCash, "CF.investing_net": -rev * 0.08 * f, "CF.financing_net": -rev * 0.02 * f,
				"CF.capex": rev * 0.07 * f, "CF.dividends_paid": parent * 0.3,
			}
			if !s.IsFinancial {
				items["IS.operating_cost"] = revenue * (1 - p.grossMargin - 0.002*float64(yi))
				items["BS.inventory"] = rev * 0.12
				items["BS.goodwill"] = goodwill
			}
			restated := short == "DM0009" && per.typ == "FY" && per.end.Year() == g.m.AsOf.Year()-1
			for _, code := range slices.Sorted(maps.Keys(items)) {
				v := items[code]
				g.statement(s.Code, per, code, v, 0)
				if restated && strings.HasPrefix(code, "IS.net_profit") {
					g.statement(s.Code, period{per.typ, per.end, per.published.AddDate(0, 0, 20)}, code, v*0.92, 1)
				}
			}
			if per.typ == "FY" {
				g.fin[s.Code][fmt.Sprint(per.end.Year())] = finYear{revenue, net, parent, deducted, equity, opCash}
			}
			if short == "DM0001" && per.typ == "FY" {
				g.segments(s.Code, per, revenue)
			}
		}
		g.valuation(s, p)
	}
}

func (g *gen) statement(code string, per period, item string, v float64, rev int) {
	logical := fmt.Sprintf("%s|%s|%s|consolidated|%s", code, per.end.Format("2006-01-02"), per.typ, item)
	key := fmt.Sprintf("%s|%d", logical, rev)
	pe := per.end
	g.add(dataservice.Record{DatasetID: "fin.statement", EntityCode: code, RecordKey: key, LogicalKey: logical, Version: rev + 1, Period: &pe,
		PublishedAt: dp(per.published), AvailableAt: per.published, Origin: "company_filing", SourceTier: "L2",
		Basis: "合并报表", Payload: map[string]any{
			"report_period": per.end.Format("2006-01-02"), "report_type": per.typ, "scope": "consolidated",
			"item_code": item, "value": dec(v, 2), "revision": rev, "filing_id": fmt.Sprintf("ANN-%s-%s-%s", code, per.end.Format("2006"), per.typ)}})
}

func (g *gen) segments(code string, per period, revenue float64) {
	for _, seg := range []struct {
		dim, name     string
		share, margin float64
	}{{"product", "通用设备", 0.58, 0.31}, {"product", "零部件", 0.30, 0.27}, {"product", "技术服务", 0.12, 0.45},
		{"region", "境内", 0.78, 0.29}, {"region", "境外", 0.22, 0.34}} {
		pe := per.end
		g.add(dataservice.Record{DatasetID: "fin.segment", EntityCode: code,
			RecordKey: fmt.Sprintf("%s|%s|%s|%s", code, per.end.Format("2006"), seg.dim, seg.name), Period: &pe,
			PublishedAt: dp(per.published), AvailableAt: per.published, Origin: "company_filing", SourceTier: "L2",
			Payload: map[string]any{"report_period": per.end.Format("2006-01-02"), "dimension": seg.dim, "segment_name": seg.name,
				"revenue": dec(revenue*seg.share, 2), "cost": dec(revenue*seg.share*(1-seg.margin), 2), "gross_margin": dec(seg.margin, 4)}})
	}
}

// valuation: vendor-style daily multiples from the latest published FY figures.
func (g *gen) valuation(s Security, p profile) {
	if p.noValuation {
		return
	}
	for _, b := range g.bars[s.Code] {
		y := b.day.Year() - 1
		if b.day.Month() < 5 {
			y--
		}
		f, ok := g.fin[s.Code][fmt.Sprint(y)]
		if !ok {
			continue
		}
		mv := b.close * p.totalShares
		payload := map[string]any{"trade_date": b.day.Format("2006-01-02"), "total_mv": dec(mv, 2),
			"float_mv": dec(mv*p.floatRatio, 2), "pe_ttm": nil, "pb_mrq": nil, "ps_ttm": dec(mv/f.revenue, 4),
			"dividend_yield_ttm": dec(f.netParent*0.3/mv, 6)}
		if f.netParent != 0 {
			payload["pe_ttm"] = dec(mv/f.netParent, 4)
		}
		if f.equity > 0 {
			payload["pb_mrq"] = dec(mv/f.equity, 4)
		}
		d := b.day
		g.add(dataservice.Record{DatasetID: "mkt.valuation_daily", EntityCode: s.Code, RecordKey: s.Code + "|" + d.Format("2006-01-02"),
			Period: &d, AvailableAt: at(d, 18), Origin: "vendor_derived", SourceTier: "L2", Basis: "演示厂商估值口径（基于最近年度报告）",
			Payload: payload})
	}
}
