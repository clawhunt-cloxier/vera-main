package fixture

import (
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// profile holds per-security generation parameters derived from the manifest profile text.
type profile struct {
	listedDaysAgo int     // 0 = listed before history start
	price         float64 // starting price
	drift, vol    float64 // daily
	totalShares   float64
	floatRatio    float64
	revenue2021   float64 // annual revenue, yuan
	growth        []float64
	grossMargin   float64
	netMargin     []float64 // per year from 2021
	noValuation   bool
	noFlows       bool
	noStatements  bool
	suspendLast   int // suspended trading days near the end
}

func profileFor(s Security) profile {
	p := profile{price: 18, drift: 0.0003, vol: 0.018, totalShares: 1.2e9, floatRatio: 0.85,
		revenue2021: 6.0e9, growth: []float64{0.12, 0.10, 0.08, 0.15, 0.33, 0.10}, grossMargin: 0.28,
		netMargin: []float64{0.09, 0.095, 0.10, 0.10, 0.10, 0.10}}
	switch strings.TrimSuffix(s.Code, ".DEMO") {
	case "DM0002":
		p.price, p.drift = 9, -0.0004
		p.growth = []float64{0.05, 0.02, -0.03, -0.08, -0.12, -0.05}
		p.netMargin = []float64{0.05, 0.04, 0.02, 0.01, -0.06, -0.04}
		p.grossMargin = 0.16
	case "DM0003":
		p.noValuation, p.noFlows = true, true
		p.price, p.totalShares = 12, 4.0e8
		p.revenue2021 = 1.5e9
	case "DM0004", "DM0012":
		p.price, p.vol, p.totalShares = 6, 0.009, 1.8e10
		p.revenue2021, p.grossMargin = 4.5e10, 0
		p.netMargin = []float64{0.33, 0.34, 0.34, 0.35, 0.35, 0.35}
		p.growth = []float64{0.04, 0.03, 0.02, 0.02, 0.01, 0.02}
	case "DM0005":
		p.listedDaysAgo, p.price, p.vol, p.totalShares = 40, 32, 0.035, 2.0e8
		p.revenue2021 = 8.0e8
		p.suspendLast = 5
	case "DM0006":
		p.noStatements = true
		p.price, p.totalShares = 7, 3.0e8
	}
	h := 0.0
	for _, c := range s.Code {
		h += float64(c)
	}
	p.price *= 1 + math.Mod(h, 7)/20
	p.revenue2021 *= 1 + math.Mod(h, 5)/10
	return p
}

func (g *gen) securities() {
	for _, s := range g.m.Securities {
		p := profileFor(s)
		code := s.Code
		listIdx := 0
		if p.listedDaysAgo > 0 {
			listIdx = len(g.days) - p.listedDaysAgo
		}
		listDate := g.days[listIdx]
		status := "listed"
		if p.suspendLast > 0 {
			status = "listed"
		}
		g.add(dataservice.Record{DatasetID: "ref.security", EntityCode: code, RecordKey: code, AvailableAt: at(g.days[0], 0),
			Origin: "exchange", SourceTier: "L2", Payload: map[string]any{
				"security_code": code, "short_name": s.Name, "exchange": "DEMO", "board": "main",
				"list_date": listDate.Format("2006-01-02"), "status": status, "is_financial": s.IsFinancial,
				"margin_eligible": s.MarginEligible, "total_shares": dec(p.totalShares, 0), "float_shares": dec(p.totalShares*p.floatRatio, 0)}})
		g.bars[code] = g.walk(code, p, listIdx)
		for _, b := range g.bars[code] {
			g.add(dataservice.Record{DatasetID: "mkt.daily_bar", EntityCode: code, RecordKey: code + "|" + b.day.Format("2006-01-02"),
				Period: dp(b.day), AvailableAt: at(b.day, 17), Origin: "exchange", SourceTier: "L2", Payload: map[string]any{
					"trade_date": b.day.Format("2006-01-02"), "open": dec(b.open, 2), "high": dec(b.high, 2), "low": dec(b.low, 2),
					"close": dec(b.close, 2), "pre_close": dec(b.preClose, 2), "volume": dec(b.volume, 0), "amount": dec(b.amount, 2),
					"turnover_rate": dec(b.volume/(p.totalShares*p.floatRatio), 6), "adj_factor": "1.000000", "suspended": b.suspended}})
		}
	}
}

func (g *gen) walk(code string, p profile, from int) []bar {
	r := rngFor(g.m.Seed, "bars|"+code)
	price := p.price
	out := make([]bar, 0, len(g.days)-from)
	for i := from; i < len(g.days); i++ {
		d := g.days[i]
		pre := price
		suspended := p.suspendLast > 0 && i >= len(g.days)-p.suspendLast-3 && i < len(g.days)-3
		if !suspended {
			ret := p.drift + p.vol*r.NormFloat64()
			ret = math.Max(-0.095, math.Min(0.095, ret))
			price = round2(math.Max(1, price*(1+ret)))
		}
		hi := math.Max(pre, price) * (1 + math.Abs(r.NormFloat64())*0.004)
		lo := math.Min(pre, price) * (1 - math.Abs(r.NormFloat64())*0.004)
		vol := 0.0
		if !suspended {
			vol = math.Round(p.totalShares*p.floatRatio*(0.006+0.004*r.Float64())/100) * 100
		}
		out = append(out, bar{day: d, open: round2(pre), high: round2(hi), low: round2(lo), close: price, preClose: pre,
			volume: vol, amount: round2(vol * (price + pre) / 2), suspended: suspended})
	}
	return out
}

// indexes: a fictional broad index (DM300.DEMO) and one index per industry, built from member returns.
func (g *gen) indexes() {
	level := map[string]float64{"DM300.DEMO": 3500}
	members := map[string][]string{"DM300.DEMO": nil}
	for _, s := range g.m.Securities {
		members["DM300.DEMO"] = append(members["DM300.DEMO"], s.Code)
	}
	for _, ind := range g.m.Industries {
		code := "IDX-" + ind.ID
		level[code] = 1000
		for _, mcode := range ind.Members {
			members[code] = append(members[code], mcode+".DEMO")
		}
	}
	byDay := map[string]map[string]bar{}
	for code, bars := range g.bars {
		byDay[code] = map[string]bar{}
		for _, b := range bars {
			byDay[code][b.day.Format("2006-01-02")] = b
		}
	}
	for _, d := range g.days {
		key := d.Format("2006-01-02")
		for _, idx := range slices.Sorted(maps.Keys(members)) {
			ms := members[idx]
			sum, n, amount := 0.0, 0, 0.0
			for _, c := range ms {
				if b, ok := byDay[c][key]; ok && !b.suspended && b.preClose > 0 {
					sum += b.close/b.preClose - 1
					amount += b.amount
					n++
				}
			}
			if n > 0 {
				level[idx] *= 1 + sum/float64(n)
			}
			g.add(dataservice.Record{DatasetID: "mkt.index_bar", RecordKey: idx + "|" + key, Period: dp(d), AvailableAt: at(d, 17),
				Origin: "exchange", SourceTier: "L2", Payload: map[string]any{"index_code": idx, "trade_date": key,
					"close": dec(level[idx], 2), "amount": dec(amount, 2)}})
		}
	}
}
