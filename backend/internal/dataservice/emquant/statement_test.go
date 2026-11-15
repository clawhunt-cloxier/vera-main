package emquant

import (
	"context"
	"errors"
	"testing"
)

func TestCollectStatementFieldsPreservesPeriodAndSource(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"IncomeStatement_61", "IncomeStatement_9"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 11, Value: -12.5}, {Type: 8, Value: int64(100000)}}}}
	s := &testRawStore{ref: "s3://test/statement"}
	got, err := (Collector{Fetcher: f, Store: s}).CollectStatementFields(context.Background(), "A.SH", "2025-12-31", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestedPeriod != "2025-12-31" || got.RequestedType != 1 || got.RevenueCNY == nil || *got.RevenueCNY != "100000" || got.NetProfitCNY == nil || *got.NetProfitCNY != "-12.5" || got.Locators["INCOMESTATEMENT_61"] != "$.data.Values[0]" || got.Source.BodySHA256 != digest(s.body) {
		t.Fatalf("unexpected statement mapping: %+v", got)
	}
	replayed, _, err := DecodeArchive(s.body, got.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mapStatementFields(replayed, got.Source, "A.SH", "2025-09-30", 1); err == nil {
		t.Fatal("wrong requested period accepted")
	}
	if _, err = mapStatementFields(replayed, got.Source, "A.SH", "2025-12-31", 3); err == nil {
		t.Fatal("wrong requested statement type accepted")
	}
	replayed.Values[0].Value = -11.0
	if _, err = mapStatementFields(replayed, got.Source, "A.SH", "2025-12-31", 1); err == nil {
		t.Fatal("modified response accepted")
	}
}

func TestCollectStatementFieldsMissingAndInvalidData(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"IncomeStatement_9", "IncomeStatement_61"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 0}, {Type: 11, Value: 0.0}}}}
	s := &testRawStore{ref: "s3://test/statement"}
	c := Collector{Fetcher: f, Store: s}
	got, err := c.CollectStatementFields(context.Background(), "A.SH", "2025-12-31", 1)
	if err != nil || got.RevenueCNY != nil || got.NetProfitCNY == nil || *got.NetProfitCNY != "0" {
		t.Fatalf("null and zero were not preserved: %+v, %v", got, err)
	}
	before := f.calls
	for _, input := range []struct {
		code, period string
		kind         int
	}{{"A.SH,B.SZ", "2025-12-31", 1}, {"A.SH", "2025-11-30", 1}, {"A.SH", "2025-12-31", 5}} {
		if _, err := c.CollectStatementFields(context.Background(), input.code, input.period, input.kind); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if f.calls != before {
		t.Fatal("invalid request reached vendor")
	}
	f.cube.Codes[0] = "B.SZ"
	if _, err := c.CollectStatementFields(context.Background(), "A.SH", "2025-12-31", 1); !errors.Is(err, ErrShape) {
		t.Fatalf("wrong response security: %v", err)
	}
	s.err = errors.New("storage unavailable")
	if got, err := c.CollectStatementFields(context.Background(), "A.SH", "2025-12-31", 1); err == nil || got.RevenueCNY != nil {
		t.Fatal("failed archive exposed statement data")
	}
}
