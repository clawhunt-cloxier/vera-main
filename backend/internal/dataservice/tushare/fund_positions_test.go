package tushare

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type fundSource struct{ *marketTestSource }

func (s fundSource) Rows(ctx context.Context, api string, p map[string]string, fields []string, size int) ([]Row, error) {
	if api != "fund_portfolio" {
		return s.marketTestSource.Rows(ctx, api, p, fields, size)
	}
	if p["symbol"] == "" || p["period"] == "" {
		return nil, fmt.Errorf("invalid synthetic fund request")
	}
	rows, err := s.marketTestSource.Rows(ctx, api, map[string]string{"symbol": p["symbol"]}, fields, size)
	return slices.DeleteFunc(rows, func(r Row) bool { return r.Values["end_date"] != p["period"] }), err
}

func TestFundPositionsPreserveFilingIdentity(t *testing.T) {
	src := marketTestData()
	row := map[string]string{"symbol": "600000.SH", "ts_code": "F000001.OF", "ann_date": "20261023", "end_date": "20260930", "amount": "150"}
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
	batch, err := collectMarket(t.Context(), fundSource{src}, row["symbol"], time.Date(2026, time.November, 20, 11, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatal(err)
	}
	var records []dataservice.Record
	for _, record := range batch.Records {
		if record.DatasetID == "fund.disclosed_position" {
			records = append(records, record)
		}
	}
	if len(records) != 4 {
		t.Fatalf("unexpected fund positions: %+v", records)
	}
	seenFundCodes := map[string]bool{}
	for _, r := range records {
		fundCode, ok := r.Payload["fund_code"].(string)
		if r.RawRef == "" || r.Period.Format(time.DateOnly) != "2026-09-30" ||
			r.PublishedAt.In(marketZone).Format(time.DateOnly) != r.Payload["announcement_date"] || !ok || fundCode == "" {
			t.Fatal(r)
		}
		seenFundCodes[fundCode] = true
		switch r.Payload["announcement_date"] {
		case "2026-10-24":
			if r.Payload["observation_state"] != "conflicting" || r.Payload["holding_shares"] != nil {
				t.Fatal(r)
			}
		case "2026-10-28":
			if r.Payload["observation_state"] != "undisclosed" || r.Payload["holding_shares"] != nil {
				t.Fatal(r)
			}
		default:
			if r.Payload["holding_shares"] != "150" {
				t.Fatal(r)
			}
		}
	}
	if !seenFundCodes[row["ts_code"]] || !seenFundCodes["000002.OF"] {
		t.Fatalf("fund codes were not preserved: %v", seenFundCodes)
	}
}

func TestFundPositionsStillRequireFundIdentity(t *testing.T) {
	src := marketTestData()
	src.data["fund_portfolio"] = []map[string]string{{
		"symbol": "600000.SH", "ann_date": "20261023", "end_date": "20260930", "amount": "150",
	}}
	c := marketCollector{Source: fundSource{src}, ctx: t.Context(), code: "600000.SH", until: "20261120"}
	period := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	err := c.fundQuarter(period)
	var providerErr *dataservice.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Class != dataservice.ErrSchemaChanged {
		t.Fatalf("missing fund identity error = %v", err)
	}
}
