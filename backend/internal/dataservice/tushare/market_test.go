package tushare

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// Explicitly synthetic cells shaped as documented by TuShare. No vendor account
// data is needed to exercise portfolio selection, joins or exact unit conversion.
type marketTestSource struct {
	data    map[string][]map[string]string
	objects map[string][]byte
	fail    map[string]error
}

func (s *marketTestSource) Archive(_ context.Context, kind string, data []byte) (string, string, error) {
	h := fmt.Sprintf("%x", sha256.Sum256(data))
	k := kind + "/" + h
	s.objects[k] = data
	return k, h, nil
}
func (s *marketTestSource) Rows(_ context.Context, api string, params map[string]string, fields []string, _ int) ([]Row, error) {
	if err := s.fail[api]; err != nil {
		return nil, err
	}
	var rows []Row
	for i, v := range s.data[api] {
		ok := true
		for k, val := range params {
			if k == "start_date" || k == "end_date" {
				continue
			}
			if v[k] != val {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		pick := map[string]string{}
		for _, f := range fields {
			if val := v[f]; val != "" {
				pick[f] = val
			}
		}
		rows = append(rows, Row{Values: pick, RawRef: "synthetic/" + api, RawSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(api))), RawLocator: fmt.Sprint(i), ReceivedAt: time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)})
	}
	return rows, nil
}

func marketTestData() *marketTestSource {
	data := map[string][]map[string]string{}
	add := func(api string, v map[string]string) { data[api] = append(data[api], v) }
	const day = "20261120"
	for i := 16; i <= 20; i++ {
		add("trade_cal", map[string]string{"exchange": "SSE", "cal_date": fmt.Sprintf("202611%d", i), "is_open": "1"})
	}
	for i := 0; i < 11; i++ {
		code := fmt.Sprintf("600%03d.SH", i)
		add("index_member_all", map[string]string{"ts_code": code, "l1_code": "801010.SI", "l1_name": "模拟一级", "l2_code": "801011.SI", "l2_name": "模拟二级", "in_date": "20211101", "is_new": "Y"})
		add("stock_basic", map[string]string{"ts_code": code, "list_status": "L", "name": "合成主体" + strconv.Itoa(i), "exchange": "SSE", "market": "主板", "list_date": "20100101"})
		v := map[string]string{"ts_code": code, "trade_date": day, "total_mv": strconv.Itoa(100 + i), "circ_mv": "75.34", "turnover_rate": "2.6789", "total_share": "1234.56", "float_share": "999", "pb": "2.05"}
		add("daily_basic", v)
	}
	subj := data["index_member_all"][0]["ts_code"]
	add("index_member_all", map[string]string{"ts_code": subj, "l1_code": "801020.SI", "l1_name": "旧模拟一级", "l2_code": "801021.SI", "l2_name": "旧模拟二级", "in_date": "20150101", "out_date": "20211101", "is_new": "N"})
	bar := map[string]string{"ts_code": subj, "trade_date": day, "open": "12.2", "high": "13", "low": "11.8", "close": "12.34", "pre_close": "12", "vol": "123.45", "amount": "15.13"}
	add("daily", bar)
	missing := maps.Clone(bar)
	missing["trade_date"] = "20261117"
	add("daily", missing)
	add("adj_factor", map[string]string{"ts_code": subj, "trade_date": day, "adj_factor": "1.45"})
	add("suspend_d", map[string]string{"ts_code": subj, "trade_date": "20261118", "suspend_type": "S"})
	for i, api := range []string{"index_daily", "sw_daily"} {
		add(api, map[string]string{"ts_code": []string{"000300.SH", "801011.SI"}[i], "trade_date": day, "close": "3564.88", "amount": "1.23"})
	}
	add("margin_secs", map[string]string{"trade_date": day, "ts_code": subj, "exchange": "SSE"})
	add("margin_detail", map[string]string{"ts_code": subj, "trade_date": "20261119", "rzye": "25e7", "rzmre": "213.14", "rqyl": "210.0"})
	add("stk_holdernumber", map[string]string{"ts_code": subj, "end_date": "20260930", "ann_date": "20261030", "holder_num": "25666"})
	add("block_trade", map[string]string{"ts_code": subj, "trade_date": day, "price": "86.95", "vol": "9.49", "amount": "825.16", "buyer": "甲", "seller": "乙"})
	f := map[string]string{"ts_code": subj, "trade_date": day}
	for _, n := range []string{"elg", "lg", "md", "sm"} {
		f["buy_"+n+"_amount"] = "5.66"
		f["sell_"+n+"_amount"] = "6.12"
	}
	add("moneyflow", f)
	return &marketTestSource{data: data, objects: map[string][]byte{}, fail: map[string]error{}}
}

func TestMarketClosingResearch(t *testing.T) {
	src := marketTestData()
	const code = "600000.SH"
	b, err := collectMarket(t.Context(), src, code, time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Peers) != 8 || b.Peers[0] != "600001.SH" || b.Peers[7] != "600008.SH" || len(b.Entities) != 10 {
		t.Fatalf("bad peer universe %v", b.Peers)
	}
	lookup := func(dataset, code string, test func(map[string]any) bool) dataservice.Record {
		t.Helper()
		for _, r := range b.Records {
			if r.DatasetID == dataset && r.EntityCode == code && test(r.Payload) {
				return r
			}
		}
		t.Fatalf("missing %s %s", dataset, code)
		return dataservice.Record{}
	}
	anyRow := func(map[string]any) bool { return true }
	bar := lookup("mkt.daily_bar", code, func(v map[string]any) bool { return v["trade_date"] == "2026-11-20" })
	if bar.Payload["volume"] != "12345" || bar.Payload["amount"] != "15130" || bar.Payload["turnover_rate"] != "0.026789" || bar.Payload["adj_factor"] != "1.45" {
		t.Fatal(bar.Payload)
	}
	var j marketJoins
	if err := json.Unmarshal(src.objects[bar.RawRef], &j); err != nil || len(j["20261120"]) != 3 {
		t.Fatalf("missing join evidence %+v %v", j, err)
	}
	if !slices.Contains(b.Notes, code+" 2026-11-18 全天停牌") {
		t.Fatal("lost suspension notice")
	}
	catalog, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Records {
		if err := catalog.Validate(r); err != nil {
			t.Fatal(err)
		}
		if r.DatasetID == "mkt.daily_bar" && r.Payload["trade_date"] != "2026-11-20" {
			t.Fatal("invented adjusted price")
		}
	}
	security := lookup("ref.security", code, anyRow)
	if security.Payload["total_shares"] != "12345600" || security.Payload["margin_eligible"] != true {
		t.Fatal(security)
	}
	hist := lookup("ref.industry", code, func(p map[string]any) bool { return p["level2_code"] == "801021.SI" })
	if hist.Payload["valid_to"] != "2021-10-31" {
		t.Fatal(hist)
	}
	val := lookup("mkt.valuation_daily", code, anyRow)
	if val.Payload["float_mv"] != "753400" || val.Payload["pe_ttm"] != nil {
		t.Fatal(val)
	}
	for _, check := range []struct {
		dataset, code, field string
		expected             any
	}{
		{"flow.margin", code, "financing_balance", "250000000"}, {"flow.order_size", code, "xl_net", "-4600"}, {"flow.block_trade", code, "amount", "8251600"},
		{"corp.holder", code, "holder_count", int64(25666)}, {"ref.security", "600001.SH", "margin_eligible", false},
	} {
		r := lookup(check.dataset, check.code, anyRow)
		if r.Payload[check.field] != check.expected {
			t.Fatal(r.Payload)
		}
	}
	for _, k := range []struct{ code, amt string }{{"000300.SH", "1230"}, {"801011.SI", "12300"}} {
		r := lookup("mkt.index_bar", "", func(p map[string]any) bool { return p["index_code"] == k.code })
		if r.Payload["amount"] != k.amt {
			t.Fatal(r)
		}
	}
}

func TestMarketRightsAndConflicts(t *testing.T) {
	cutoff := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		change  func(*marketTestSource)
		wantErr bool
	}{
		{"optional endpoint denied", func(s *marketTestSource) {
			s.fail["margin_secs"] = &dataservice.ProviderError{Class: dataservice.ErrNotCovered, Err: errors.New("synthetic")}
		}, false},
		{"invalid token", func(s *marketTestSource) {
			s.fail["moneyflow"] = &dataservice.ProviderError{Class: dataservice.ErrAuthFailed, Err: errors.New("synthetic")}
		}, true},
		{"conflicting daily", func(s *marketTestSource) {
			bad := maps.Clone(s.data["daily"][0])
			bad["close"] = "14"
			s.data["daily"] = append(s.data["daily"], bad)
		}, true},
		{"impossible block units", func(s *marketTestSource) { s.data["block_trade"][0]["amount"] = "8251600" }, true},
		{"full day suspension conflicts with trade", func(s *marketTestSource) { s.data["suspend_d"][0]["trade_date"] = "20261120" }, true},
		{"legitimate intraday halt", func(s *marketTestSource) {
			s.data["suspend_d"][0]["trade_date"] = "20261120"
			s.data["suspend_d"][0]["suspend_timing"] = "10:05-10:35"
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			src := marketTestData()
			test.change(src)
			b, err := collectMarket(t.Context(), src, "600000.SH", cutoff)
			if (err != nil) != test.wantErr {
				t.Fatalf("got %v", err)
			}
			if test.name == "optional endpoint denied" {
				if slices.ContainsFunc(b.Records, func(r dataservice.Record) bool { return r.DatasetID == "ref.security" }) || len(b.Notes) == 0 {
					t.Fatal("unknown margin eligibility treated as known")
				}
			}
		})
	}
}

func TestMarginRosterIsExchangeScoped(t *testing.T) {
	src := marketTestData()
	for api, rows := range src.data {
		for _, r := range rows {
			if r["ts_code"] == "600001.SH" {
				r["ts_code"] = "000001.SZ"
				if api == "stock_basic" {
					r["exchange"] = "SZSE"
				}
			}
		}
	}
	b, err := collectMarket(t.Context(), src, "600000.SH", time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(b.Peers, "000001.SZ") {
		t.Fatal("missing Shenzhen peer")
	}
	for _, e := range b.Entities {
		if e.Code == "000001.SZ" {
			if _, known := e.Attrs["margin_eligible"]; known {
				t.Fatal("inferred Shenzhen eligibility from Shanghai roster")
			}
		}
	}
	for _, r := range b.Records {
		if r.DatasetID == "corp.holder" && (r.PublishedAt == nil || r.PublishedAt.Nanosecond() != 999999000) {
			t.Fatal("publication must round-trip through PostgreSQL exactly")
		}
	}
}
