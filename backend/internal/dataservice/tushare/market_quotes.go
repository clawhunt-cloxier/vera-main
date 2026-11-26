package tushare

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const marketValFields = "ts_code trade_date turnover_rate pe_ttm pb ps_ttm dv_ttm total_share float_share total_mv circ_mv"

func marketValuation(r Row) (dataservice.Record, bool, error) {
	v := r.Values
	d, err := marketDate(v["trade_date"])
	if err != nil {
		return dataservice.Record{}, false, err
	}
	p := map[string]any{"trade_date": d.Format(time.DateOnly)}
	ok, err := marketNumbers(r, p, marketField{"total_mv", "total_mv", 4, true}, marketField{"circ_mv", "float_mv", 4, true},
		marketField{"pe_ttm", "pe_ttm", 0, false}, marketField{"pb", "pb_mrq", 0, false}, marketField{"ps_ttm", "ps_ttm", 0, false}, marketField{"dv_ttm", "dividend_yield_ttm", -2, false})
	return marketRecord(r, "mkt.valuation_daily", v["ts_code"], v["ts_code"]+"|"+v["trade_date"], "vendor_derived", "L2", marketValuationBasis, &d, p), ok, err
}

func (c *marketCollector) stockHistory() error {
	bars, err := c.stockPrices()
	if err != nil {
		return err
	}
	if len(bars) == 0 {
		return fmt.Errorf("TuShare daily: no closing bars for %s", c.code)
	}
	return c.index("index_daily", marketBenchmark, 3)
}

func (c *marketCollector) checkSuspensions(w map[string]string, bars map[string]Row) error {
	// A full-session suspension is an explicit event. An absent daily bar alone
	// is not a suspension. Intraday halts can coexist with a valid closing bar.
	stops, err := c.rows("suspend_d", w, "ts_code trade_date suspend_type suspend_timing", 4000, true)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range stops {
		v := r.Values
		day := v["trade_date"]
		if v["suspend_type"] != "S" || v["suspend_timing"] != "" || seen[day] {
			continue
		}
		if _, ok := bars[day]; ok {
			return marketBad("suspend_d", "full-session suspension conflicts with daily")
		}
		d, e := marketDate(day)
		if e != nil {
			return e
		}
		// A suspension event has no observed prices. Keep its archived raw
		// response and leave a gap in the canonical OHLC series.
		c.Notes = append(c.Notes, c.code+" "+d.Format(time.DateOnly)+" 全天停牌")
		seen[day] = true
	}
	return nil
}

// stockPrices publishes the canonical daily bars, adjustment factors and
// valuations shared by single-stock research and comparison.
func (c *marketCollector) stockPrices() (map[string]Row, error) {
	c.latestVal = Row{}
	w := c.window(c.code, c.from)
	raw, err := c.rows("daily", w, "ts_code trade_date open high low close pre_close vol amount", 5000, false)
	if err != nil {
		return nil, err
	}
	bars, err := marketUnique(raw, "trade_date")
	if err != nil {
		return nil, err
	}
	if len(bars) == 0 {
		return bars, nil
	}
	if err := c.checkSuspensions(w, bars); err != nil {
		return nil, err
	}
	raw, err = c.rows("adj_factor", w, "ts_code trade_date adj_factor", 5000, false)
	if err != nil {
		return nil, err
	}
	factors, err := marketUnique(raw, "trade_date")
	if err != nil {
		return nil, err
	}
	raw, err = c.rows("daily_basic", w, marketValFields, 5000, false)
	if err != nil {
		return nil, err
	}
	vals, err := marketUnique(raw, "trade_date")
	if err != nil {
		return nil, err
	}
	for _, day := range slices.Sorted(maps.Keys(vals)) {
		r := vals[day]
		rec, ok, err := marketValuation(r)
		if err != nil {
			return nil, err
		}
		if ok {
			c.Records = append(c.Records, rec)
			c.latestVal = r
		}
	}
	joins := marketJoins{}
	var joined []dataservice.Record
	var keys []string
	skipped := 0
	for _, day := range slices.Sorted(maps.Keys(bars)) {
		r := bars[day]
		d, _ := marketDate(day)
		p := map[string]any{"trade_date": d.Format(time.DateOnly), "suspended": false}
		ok, err := marketNumbers(r, p, marketField{"open", "open", 0, true}, marketField{"high", "high", 0, true}, marketField{"low", "low", 0, true},
			marketField{"close", "close", 0, true}, marketField{"pre_close", "pre_close", 0, true}, marketField{"vol", "volume", 2, true}, marketField{"amount", "amount", 3, true})
		if err != nil {
			return nil, err
		}
		if !ok {
			skipped++
			continue
		}
		fr := factors[day]
		ok, err = marketNumbers(fr, p, marketField{"adj_factor", "adj_factor", 0, true})
		if err != nil {
			return nil, err
		}
		if !ok {
			skipped++
			continue
		}
		joins.add(day, "daily", r)
		joins.add(day, "adj_factor", fr)
		available := r.ReceivedAt
		if fr.ReceivedAt.After(available) {
			available = fr.ReceivedAt
		}
		if vr, ok := vals[day]; ok && vr.Values["turnover_rate"] != "" {
			if _, err := marketNumbers(vr, p, marketField{"turnover_rate", "turnover_rate", -2, false}); err != nil {
				return nil, err
			}
			joins.add(day, "daily_basic", vr)
			if vr.ReceivedAt.After(available) {
				available = vr.ReceivedAt
			}
		}
		rec := marketRecord(r, "mkt.daily_bar", c.code, c.code+"|"+day, "exchange", "L2", "TuShare 收盘日线（不复权）及同日复权因子", &d, p)
		rec.AvailableAt = available
		joined = append(joined, rec)
		keys = append(keys, day)
	}
	if skipped != 0 {
		c.Notes = append(c.Notes, fmt.Sprintf("有 %d 个行情日缺少完整价格、成交数据或复权因子", skipped))
	}
	if err := c.archiveJoins(joins, joined, keys); err != nil {
		return nil, err
	}
	return bars, nil
}

func (c *marketCollector) index(api, code string, amountShift int32) error {
	rs, err := c.rows(api, c.window(code, c.from), "ts_code trade_date close amount", 3000, api == "sw_daily")
	if err != nil {
		return err
	}
	byDate, err := marketUnique(rs, "trade_date")
	if err != nil {
		return err
	}
	for _, day := range slices.Sorted(maps.Keys(byDate)) {
		r := byDate[day]
		d, e := marketDate(day)
		if e != nil {
			return e
		}
		p := map[string]any{"trade_date": d.Format(time.DateOnly), "index_code": code}
		ok, err := marketNumbers(r, p, marketField{"close", "close", 0, true}, marketField{"amount", "amount", amountShift, false})
		if err != nil {
			return err
		}
		if ok {
			c.Records = append(c.Records, marketRecord(r, "mkt.index_bar", "", code+"|"+day, "exchange", "L2", "TuShare "+api+" 收盘指数", &d, p))
		}
	}
	return nil
}
