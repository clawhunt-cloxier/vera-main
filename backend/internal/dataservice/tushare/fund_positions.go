package tushare

import (
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// fund_portfolio identifies a filing by fund code, subject, quarter and
// announcement date. It doesn't identify a common portfolio across share classes.
func (c *marketCollector) fundPositions() error {
	asOf, err := marketDate(c.until)
	if err != nil {
		return err
	}
	firstMonth := time.Month(1 + 3*((int(asOf.Month())-1)/3))
	start := time.Date(asOf.Year(), firstMonth, 1, 0, 0, 0, 0, time.UTC)
	if asOf.Equal(start.AddDate(0, 3, -1)) {
		start = start.AddDate(0, 3, 0)
	}
	for range 3 {
		period := start.AddDate(0, 0, -1)
		start = start.AddDate(0, -3, 0)
		err := c.fundQuarter(period)
		var pe *dataservice.ProviderError
		if errors.As(err, &pe) {
			switch pe.Class {
			case dataservice.ErrEmptyValid:
				continue
			case dataservice.ErrRateLimited, dataservice.ErrNotCovered, dataservice.ErrUpstreamUnavailable:
				c.Notes = append(c.Notes, "TuShare 基金持仓披露覆盖不完整")
				return nil
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *marketCollector) fundQuarter(period time.Time) error {
	const api = "fund_portfolio"
	params := map[string]string{"symbol": c.code, "period": period.Format(marketDay)}
	rows, err := c.rows(api, params, "ts_code symbol end_date ann_date amount", 1000, false)
	if err != nil {
		return err
	}
	byKey := map[string]dataservice.Record{}
	conflicts := map[string]bool{}
	for _, row := range rows {
		v := row.Values
		fund := v["ts_code"]
		// The dataset contract treats the vendor's fund code as an opaque string.
		// Preserve it verbatim instead of assuming a specific exchange-code format.
		if v["symbol"] != c.code || v["end_date"] != params["period"] || fund == "" {
			return marketBad(api, "fund or position identity")
		}
		if v["ann_date"] == "" {
			continue
		}
		pub, err := time.ParseInLocation(marketDay, v["ann_date"], marketZone)
		if err != nil || v["ann_date"] < params["period"] {
			return marketBad(api, "ann_date")
		}
		if v["ann_date"] > c.until {
			continue
		}
		key := c.code + "|" + fund + "|" + v["end_date"] + "|" + v["ann_date"]
		rec := marketRecord(row, "fund.disclosed_position", c.code, key, "company_filing", "L2",
			"TuShare 基金定期披露，按单个基金代码及公告日期列示", &period, map[string]any{
				"fund_code": fund, "period_end": period.Format(time.DateOnly), "announcement_date": pub.Format(time.DateOnly), "observation_state": "undisclosed",
			})
		if v["amount"] != "" {
			shares, err := decimal.NewFromString(v["amount"])
			if err != nil || shares.Exponent() > 18 || shares.Exponent() < -30 || shares.IsNegative() {
				return marketBad(api, "amount")
			}
			rec.Payload["holding_shares"] = shares.String()
			rec.Payload["observation_state"] = "disclosed"
		}
		// The provider gives a date, not an intraday publication timestamp.
		// The archived receipt already bounds actual visibility on that date.
		pub = pub.AddDate(0, 0, 1).Add(-time.Microsecond)
		rec.PublishedAt = &pub
		if prev, ok := byKey[key]; ok && prev.Payload["holding_shares"] != rec.Payload["holding_shares"] {
			conflicts[key] = true
		}
		byKey[key] = rec
	}
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		rec := byKey[key]
		if conflicts[key] {
			rec.Payload["observation_state"] = "conflicting"
			delete(rec.Payload, "holding_shares")
		}
		c.Records = append(c.Records, rec)
	}
	if len(conflicts) != 0 {
		c.Notes = append(c.Notes, "同一基金代码在同一公告日有多条不同持股数，已标记冲突")
	}
	return nil
}
