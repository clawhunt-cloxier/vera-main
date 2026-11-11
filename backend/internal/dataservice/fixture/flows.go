package fixture

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const orderSizeMethod = "demo-vendor-os-v1"

func (g *gen) flows() {
	window := 260
	for _, s := range g.m.Securities {
		p := profileFor(s)
		bars := g.bars[s.Code]
		short := strings.TrimSuffix(s.Code, ".DEMO")
		start := max(0, len(bars)-window)
		r := rngFor(g.m.Seed, "flows|"+s.Code)
		balance := p.totalShares * p.floatRatio * p.price * 0.03
		for i := start; i < len(bars); i++ {
			b := bars[i]
			next := g.days[min(g.dayIndex(b.day)+1, len(g.days)-1)]
			if s.MarginEligible && !p.noFlows {
				trend := 0.0
				if short == "DM0001" && i > len(bars)-25 {
					trend = 0.006
				}
				buy := b.amount * (0.08 + 0.02*r.Float64())
				balance = math.Max(0, balance*(1+trend+0.004*r.NormFloat64()))
				g.add(dataservice.Record{DatasetID: "flow.margin", EntityCode: s.Code, RecordKey: s.Code + "|" + b.day.Format("2006-01-02"),
					Period: dp(b.day), AvailableAt: at(next, 9), Origin: "exchange", SourceTier: "L2", Payload: map[string]any{
						"trade_date": b.day.Format("2006-01-02"), "financing_balance": dec(balance, 2), "financing_buy": dec(buy, 2),
						"financing_repay": dec(buy*(0.97+0.06*r.Float64()), 2), "short_balance_shares": dec(p.totalShares*0.0004, 0),
						"short_balance": dec(p.totalShares*0.0004*b.close, 2)}})
			}
			if !p.noFlows && !b.suspended {
				net := b.amount * 0.03 * r.NormFloat64()
				g.add(dataservice.Record{DatasetID: "flow.order_size", EntityCode: s.Code, RecordKey: s.Code + "|" + b.day.Format("2006-01-02") + "|" + orderSizeMethod,
					Period: dp(b.day), AvailableAt: at(b.day, 18), Origin: "vendor_derived", SourceTier: "L4", Basis: orderSizeMethod,
					Payload: map[string]any{"trade_date": b.day.Format("2006-01-02"), "method_id": orderSizeMethod,
						"xl_net": dec(net*0.5, 2), "l_net": dec(net*0.3, 2), "m_net": dec(-net*0.3, 2), "s_net": dec(-net*0.5, 2)}})
			}
		}
		if p.noFlows {
			continue
		}
		g.dragonTiger(s, bars)
		g.blockTrades(s, bars)
		g.quarterlyHoldings(s, p)
	}
}

func (g *gen) dragonTiger(s Security, bars []bar) {
	short := strings.TrimSuffix(s.Code, ".DEMO")
	if short != "DM0001" && short != "DM0007" {
		return
	}
	for _, back := range []int{3, 11} {
		b := bars[len(bars)-back]
		key := b.day.Format("2006-01-02")
		seats := []struct {
			side, name, typ string
			buy, sell       float64
		}{{"buy", "机构专用", "institution", 0.021, 0.002}, {"buy", "演示证券上海某路营业部", "branch", 0.012, 0.001},
			{"buy", "沪股通专用", "connect", 0.009, 0.004}, {"sell", "机构专用", "institution", 0.001, 0.015},
			{"sell", "演示证券深圳某路营业部", "branch", 0.0005, 0.011}}
		for i, st := range seats {
			g.add(dataservice.Record{DatasetID: "flow.dragon_tiger", EntityCode: s.Code,
				RecordKey: fmt.Sprintf("%s|%s|R01|%s|%d", s.Code, key, st.side, i+1), Period: dp(b.day), AvailableAt: at(b.day, 19),
				Origin: "exchange", SourceTier: "L2", Payload: map[string]any{"trade_date": key, "reason_code": "R01",
					"reason_text": "日涨幅偏离值达到 7% 的证券", "side": st.side, "rank": i%3 + 1, "seat_name": st.name, "seat_type": st.typ,
					"buy_amount": dec(b.amount*st.buy, 2), "sell_amount": dec(b.amount*st.sell, 2)}})
		}
	}
}

func (g *gen) blockTrades(s Security, bars []bar) {
	r := rngFor(g.m.Seed, "block|"+s.Code)
	for _, back := range []int{2, 9, 17, 41} {
		if back >= len(bars) || bars[len(bars)-back].suspended {
			continue
		}
		b := bars[len(bars)-back]
		price := round2(b.close * (0.94 + 0.05*r.Float64()))
		vol := math.Round(b.volume*0.05/100) * 100
		g.add(dataservice.Record{DatasetID: "flow.block_trade", EntityCode: s.Code, RecordKey: fmt.Sprintf("%s|%s|1", s.Code, b.day.Format("2006-01-02")),
			Period: dp(b.day), AvailableAt: at(b.day, 19), Origin: "exchange", SourceTier: "L2", Payload: map[string]any{
				"trade_date": b.day.Format("2006-01-02"), "seq": 1, "price": dec(price, 2), "volume": dec(vol, 0), "amount": dec(price*vol, 2),
				"buyer_seat": "机构专用", "seller_seat": "演示证券北京某路营业部"}})
	}
}

// quarterEnds returns the last n quarter-end dates strictly before asOf, ascending.
func quarterEnds(asOf time.Time, n int) []time.Time {
	var out []time.Time
	d := date(asOf.Year(), time.Month((int(asOf.Month())-1)/3*3+1), 1).AddDate(0, 0, -1)
	if !d.Before(asOf) {
		d = date(d.Year(), d.Month()-2, 1).AddDate(0, 0, -1)
	}
	for len(out) < n {
		out = append([]time.Time{d}, out...)
		d = date(d.Year(), d.Month()-2, 1).AddDate(0, 0, -1)
	}
	return out
}

func (g *gen) quarterlyHoldings(s Security, p profile) {
	r := rngFor(g.m.Seed, "holdings|"+s.Code)
	float := p.totalShares * p.floatRatio
	connect, funds, holders := float*0.04, float*0.06, 52000.0+float/2e5
	for _, qe := range quarterEnds(g.m.AsOf, 6) {
		avail := at(qe.AddDate(0, 0, 7), 9)
		pub := at(qe.AddDate(0, 1, 0), 18)
		connect *= 1 + 0.08*r.NormFloat64()
		funds *= 1 + 0.10*r.NormFloat64()
		holders *= 1 + 0.06*r.NormFloat64()
		k := qe.Format("2006-01-02")
		g.add(dataservice.Record{DatasetID: "flow.connect_holding", EntityCode: s.Code, RecordKey: s.Code + "|" + k, Period: dp(qe),
			AvailableAt: avail, Origin: "exchange", SourceTier: "L2", Payload: map[string]any{"period_end": k,
				"holding_shares": dec(connect, 0), "holding_ratio": dec(connect/p.totalShares, 6)}})
		basis := "top10_quarterly"
		if qe.Month() == 6 || qe.Month() == 12 {
			basis = "full_semiannual"
		}
		g.add(dataservice.Record{DatasetID: "fund.holding", EntityCode: s.Code, RecordKey: s.Code + "|" + k, Period: dp(qe),
			AvailableAt: pub, Origin: "vendor_derived", SourceTier: "L2", Payload: map[string]any{"period_end": k,
				"fund_count": int(40 + 30*r.Float64()), "holding_shares": dec(funds, 0), "holding_ratio_float": dec(funds/float, 6),
				"disclosure_basis": basis}})
		g.add(dataservice.Record{DatasetID: "corp.holder", EntityCode: s.Code, RecordKey: s.Code + "|" + k + "|count|0", Period: dp(qe),
			AvailableAt: pub, Origin: "company_filing", SourceTier: "L2", Payload: map[string]any{"period_end": k,
				"holder_kind": "count", "rank": 0, "holder_count": int(holders)}})
	}
	g.corpEvents(s, p)
}
