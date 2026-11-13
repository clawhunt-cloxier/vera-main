package emquant

import (
	"context"
	"errors"
	"testing"
)

func TestSecurityBasicsPreservesRawProvenanceAndUnknownStatus(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"LISTDATE", "NAME", "DELISTDATE"}, Dates: []string{"2026-11-14"}, Values: []Cell{{Type: 13, Value: "2010/1/2"}, {Type: 13, Value: "模拟证券"}, {Type: 0}}}}
	s := &testRawStore{ref: "s3://test/security"}
	got, err := (Collector{Fetcher: f, Store: s}).CollectSecurityBasics(context.Background(), "A.SH")
	if err != nil || got.ShortName != "模拟证券" || got.FirstListingDate != "2010-01-02" || got.DelistingDate != nil || got.Source.BodySHA256 != digest(s.body) || got.Locators["NAME"] != "$.data.Values[1]" {
		t.Fatalf("security observation: %+v, %v", got, err)
	}
	cube, _, err := DecodeArchive(s.body, got.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mapSecurityBasics(cube, got.Source, "B.SZ"); err == nil {
		t.Fatal("wrong request accepted")
	}
	cube.Values[1].Value = "modified"
	if _, err = mapSecurityBasics(cube, got.Source, "A.SH"); err == nil {
		t.Fatal("modified data accepted")
	}
}

func TestSecurityBasicsRejectsInvalidOrAmbiguousData(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"NAME", "LISTDATE", "DELISTDATE"}, Dates: []string{"2026-11-14"}, Values: []Cell{{Type: 13, Value: "模拟证券"}, {Type: 13, Value: "2010-01-02"}, {Type: 13, Value: "2009-01-01"}}}}
	s := &testRawStore{ref: "s3://test/security"}
	c := Collector{Fetcher: f, Store: s}
	if _, err := c.CollectSecurityBasics(context.Background(), "A.SH"); !errors.Is(err, ErrShape) {
		t.Fatalf("delisting before listing: %v", err)
	}
	f.cube.Values[2] = Cell{Type: 0}
	f.cube.Indicators[2] = "name"
	if _, err := c.CollectSecurityBasics(context.Background(), "A.SH"); !errors.Is(err, ErrShape) {
		t.Fatalf("duplicate field: %v", err)
	}
	before := f.calls
	if _, err := c.CollectSecurityBasics(context.Background(), "A.SH,B.SZ"); err == nil || f.calls != before {
		t.Fatal("multiple securities reached vendor")
	}
	s.err = errors.New("store unavailable")
	if got, err := c.CollectSecurityBasics(context.Background(), "A.SH"); err == nil || got.ShortName != "" {
		t.Fatal("failed archive exposed security data")
	}
}
