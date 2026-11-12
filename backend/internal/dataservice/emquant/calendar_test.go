package emquant

import (
	"context"
	"testing"
	"time"
)

func TestCollectOpenDatesChecksResponseAndPreservesArchive(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{""}, Indicators: []string{"TRADEDATE"}, Dates: []string{"2026/11/9", "2026/11/10"}, Values: []Cell{{Type: 13, Value: "2026/11/9"}, {Type: 13, Value: "2026/11/10"}}}}
	s := &testRawStore{ref: "object://test/calendar"}
	c := Collector{Fetcher: f, Store: s}
	days, ref, err := c.CollectOpenDates(context.Background(), "CNSESH", "2026-11-09", "2026-11-15")
	if err != nil || len(days) != 2 || days[0] != "2026-11-09" || days[1] != "2026-11-10" || ref.BodySHA256 != digest(s.body) {
		t.Fatalf("calendar %v %+v %v", days, ref, err)
	}
	cube, _, err := DecodeArchive(s.body, ref.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", "2026-11-09")
	end, _ := time.Parse("2006-01-02", "2026-11-15")
	if _, err = openDates(cube, start, end); err != nil {
		t.Fatal(err)
	}
	calls := f.calls
	if _, _, err = c.CollectOpenDates(context.Background(), "UNKNOWN", "2026-11-09", "2026-11-15"); err == nil || f.calls != calls {
		t.Fatal("unsupported market reached SDK")
	}
	f.cube.Values[1].Value = "2026/11/9"
	if days, _, err = c.CollectOpenDates(context.Background(), "CNSESH", "2026-11-09", "2026-11-15"); err == nil || days != nil {
		t.Fatal("mismatched dates accepted")
	}
}
