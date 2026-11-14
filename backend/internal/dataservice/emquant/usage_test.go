package emquant

import "testing"

func TestUsageOptionsRestrictFunctionAndDates(t *testing.T) {
	options, err := usageOptions("EM_CSD", "2026-11-01", "2026-11-13")
	if err != nil || options != "StartDate=2026-11-01,EndDate=2026-11-13" {
		t.Fatalf("valid usage options: %v", err)
	}
	for _, v := range []struct{ fn, from, to string }{
		{"", "2026-11-01", "2026-11-13"}, {"EM_CSD,EM_CSS", "2026-11-01", "2026-11-13"},
		{"EM_CSS", "2026-11-01,foo=1", "2026-11-13"}, {"EM_CSD", "2026-11-13", "2026-11-01"},
		{"EM_CSD", "2020-01-01", "2026-11-01"},
	} {
		if _, err := usageOptions(v.fn, v.from, v.to); err == nil {
			t.Fatal("invalid usage query accepted")
		}
	}
}
