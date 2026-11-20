package tushare

import (
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

const dragonTestCode = "600000.SH"
const dragonTestDay = "20261119"
const dragonTestReason = "日涨幅偏离值超过7%的证券"

func dragonTestRows(day, reason string) []map[string]string {
	var rows []map[string]string
	for i := range 5 {
		for _, side := range []string{"0", "1"} {
			name := fmt.Sprintf("合成席位%d营业部", i)
			if i == 4 {
				name = "机构专用"
			}
			rows = append(rows, map[string]string{"ts_code": dragonTestCode, "trade_date": day, "side": side, "reason": reason, "exalter": name, "buy": strconv.Itoa(i + 10), "sell": strconv.Itoa(50 - i)})
		}
	}
	return rows
}

func testDragonCollector(t *testing.T, src *marketTestSource) *marketCollector {
	t.Helper()
	c := &marketCollector{Source: src, ctx: t.Context(), code: dragonTestCode, from: "20261101", until: "20261120"}
	if err := c.calendar(); err != nil {
		t.Fatal(err)
	}
	c.Records = slices.Clip(c.Records)
	return c
}

func dragonTestFilter(rs []dataservice.Record, id string) []dataservice.Record {
	var found []dataservice.Record
	for _, r := range rs {
		if r.DatasetID == id {
			found = append(found, r)
		}
	}
	return found
}

func TestDragonCoverageEditionsAndLineage(t *testing.T) {
	src := marketTestData()
	src.data["top_list"] = []map[string]string{
		{"ts_code": dragonTestCode, "trade_date": dragonTestDay, "reason": dragonTestReason},
		{"ts_code": "600002.SH", "trade_date": "20261118", "reason": "合成其他股票原因"},
		{"ts_code": "000002.SZ", "trade_date": "20261117", "reason": "合成其他交易所原因"},
		{"ts_code": dragonTestCode, "trade_date": "20261120", "reason": dragonTestReason}, // test source returns at 19:00 CST.
	}
	src.data["top_inst"] = dragonTestRows(dragonTestDay, dragonTestReason)
	collect := func() ([]dataservice.Record, []dataservice.Record) {
		t.Helper()
		c := testDragonCollector(t, src)
		if err := c.dragonTiger(); err != nil {
			t.Fatal(err)
		}
		return dragonTestFilter(c.Records, "flow.dragon_tiger_day"), dragonTestFilter(c.Records, "flow.dragon_tiger")
	}
	days, seats := collect()
	if len(days) != 2 || len(seats) != 10 || days[0].Payload["listed"] != true || days[0].Payload["reason_count"] != 1 || days[0].Payload["reason_text"] != dragonTestReason || days[0].Payload["seats_complete"] != true || days[1].Payload["listed"] != false || days[1].Payload["seats_complete"] != true || days[1].Payload["trade_date"] != "2026-11-18" {
		t.Fatalf("coverage=%+v seats=%d", days, len(seats))
	}
	edition := days[0].Payload["edition_id"]
	if seats[0].Payload["side"] != "buy" || seats[0].Payload["rank"] != 1 || seats[0].Payload["buy_amount"] != "14" || seats[0].Payload["sell_amount"] != "46" || seats[0].Payload["seat_type"] != "institution" || seats[0].Payload["period_scope"] != "single_day" || seats[0].Payload["edition_id"] != edition || seats[5].Payload["side"] != "sell" || seats[5].Payload["sell_amount"] != "50" {
		t.Fatal(seats)
	}
	var proof struct {
		Pages map[string]marketInput `json:"top_list_pages"`
		Seats map[string]marketInput `json:"seats"`
	}
	if err := json.Unmarshal(src.objects[days[0].RawRef], &proof); err != nil || len(proof.Pages) != 1 || len(proof.Seats) != 10 || seats[0].RawRef != days[0].RawRef || seats[0].RawLocator == "$" {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	slices.Reverse(src.data["top_inst"])
	daysAgain, seatsAgain := collect()
	if daysAgain[0].Payload["edition_id"] != edition || !maps.Equal(seats[0].Payload, seatsAgain[0].Payload) {
		t.Fatal("source order changed edition or rank")
	}
	src.data["top_inst"][0]["buy"] = "203.781"
	revised, _ := collect()
	if revised[0].Payload["edition_id"] == edition {
		t.Fatal("amount revision kept old edition")
	}
	src.data["top_inst"] = src.data["top_inst"][:9]
	incomplete, seatsAfterRemoval := collect()
	if incomplete[0].Payload["edition_id"] == edition || incomplete[0].Payload["listed"] != true || incomplete[0].Payload["seats_complete"] != false || len(seatsAfterRemoval) != 0 {
		t.Fatal("incomplete replacement treated as complete")
	}
}

func TestDragonScopeAndPartialDisclosures(t *testing.T) {
	rs := dragonTestRows(dragonTestDay, dragonTestReason)
	for _, tt := range []struct {
		name   string
		change func([]map[string]string)
	}{
		{"null sell", func(rs []map[string]string) { delete(rs[1], "sell") }},
		{"bad side", func(rs []map[string]string) { rs[0]["side"] = "X" }},
		{"unknown reason", func(rs []map[string]string) { rs[0]["reason"] = "另一榜单" }},
		{"identical seats", func(rs []map[string]string) { rs[0] = maps.Clone(rs[2]) }},
		{"negative amount", func(rs []map[string]string) { rs[0]["sell"] = "-0.01" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values := make([]map[string]string, len(rs))
			for i := range rs {
				values[i] = maps.Clone(rs[i])
			}
			tt.change(values)
			rows := make([]Row, len(values))
			for i, v := range values {
				rows[i] = Row{Values: v}
			}
			if seats, ok := dragonSeats(rows, map[string]bool{dragonTestReason: true}); ok || len(seats) > 0 {
				t.Fatal("invalid disclosure released seats")
			}
		})
	}
	if dragonScope("连续三个交易日内日涨幅偏离值累计达20%") != "multi_day" || dragonScope("涨幅偏离值异动") != "unknown" || dragonSeatType("疑似机构专用席位") != "other" || dragonSeatType("深股通专用") != "connect" {
		t.Fatal("ambiguous classifications")
	}
}

func TestDragonEndpointEntitlementsIndependent(t *testing.T) {
	for _, tt := range []struct {
		api      string
		class    dataservice.ErrClass
		wantDays int
		wantFail bool
	}{
		{"top_list", dataservice.ErrNotCovered, 0, false}, {"top_list", dataservice.ErrRateLimited, 0, false}, {"top_inst", dataservice.ErrNotCovered, 1, false}, {"top_inst", dataservice.ErrRateLimited, 1, false}, {"top_inst", dataservice.ErrAuthFailed, 0, true},
	} {
		t.Run(tt.api+string(tt.class), func(t *testing.T) {
			src := marketTestData()
			src.data["top_list"] = []map[string]string{{"ts_code": dragonTestCode, "trade_date": dragonTestDay, "reason": dragonTestReason}}
			src.fail[tt.api] = &dataservice.ProviderError{Class: tt.class, Err: errors.New("synthetic")}
			c := testDragonCollector(t, src)
			err := c.dragonTiger()
			days := dragonTestFilter(c.Records, "flow.dragon_tiger_day")
			if (err != nil) != tt.wantFail || len(days) != tt.wantDays || len(days) > 0 && (days[0].Payload["reason_text"] != dragonTestReason || days[0].Payload["seats_complete"] != false) {
				t.Fatalf("err=%v days=%v", err, days)
			}
		})
	}
}

func TestDragonPublicationCutoffAndWindow(t *testing.T) {
	src := marketTestData()
	c := testDragonCollector(t, src)
	row := Row{Values: map[string]string{"ts_code": dragonTestCode, "reason": dragonTestReason}, ReceivedAt: time.Date(2026, 11, 19, 19, 59, 59, 0, marketZone)}
	if r, e := c.dragonRoster(dragonTestDay, []Row{row}); e != nil || r != nil {
		t.Fatal("premature coverage")
	}
	row.ReceivedAt = row.ReceivedAt.Add(time.Second)
	if r, e := c.dragonRoster(dragonTestDay, []Row{row}); e != nil || r == nil {
		t.Fatal("missed completed disclosure")
	}
	c.Records = nil
	for i := range 30 {
		date := time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC)
		c.Records = append(c.Records, dataservice.Record{DatasetID: "ref.trading_calendar", Period: &date, Payload: map[string]any{"is_open": true}})
		src.data["top_list"] = append(src.data["top_list"], map[string]string{"ts_code": "600002.SH", "trade_date": date.Format(marketDay), "reason": "模拟原因"})
	}
	if err := c.dragonTiger(); err != nil {
		t.Fatal(err)
	}
	days := dragonTestFilter(c.Records, "flow.dragon_tiger_day")
	if len(days) != 20 || days[0].Payload["trade_date"] != "2026-09-30" || days[len(days)-1].Payload["trade_date"] != "2026-09-11" {
		t.Fatalf("bad window %v", days)
	}
}

func TestDragonMultipleReasonsAndDistinctSides(t *testing.T) {
	src := marketTestData()
	const multi = "连续10个交易日内日涨幅偏离值累计异常"
	src.data["top_list"] = []map[string]string{
		{"ts_code": dragonTestCode, "trade_date": dragonTestDay, "reason": multi},
		{"ts_code": dragonTestCode, "trade_date": dragonTestDay, "reason": dragonTestReason},
	}
	src.data["top_inst"] = append(dragonTestRows(dragonTestDay, multi), dragonTestRows(dragonTestDay, dragonTestReason)...)
	c := testDragonCollector(t, src)
	if err := c.dragonTiger(); err != nil {
		t.Fatal(err)
	}
	days, seats := dragonTestFilter(c.Records, "flow.dragon_tiger_day"), dragonTestFilter(c.Records, "flow.dragon_tiger")
	if len(days) != 1 || len(seats) != 20 || days[0].Payload["reason_count"] != 2 || days[0].Payload["seats_complete"] != true {
		t.Fatal("multiple disclosure groups collapsed")
	}
	for _, r := range seats {
		if r.Payload["reason_text"] == multi && r.Payload["period_scope"] != "multi_day" {
			t.Fatal("multi-session disclosure marked single day")
		}
	}
}
