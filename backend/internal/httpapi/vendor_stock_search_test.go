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

func TestStockSearchTermsExtractsNamesFromResearchText(t *testing.T) {
	for input, want := range map[string][]string{
		"中国移动":             {"中国移动"},
		"分析中国移动最近可用行情":     {"中国移动"},
		"看看 中国石油 最近行情":     {"中国石油"},
		"深度研究贵州茅台近一个月的基本面": {"贵州茅台"},
		"分析美的集团最近行情":       {"美的集团"},
	} {
		got := stockSearchTerms(input)
		for _, term := range want {
			if !slices.Contains(got, term) {
				t.Errorf("%q: terms %v missing %q", input, got, term)
			}
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
