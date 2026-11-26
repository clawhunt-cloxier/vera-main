package emquant

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
