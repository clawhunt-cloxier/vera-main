package tushare

import (
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

func dividendData(changes map[string]string) map[string]string {
	v := map[string]string{"ts_code": "600000.SH", "end_date": "20251231", "div_proc": "实施",
		"imp_ann_date": "20260420", "record_date": "20260521", "pay_date": "20260522", "cash_div_tax": "0.245678", "base_share": "10.1234"}
	maps.Copy(v, changes)
	return v
}

func TestDividendImplementedEvents(t *testing.T) {
	src := marketTestData()
	src.data["dividend"] = []map[string]string{
		dividendData(nil), dividendData(nil),
		dividendData(map[string]string{"end_date": "20250630", "imp_ann_date": "20250901", "record_date": "20250918", "pay_date": "20250919", "base_share": "9.8765"}),
		dividendData(map[string]string{"div_proc": "预案", "cash_div_tax": "30"}),
		dividendData(map[string]string{"record_date": "20260610", "pay_date": "20260611", "cash_div_tax": "0"}),
		dividendData(map[string]string{"record_date": "20261120", "pay_date": "20261120"}),
		dividendData(map[string]string{"record_date": "20260801", "pay_date": "20260802", "base_share": ""}),
		dividendData(map[string]string{"record_date": "20260803", "pay_date": "20260804", "cash_div_tax": "", "cash_div": "0.2"}),
		dividendData(map[string]string{"imp_ann_date": "", "record_date": "20260710", "pay_date": "20260711"}),
		dividendData(map[string]string{"end_date": "20051231", "record_date": "20160510", "pay_date": "20160511"}),
		dividendData(map[string]string{"imp_ann_date": "20260421", "pay_date": "20260523", "cash_div_tax": "0.25555"}),
	}
	c := marketCollector{ctx: t.Context(), Source: src, code: "600000.SH", from: "20211101", until: "20261120"}
	if err := c.dividends(); err != nil || len(c.Records) != 2 {
		t.Fatalf("events %d, %v", len(c.Records), err)
	}
	interim, final := c.Records[0], c.Records[1]
	if interim.Payload["report_period"] != "2025-06-30" || interim.Payload["fiscal_year"] != "2025" || interim.Payload["base_shares"] != "98765" ||
		final.Payload["cash_per_10_shares"] != "2.5555" || final.Payload["total_cash"] != "25870.3487" ||
		final.Payload["base_shares"] != "101234" || final.Payload["payment_date"] != "2026-05-23" || final.Payload["implementation_date"] != "2026-04-21" {
		t.Fatalf("unverified conversion or revision: %v %v", interim.Payload, final.Payload)
	}
	expectedPub := time.Date(2026, 4, 21, 23, 59, 59, 999999000, marketZone)
	if final.PublishedAt == nil || !final.PublishedAt.Equal(expectedPub) || final.Period.Format(time.DateOnly) != "2025-12-31" ||
		final.RawRef != "synthetic/dividend" || final.RawSHA256 == "" || final.RawLocator != "10" || interim.RecordKey == final.RecordKey {
		t.Fatalf("lost period, event identity or provenance: %+v", final)
	}
}

func TestDividendRejectsConflictsAndHonorsEntitlement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    []map[string]string
		class   dataservice.ErrClass
		wantErr bool
	}{
		{"same edition different cash", []map[string]string{dividendData(nil), dividendData(map[string]string{"cash_div_tax": "0.22"})}, "", true},
		{"fractional participating share", []map[string]string{dividendData(map[string]string{"base_share": "2.12345"})}, "", true},
		{"wrong date order", []map[string]string{dividendData(map[string]string{"pay_date": "20260520"})}, "", true},
		{"denied", nil, dataservice.ErrNotCovered, false},
		{"global authentication failure", nil, dataservice.ErrAuthFailed, true},
		{"revised to unconfirmed new payment", []map[string]string{dividendData(nil), dividendData(map[string]string{"imp_ann_date": "20260501", "pay_date": ""})}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := marketTestData()
			src.data["dividend"] = tc.rows
			if tc.class != "" {
				src.fail["dividend"] = &dataservice.ProviderError{Class: tc.class, Err: errors.New("test")}
			}
			c := marketCollector{ctx: t.Context(), Source: src, code: "600000.SH", from: "20211101", until: "20261120"}
			err := c.dividends()
			if (err != nil) != tc.wantErr || len(c.Records) != 0 {
				t.Fatalf("got rows=%d, err=%v", len(c.Records), err)
			}
		})
	}
}
