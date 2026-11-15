package emquant

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCollectDailyLiquidityUsesConfirmedUnitsAndLineage(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"TURN", "VOLUME", "AMOUNT"}, Dates: []string{"2026-11-02", "2026-11-03"}, Values: []Cell{{Type: 11, Value: 1.25}, {Type: 8, Value: int64(10000)}, {Type: 11, Value: 123400.5}, {Type: 0}, {Type: 8, Value: int64(0)}, {Type: 11, Value: 0.0}}}}
	s := &testRawStore{ref: "s3://test/decoded"}
	rows, err := (Collector{Fetcher: f, Store: s}).CollectDailyLiquidity(context.Background(), "A.SH", "2026-11-02", "2026-11-03")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].VolumeShares != "10000" || rows[0].AmountCNY != "123400.5" || rows[0].TurnoverRatio == nil || *rows[0].TurnoverRatio != "0.0125" || rows[0].Locators["VOLUME"] != "$.data.Values[1]" || rows[1].TurnoverRatio != nil || rows[1].VolumeShares != "0" {
		t.Fatalf("unexpected mapping: %+v", rows)
	}
	if rows[0].Source.BodySHA256 != digest(s.body) || rows[0].Source.BodyRef != s.ref || rows[0].Source.ArchivedAt.Before(rows[0].Source.ReceivedAt) {
		t.Fatal("missing provenance")
	}
	replayed, _, err := DecodeArchive(s.body, rows[0].Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", "2026-11-02")
	end, _ := time.Parse("2006-01-02", "2026-11-03")
	if _, err = mapDailyLiquidity(replayed, rows[0].Source, "A.SH", start, end); err != nil {
		t.Fatalf("replay: %v", err)
	}
	replayed.Values[1].Value = int64(10001)
	if _, err = mapDailyLiquidity(replayed, rows[0].Source, "A.SH", start, end); err == nil {
		t.Fatal("modified data accepted with old archive hash")
	}
}

func TestDailyLiquidityRejectsMisattributionAndInvalidData(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"VOLUME", "AMOUNT", "TURN"}, Dates: []string{"2026-11-02"}, Values: []Cell{{Type: 8, Value: int64(100)}, {Type: 11, Value: 200.0}, {Type: 11, Value: 1.0}}}}
	s := &testRawStore{ref: "s3://test/decoded"}
	c := Collector{Fetcher: f, Store: s}
	if _, err := c.CollectDailyLiquidity(context.Background(), "B.SH", "2026-11-02", "2026-11-02"); !errors.Is(err, ErrShape) {
		t.Fatalf("wrong security: %v", err)
	}
	f.cube.Values[0] = Cell{Type: 11, Value: -1.0}
	if _, err := c.CollectDailyLiquidity(context.Background(), "A.SH", "2026-11-02", "2026-11-02"); !errors.Is(err, ErrShape) {
		t.Fatalf("negative volume: %v", err)
	}
	f.cube.Values[0] = Cell{Type: 10, Value: 16777218.0}
	if _, err := c.CollectDailyLiquidity(context.Background(), "A.SH", "2026-11-02", "2026-11-02"); !errors.Is(err, ErrShape) {
		t.Fatalf("unreliable float volume: %v", err)
	}
	f.cube.Values[0] = Cell{Type: 0}
	if _, err := c.CollectDailyLiquidity(context.Background(), "A.SH", "2026-11-02", "2026-11-02"); !errors.Is(err, ErrShape) {
		t.Fatalf("missing volume: %v", err)
	}
	before := f.calls
	if _, err := c.CollectDailyLiquidity(context.Background(), "A.SH,B.SZ", "2026-11-02", "2026-11-02"); err == nil || f.calls != before {
		t.Fatal("invalid code reached SDK")
	}
}
