package emquant

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMarginObservationsPreserveCellsAndArchive(t *testing.T) {
	fields := []string{"SECLENDINGBALANCEM", "TRADINGBALANCEM", "PURCHASEWITHBORROWEDMONEYM", "REPAYMENTTOBROKERM", "SECLENDINGBALANCEVOLUMEM"}
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: fields, Dates: []string{"2026-11-02", "2026-11-03"}, Values: []Cell{{Type: 0}, {Type: 8, Value: int64(123)}, {Type: 11, Value: 3.5}, {Type: 0}, {Type: 8, Value: int64(45)}, {Type: 0}, {Type: 8, Value: int64(124)}, {Type: 0}, {Type: 0}, {Type: 0}}}}
	store := &testRawStore{ref: "object://test/margin"}
	c := Collector{Fetcher: f, Store: store}
	got, err := c.CollectMarginObservations(context.Background(), "A.SH", "2026-11-02", "2026-11-03")
	if err != nil || len(got.Rows) != 2 || got.Rows[0].Cells["TRADINGBALANCEM"].Value != int64(123) || got.Rows[1].Cells["PURCHASEWITHBORROWEDMONEYM"].Value != nil || got.Rows[0].Locators["TRADINGBALANCEM"] != "$.data.Values[1]" {
		t.Fatalf("observations: %v", err)
	}
	cube, _, err := DecodeArchive(store.body, got.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", "2026-11-02")
	end, _ := time.Parse("2006-01-02", "2026-11-03")
	if _, err := mapMarginObservations(cube, got.Source, "A.SH", start, end); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, err := mapMarginObservations(cube, got.Source, "B.SZ", start, end); err == nil {
		t.Fatal("wrong request accepted")
	}
	cube.Values[1].Value = int64(999)
	if _, err := mapMarginObservations(cube, got.Source, "A.SH", start, end); err == nil {
		t.Fatal("modified source accepted")
	}
	before := f.calls
	if _, err := c.CollectMarginObservations(context.Background(), "A.SH,B.SZ", "2026-11-02", "2026-11-03"); err == nil || f.calls != before {
		t.Fatal("invalid query reached vendor")
	}
	f.cube.Dates = []string{"2026-11-03", "2026-11-02"}
	if _, err := c.CollectMarginObservations(context.Background(), "A.SH", "2026-11-02", "2026-11-03"); !errors.Is(err, ErrShape) {
		t.Fatalf("unordered dates: %v", err)
	}
	store.err = errors.New("unavailable")
	if result, err := c.CollectMarginObservations(context.Background(), "A.SH", "2026-11-02", "2026-11-03"); err == nil || len(result.Rows) != 0 {
		t.Fatal("archive failure exposed data")
	}
}

func TestEmptyMarginObservationsRetainSource(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"TRADINGBALANCEM", "PURCHASEWITHBORROWEDMONEYM", "REPAYMENTTOBROKERM", "SECLENDINGBALANCEVOLUMEM", "SECLENDINGBALANCEM"}}}
	store := &testRawStore{ref: "object://test/margin"}
	got, err := (Collector{Fetcher: f, Store: store}).CollectMarginObservations(context.Background(), "A.SH", "2026-11-02", "2026-11-03")
	if err != nil || len(got.Rows) != 0 || got.Source.BodySHA256 != digest(store.body) {
		t.Fatalf("empty response provenance: %v", err)
	}
}
