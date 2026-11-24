package dataservice_test

import (
	"context"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
)

func TestFixturePayloadsMatchCatalog(t *testing.T) {
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := fixture.New("../../../tests/fixtures/demo-universe.json")
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := p.Records(context.Background())
	bad := 0
	for _, r := range recs {
		if err := cat.Validate(r); err != nil {
			if bad < 15 {
				t.Error(err)
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%d invalid records", bad)
	}
}

func TestCanonicalCalendarDate(t *testing.T) {
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for date, valid := range map[string]bool{"2024-02-29": true, "2025-02-29": false, "2026-02-31": false, "2026-13-01": false} {
		r := dataservice.Record{DatasetID: "ref.trading_calendar", Payload: map[string]any{"trade_date": date, "is_open": true}}
		if err := cat.Validate(r); (err == nil) != valid {
			t.Errorf("calendar date %s, wanted valid=%v, got %v", date, valid, err)
		}
	}
}
