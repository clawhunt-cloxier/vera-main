package httpapi

import (
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
)

func TestReplayScopeSeparatesProvidersAndProducts(t *testing.T) {
	seen := map[string]string{"tushare": "historical unqualified vendor"}
	for _, tc := range []struct {
		label, id, product string
		full               bool
	}{
		{label: "fixture"},
		{label: "TuShare", full: true},
		{label: "Choice, same provider ID", id: "tushare", product: "quoted"},
		{label: "Choice, different product", id: "tushare", product: "another"},
		{label: "Choice, different provider ID", id: "another", product: "quoted"},
	} {
		s := &Server{FullVendorResearch: tc.full, Engine: &harness.Engine{}}
		if tc.id != "" {
			s.Engine.Brief = &emquant.BriefService{ProviderID: tc.id, Product: tc.product}
		}
		scope := s.replayScope()
		if prior, exists := seen[scope]; exists {
			t.Fatalf("%s and %s share a cached response scope", tc.label, prior)
		}
		seen[scope] = tc.label
	}
}
