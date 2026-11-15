package emquant

import (
	"context"
	"errors"
	"testing"
)

func TestIndustryLevelRequiresBoundSourceAndCutoff(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"HISSW2021CODE", "HISSW2021"}, Dates: []string{"2026-11-14"}, Values: []Cell{{Type: 13, Value: "synthetic-code"}, {Type: 13, Value: "模拟行业"}}}}
	s := &testRawStore{ref: "s3://test/industry"}
	got, err := (Collector{Fetcher: f, Store: s}).CollectIndustryLevel(context.Background(), "A.SH", "2025-12-31", 2)
	if err != nil || got.RequestedAsOf != "2025-12-31" || got.RequestedLevel != 2 || got.IndustryCode != "synthetic-code" || got.IndustryName != "模拟行业" || got.Source.BodySHA256 != digest(s.body) || got.Locators["HISSW2021CODE"] != "$.data.Values[0]" {
		t.Fatalf("industry observation: %+v, %v", got, err)
	}
	cube, _, err := DecodeArchive(s.body, got.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapIndustryLevel(cube, got.Source, "A.SH", "2025-09-30", 2); err == nil {
		t.Fatal("wrong cutoff accepted")
	}
	if _, err := mapIndustryLevel(cube, got.Source, "A.SH", "2025-12-31", 1); err == nil {
		t.Fatal("wrong level accepted")
	}
	cube.Values[0].Value = "modified"
	if _, err := mapIndustryLevel(cube, got.Source, "A.SH", "2025-12-31", 2); err == nil {
		t.Fatal("modified archived data accepted")
	}
}

func TestIndustryLevelRejectsMissingAndAmbiguousData(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"HISSW2021", "HISSW2021CODE"}, Dates: []string{"2026-11-14"}, Values: []Cell{{Type: 13, Value: "模拟行业"}, {Type: 0}}}}
	s := &testRawStore{ref: "s3://test/industry"}
	c := Collector{Fetcher: f, Store: s}
	if _, err := c.CollectIndustryLevel(context.Background(), "A.SH", "2025-12-31", 1); !errors.Is(err, ErrShape) {
		t.Fatalf("missing code: %v", err)
	}
	f.cube.Values[1] = Cell{Type: 13, Value: "synthetic-code"}
	f.cube.Indicators[1] = "hissw2021"
	if _, err := c.CollectIndustryLevel(context.Background(), "A.SH", "2025-12-31", 1); !errors.Is(err, ErrShape) {
		t.Fatalf("duplicate field: %v", err)
	}
	before := f.calls
	if _, err := c.CollectIndustryLevel(context.Background(), "A.SH,B.SZ", "2025-12-31", 1); err == nil || f.calls != before {
		t.Fatal("multiple securities reached vendor")
	}
	if _, err := c.CollectIndustryLevel(context.Background(), "A.SH", "2025-12-31", 4); err == nil || f.calls != before {
		t.Fatal("all-level response reached vendor")
	}
	s.err = errors.New("store unavailable")
	if got, err := c.CollectIndustryLevel(context.Background(), "A.SH", "2025-12-31", 1); err == nil || got.IndustryCode != "" {
		t.Fatal("failed archive exposed industry data")
	}
}
