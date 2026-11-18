package httpapi

import (
	"slices"
	"testing"
)

func TestMatchedSearchCodes(t *testing.T) {
	raw := "000001.HK,000001.SZ, 000001.sh,000002.BJ,000001,invalid"
	for _, tc := range []struct {
		query      string
		codeLookup bool
		want       []string
	}{
		{"000001", true, []string{"000001.SZ", "000001.SH"}},
		{"000001.SH", true, []string{"000001.SH"}},
		{"000001.BJ", true, nil},
		{"name", false, []string{"000001.SZ", "000001.SH", "000002.BJ"}},
	} {
		if got := matchedSearchCodes(raw, tc.query, tc.codeLookup); !slices.Equal(got, tc.want) {
			t.Errorf("query %q: got %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestQualifiedStockSearchIsExactEvenWithFuzzyCache(t *testing.T) {
	e, _ := realtimeAPIEnv(t)
	c := onboardAPIClient(t, e, "stock-search")
	// The fixture contains 600000.SH. A cache miss must try the offline
	// test runtime and surface its failure instead of returning a near hit.
	for query, want := range map[string]int{"600000.sh": 200, "600001.SH": 503, "600000": 503} {
		status, _, _ := c.do("GET", "/api/v1/entities?q="+query, nil, apiHeaders(realtimeMedia))
		if status != want {
			t.Errorf("%s: got %d, want %d", query, status, want)
		}
	}
}
