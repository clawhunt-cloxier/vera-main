package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestCanonicalEvidenceUsesCatalogUnitsAndFilingEdition(t *testing.T) {
	pool := dbtest.New(t)
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	s := Server{Pool: pool, Store: dataservice.NewStore(pool, cat)}
	req := httptest.NewRequest("GET", "/api/v1/evidence/example", nil)
	bar := dataservice.StoredRecord{DatasetID: "mkt.daily_bar", ProviderID: "simulated", Payload: map[string]any{
		"volume": "101", "amount": "1.05", "close": "12.30", "turnover_rate": "0.0001234", "high": nil,
	}}
	ev := s.evidence(req, bar)
	if ev.Excerpt == nil {
		t.Fatal("missing daily evidence")
	}
	for _, want := range []string{"成交量：1.01 手", "成交额：0.000105 万元", "12.30 元/股", "0.01234%"} {
		if !strings.Contains(*ev.Excerpt, want) {
			t.Errorf("missing %q in %s", want, *ev.Excerpt)
		}
	}
	if strings.Contains(*ev.Excerpt, "最高价") {
		t.Fatal("missing high was displayed")
	}
	bar.Payload["amount"] = "0"
	if got := s.evidence(req, bar); !strings.Contains(*got.Excerpt, "成交额：0.00 万元") {
		t.Fatalf("zero: %s", *got.Excerpt)
	}
	for _, tc := range []struct {
		revision any
		want     string
	}{{float64(0), "公告 ABC，公告更正序号 0"}, {float64(2), "公告 ABC，公告更正序号 2"}, {nil, "公告 ABC"}} {
		r := dataservice.StoredRecord{DatasetID: "fin.statement", Version: 9, Payload: map[string]any{"item_code": "IS.revenue", "value": "100", "filing_id": "ABC", "revision": tc.revision}}
		if got := s.evidence(req, r); got.Locator == nil || *got.Locator != tc.want {
			t.Fatalf("wanted %s, got %v", tc.want, got.Locator)
		}
	}
}
