package tushare

import (
	"maps"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// TuShare doc 103 defines end_date as the profit distribution period,
// cash_div_tax in pre-tax yuan per share and base_share in 10,000 shares.
const dividendBasis = "TuShare 分红实施记录。本次每十股税前派现按 cash_div_tax × 10 换算，本次现金金额按 cash_div_tax × base_share × 10000 折算。"

func (c *marketCollector) dividends() error {
	rs, err := c.rows("dividend", map[string]string{"ts_code": c.code},
		"ts_code end_date div_proc imp_ann_date record_date pay_date cash_div_tax base_share", 2000, true)
	if err != nil {
		return err
	}
	// The entitlement date distinguishes separate distributions within a
	// period. Its payment date or amount may be amended by a later notice.
	type edition struct {
		row      Row
		conflict bool
	}
	events := map[string]edition{}
	for _, r := range rs {
		v := r.Values
		if v["div_proc"] != "实施" || v["end_date"] < c.from || v["end_date"] > c.until {
			continue
		}
		if v["imp_ann_date"] == "" || v["record_date"] == "" {
			continue
		}
		for _, field := range []string{"end_date", "imp_ann_date", "record_date"} {
			if _, err := marketDate(v[field]); err != nil {
				return marketBad("dividend", field)
			}
		}
		// Date-only notices are public by the end of the announcement day.
		pub := dividendDayEnd(v["imp_ann_date"])
		if v["end_date"] > v["imp_ann_date"] || v["imp_ann_date"] > c.until || pub.After(r.ReceivedAt) {
			continue
		}
		key := v["end_date"] + "|" + v["record_date"]
		prev, seen := events[key]
		switch {
		case !seen || v["imp_ann_date"] > prev.row.Values["imp_ann_date"]:
			events[key] = edition{row: r}
		case v["imp_ann_date"] == prev.row.Values["imp_ann_date"] && !maps.Equal(v, prev.row.Values):
			prev.conflict = true
			events[key] = prev
		}
	}
	for _, key := range slices.Sorted(maps.Keys(events)) {
		event := events[key]
		if event.conflict {
			return marketBad("dividend", "conflicting implementation notices for an entitlement")
		}
		r, ok, err := paidDividend(event.row, c.code, c.until, key)
		if err != nil {
			return err
		}
		if ok {
			c.Records = append(c.Records, r)
		}
	}
	return nil
}

func dividendDayEnd(day string) time.Time {
	d, _ := time.ParseInLocation(marketDay, day, marketZone)
	return d.AddDate(0, 0, 1).Add(-time.Microsecond)
}

func paidDividend(row Row, code, cutoff, eventKey string) (dataservice.Record, bool, error) {
	v := row.Values
	payment, err := marketDate(v["pay_date"])
	if v["pay_date"] == "" || v["cash_div_tax"] == "" || v["base_share"] == "" {
		return dataservice.Record{}, false, nil
	}
	if err != nil || v["pay_date"] < v["record_date"] || v["pay_date"] < v["imp_ann_date"] {
		return dataservice.Record{}, false, marketBad("dividend", "pay_date")
	}
	if v["pay_date"] > cutoff || dividendDayEnd(v["pay_date"]).After(row.ReceivedAt) {
		return dataservice.Record{}, false, nil
	}
	payload := map[string]any{}
	_, err = marketNumbers(row, payload, marketField{"cash_div_tax", "cash_per_10_shares", 1, true},
		marketField{"base_share", "base_shares", 4, true})
	if err != nil {
		return dataservice.Record{}, false, err
	}
	base, _ := decimal.NewFromString(payload["base_shares"].(string))
	per10, _ := decimal.NewFromString(payload["cash_per_10_shares"].(string))
	if base.IsNegative() || per10.IsNegative() || !base.Equal(base.Truncate(0)) {
		return dataservice.Record{}, false, marketBad("dividend", "cash or participating shares")
	}
	if !per10.IsPositive() || !base.IsPositive() {
		return dataservice.Record{}, false, nil
	}
	period, _ := marketDate(v["end_date"])
	announced, _ := marketDate(v["imp_ann_date"])
	payload["fiscal_year"] = period.Format("2006")
	payload["report_period"] = period.Format(time.DateOnly)
	payload["implementation_date"] = announced.Format(time.DateOnly)
	payload["payment_date"] = payment.Format(time.DateOnly)
	payload["total_cash"] = per10.Shift(-1).Mul(base).String()
	record := marketRecord(row, "corp.dividend", code, code+"|"+eventKey, "company_filing", "L2", dividendBasis, &period, payload)
	pub := dividendDayEnd(v["imp_ann_date"])
	record.PublishedAt = &pub
	return record, true, nil
}
