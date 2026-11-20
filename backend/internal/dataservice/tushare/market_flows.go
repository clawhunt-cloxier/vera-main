package tushare

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func (c *marketCollector) flows() error {
	closeDate, _ := marketDate(c.day)
	w := c.window(c.code, closeDate.AddDate(-1, 0, 0).Format(marketDay))
	rows, err := c.rows("margin_detail", w, "ts_code trade_date rzye rzmre rzche rqyl rqye", 3000, true)
	if err != nil {
		return err
	}
	byDate, err := marketUnique(rows, "trade_date")
	if err != nil {
		return err
	}
	for _, day := range slices.Sorted(maps.Keys(byDate)) {
		r := byDate[day]
		d, _ := marketDate(day)
		payload := map[string]any{"trade_date": d.Format(time.DateOnly)}
		ok, err := marketNumbers(r, payload, marketField{"rzye", "financing_balance", 0, true}, marketField{"rzmre", "financing_buy", 0, true},
			marketField{"rzche", "financing_repay", 0, false}, marketField{"rqyl", "short_balance_shares", 0, false}, marketField{"rqye", "short_balance", 0, false})
		if err != nil {
			return err
		}
		if ok {
			c.Records = append(c.Records, marketRecord(r, "flow.margin", c.code, c.code+"|"+day, "exchange", "L2", "TuShare 转提供交易所融资融券明细", &d, payload))
		}
	}
	if err := c.holderCounts(w); err != nil {
		return err
	}
	if err := c.blocks(w); err != nil {
		return err
	}
	return c.orderFlow(w)
}

func (c *marketCollector) holderCounts(window map[string]string) error {
	rows, err := c.rows("stk_holdernumber", window, "ts_code ann_date end_date holder_num", 2000, true)
	if err != nil {
		return err
	}
	// Multiple disclosures for the same statistical date are common. Pick the
	// newest announcement known at the cutoff, retaining its date and raw row.
	latest := map[string]Row{}
	for _, r := range rows {
		period, pub := r.Values["end_date"], r.Values["ann_date"]
		if _, err = marketDate(period); err != nil {
			return err
		}
		if _, err = marketDate(pub); err != nil {
			return err
		}
		if pub > c.day || period > pub {
			continue
		}
		prev, ok := latest[period]
		if ok && pub == prev.Values["ann_date"] && r.Values["holder_num"] != prev.Values["holder_num"] {
			return marketBad("stk_holdernumber", "duplicate disclosure")
		}
		if !ok || pub > prev.Values["ann_date"] {
			latest[period] = r
		}
	}
	for _, period := range slices.Sorted(maps.Keys(latest)) {
		r := latest[period]
		s := r.Values["holder_num"]
		if s == "" {
			continue
		}
		n, err := decimal.NewFromString(s)
		if err != nil || n.Exponent() > 18 || !n.IsPositive() || n.GreaterThan(decimal.NewFromInt(1_000_000_000)) || !n.Equal(n.Truncate(0)) {
			return marketBad("stk_holdernumber", "holder_num")
		}
		d, _ := marketDate(period)
		pub, _ := time.ParseInLocation(marketDay, r.Values["ann_date"], marketZone)
		// PostgreSQL stores timestamps to microseconds. Using its exact
		// precision keeps repeated observations of the same filing identical.
		pub = pub.AddDate(0, 0, 1).Add(-time.Microsecond)
		// Date-only publication is conservative even if the vendor exposes a
		// same-day row before the end of that day.
		if r.ReceivedAt.Before(pub) {
			continue
		}
		rec := marketRecord(r, "corp.holder", c.code, c.code+"|"+period+"|count", "company_filing", "L2", "TuShare 整理的股东户数（按公告日）", &d, map[string]any{"period_end": d.Format(time.DateOnly), "rank": 0, "holder_kind": "count", "holder_count": n.IntPart()})
		rec.PublishedAt = &pub
		c.Records = append(c.Records, rec)
	}
	return nil
}

func (c *marketCollector) orderFlow(window map[string]string) error {
	rows, err := c.rows("moneyflow", window, "ts_code trade_date buy_sm_amount sell_sm_amount buy_md_amount sell_md_amount buy_lg_amount sell_lg_amount buy_elg_amount sell_elg_amount", 3000, true)
	if err != nil {
		return err
	}
	days, err := marketUnique(rows, "trade_date")
	if err != nil {
		return err
	}
	for _, day := range slices.Sorted(maps.Keys(days)) {
		r := days[day]
		d, _ := marketDate(day)
		payload := map[string]any{"trade_date": d.Format(time.DateOnly), "method_id": "tushare.moneyflow.v1"}
		for _, tier := range []struct{ field, name string }{{"sm", "s_net"}, {"md", "m_net"}, {"lg", "l_net"}, {"elg", "xl_net"}} {
			buy, sell := r.Values["buy_"+tier.field+"_amount"], r.Values["sell_"+tier.field+"_amount"]
			if buy == "" || sell == "" {
				continue
			}
			b, err := decimal.NewFromString(buy)
			if err != nil || b.Exponent() > 18 || b.Exponent() < -30 {
				return marketBad("moneyflow", "buy_amount")
			}
			s, err := decimal.NewFromString(sell)
			if err != nil || s.Exponent() > 18 || s.Exponent() < -30 {
				return marketBad("moneyflow", "sell_amount")
			}
			payload[tier.name] = b.Sub(s).Shift(4).String()
		}
		// The existing fact builder uses every tier; a partial row cannot
		// support the registered comparison.
		if len(payload) == 6 {
			c.Records = append(c.Records, marketRecord(r, "flow.order_size", c.code, c.code+"|"+day+"|tushare.moneyflow.v1", "vendor_derived", "L4", marketOrderBasis, &d, payload))
		}
	}
	return nil
}

func (c *marketCollector) blocks(window map[string]string) error {
	rows, err := c.rows("block_trade", window, "ts_code trade_date price vol amount buyer seller", 800, true)
	if err != nil {
		return err
	}
	// Archive order can change on refetch. Sequence is a reproducible
	// enumeration, not execution time or exchange trade ID.
	sortKey := func(r Row) string {
		return strings.Join([]string{r.Values["trade_date"], r.Values["price"], r.Values["vol"], r.Values["amount"], r.Values["buyer"], r.Values["seller"]}, "|")
	}
	slices.SortStableFunc(rows, func(a, b Row) int { return cmp.Compare(sortKey(a), sortKey(b)) })
	seq := map[string]int{}
	count := map[string]int{}
	for _, r := range rows {
		v := r.Values
		day := v["trade_date"]
		d, _ := marketDate(day)
		p := map[string]any{"trade_date": d.Format(time.DateOnly), "buyer_seat": v["buyer"], "seller_seat": v["seller"]}
		ok, err := marketNumbers(r, p, marketField{"price", "price", 0, true}, marketField{"vol", "volume", 4, true}, marketField{"amount", "amount", 4, true})
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// The documentation labels volume in 10k shares; examples express
		// consideration in 10k yuan. Verify the units against price * volume,
		// allowing published rounding to 0.01 in each 10k-unit field.
		price, _ := decimal.NewFromString(v["price"])
		vol, _ := decimal.NewFromString(v["vol"])
		amount, _ := decimal.NewFromString(v["amount"])
		tolerance := price.Add(decimal.NewFromInt(1)).Mul(decimal.New(5, -3))
		if !price.IsPositive() || !vol.IsPositive() || !amount.IsPositive() || price.Mul(vol).Sub(amount).Abs().GreaterThan(tolerance) {
			return marketBad("block_trade", "volume/amount units")
		}
		seq[day]++
		p["seq"] = seq[day]
		key := sortKey(r)
		count[key]++
		c.Records = append(c.Records, marketRecord(r, "flow.block_trade", c.code, fmt.Sprintf("%s|%s|%s", c.code, key, strconv.Itoa(count[key])), "exchange", "L2", "TuShare 交易所大宗成交，日内无时间顺序", &d, p))
	}
	return nil
}
