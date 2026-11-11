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
