package tushare

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
)

type fundSource struct{ *marketTestSource }

func (s fundSource) Rows(ctx context.Context, api string, p map[string]string, fields []string, size int) ([]Row, error) {
	if api != "fund_portfolio" || p["symbol"] == "" || p["period"] == "" {
		return nil, fmt.Errorf("invalid synthetic fund request")
	}
	rows, err := s.marketTestSource.Rows(ctx, api, map[string]string{"symbol": p["symbol"]}, fields, size)
	return slices.DeleteFunc(rows, func(r Row) bool { return r.Values["end_date"] != p["period"] }), err
}

func TestFundPositionsPreserveFilingIdentity(t *testing.T) {
	src := marketTestData()
	row := map[string]string{"symbol": "600519.SH", "ts_code": "000001.OF", "ann_date": "20261023", "end_date": "20260930", "amount": "150"}
	appendRow := func(changes map[string]string) {
		copy := maps.Clone(row)
		maps.Copy(copy, changes)
		src.data["fund_portfolio"] = append(src.data["fund_portfolio"], copy)
	}
	appendRow(nil)
	appendRow(nil)
	appendRow(map[string]string{"ann_date": "20261024", "amount": "200"})
	appendRow(map[string]string{"ann_date": "20261024", "amount": "300"})
	appendRow(map[string]string{"ts_code": "000002.OF"})
	appendRow(map[string]string{"ts_code": "000002.OF", "ann_date": "20261028", "amount": ""})
	appendRow(map[string]string{"ann_date": "20261215"})
	c := marketCollector{Source: fundSource{src}, ctx: t.Context(), code: row["symbol"], until: "20261120"}
	if err := c.fundPositions(); err != nil {
		t.Fatal(err)
	}
	if len(c.Records) != 4 || len(c.Notes) != 1 {
		t.Fatalf("unexpected selection: %+v", c.Batch)
	}
	for _, r := range c.Records {
		if r.RawRef == "" || r.Period.Format(time.DateOnly) != "2026-09-30" || r.PublishedAt.In(marketZone).Format(time.DateOnly) != r.Payload["announcement_date"] {
			t.Fatal(r)
		}
		if r.Payload["announcement_date"] == "2026-10-24" {
			if r.Payload["observation_state"] != "conflicting" || r.Payload["holding_shares"] != nil {
				t.Fatal(r)
			}
		} else if r.Payload["announcement_date"] == "2026-10-28" {
			if r.Payload["observation_state"] != "undisclosed" || r.Payload["holding_shares"] != nil {
				t.Fatal(r)
			}
		} else if r.Payload["holding_shares"] != "150" {
			t.Fatal(r)
		}
	}
}
