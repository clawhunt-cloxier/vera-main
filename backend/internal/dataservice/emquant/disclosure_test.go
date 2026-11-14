package emquant

import (
	"context"
	"errors"
	"testing"
)

func TestDisclosureObservationPreservesRawCellsAndBinding(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"CORRECTANNCNUM", "STMTACTDATE", "CORRECTANNCDATE"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 8, Value: int64(2)}, {Type: 13, Value: "2026-04-01"}, {Type: 0}}}}
	store := &testRawStore{ref: "s3://test/disclosure"}
	c := Collector{Fetcher: f, Store: store}
	got, err := c.CollectDisclosureObservation(context.Background(), "A.SH", "2025-12-31")
	if err != nil || got.Cells["CORRECTANNCNUM"].Value != int64(2) || got.Cells["CORRECTANNCDATE"].Value != nil || got.Locators["STMTACTDATE"] != "$.data.Values[1]" {
		t.Fatalf("observation: %v", err)
	}
	cube, _, err := DecodeArchive(store.body, got.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := mapDisclosureObservation(cube, got.Source, "A.SH", "2025-12-31")
	if err != nil || replayed.Cells["CORRECTANNCNUM"].Value != int64(2) {
		t.Fatalf("replay: %v", err)
	}
	if _, err := mapDisclosureObservation(cube, got.Source, "A.SH", "2025-09-30"); err == nil {
		t.Fatal("accepted wrong period")
	}
	cube.Values[0].Value = int64(1)
	if _, err := mapDisclosureObservation(cube, got.Source, "A.SH", "2025-12-31"); err == nil {
		t.Fatal("accepted modified response")
	}
	before := f.calls
	for _, in := range []struct{ code, period string }{{"A.SH,B.SZ", "2025-12-31"}, {"A.SH", "2025-11-30"}} {
		if _, err := c.CollectDisclosureObservation(context.Background(), in.code, in.period); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if f.calls != before {
		t.Fatal("invalid request reached vendor")
	}
	f.cube.Codes[0] = "B.SZ"
	if _, err := c.CollectDisclosureObservation(context.Background(), "A.SH", "2025-12-31"); !errors.Is(err, ErrShape) {
		t.Fatalf("wrong security: %v", err)
	}
	store.err = errors.New("unavailable")
	if got, err := c.CollectDisclosureObservation(context.Background(), "A.SH", "2025-12-31"); err == nil || len(got.Cells) != 0 {
		t.Fatal("archive failure exposed data")
	}
}
