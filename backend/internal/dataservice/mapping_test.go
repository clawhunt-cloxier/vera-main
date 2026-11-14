package dataservice

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMappings(t *testing.T) {
	dir := t.TempDir()
	yaml := `provider: vendor_a
provider_dataset: daily_quote
target: mkt.daily_bar
version: 1
fields:
  - target: close
    source: $.close_px
    unit: CNY
    transform: decimal
  - target: amount
    source: $.turnover
    unit: CNY_10K
    transform: scale_to_base
  - target: trade_date
    source: $.dt
    transform: date_yyyymmdd
basis:
  adjust: none
`
	if err := os.MkdirAll(filepath.Join(dir, "vendor_a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor_a", "daily_quote.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMappings(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := m["vendor_a/daily_quote"]
	if !ok {
		t.Fatal("mapping not found")
	}
	if spec.Target != "mkt.daily_bar" {
		t.Fatalf("target = %q", spec.Target)
	}
	if len(spec.Fields) != 3 {
		t.Fatalf("fields = %d", len(spec.Fields))
	}
}

func TestApplyMapping(t *testing.T) {
	spec := &MappingSpec{
		Provider:        "vendor_a",
		ProviderDataset: "daily_quote",
		Target:          "mkt.daily_bar",
		Version:         1,
		Fields: []FieldMap{
			{Target: "close", Source: "$.close_px", Unit: "CNY", Transform: "decimal"},
			{Target: "amount", Source: "$.turnover", Unit: "CNY_10K", Transform: "scale_to_base"},
			{Target: "trade_date", Source: "$.dt", Transform: "date_yyyymmdd"},
		},
	}
	raw := map[string]any{
		"close_px": "100.50",
		"turnover": "1234.56",
		"dt":       "20251231",
	}
	payload, issues, err := ApplyMapping(spec, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if payload["close"] != "100.5" {
		t.Fatalf("close = %v", payload["close"])
	}
	if payload["amount"] != "12345600" {
		t.Fatalf("amount (scaled from CNY_10K) = %v, want 12345600", payload["amount"])
	}
	if payload["trade_date"] != "2025-12-31" {
		t.Fatalf("trade_date = %v", payload["trade_date"])
	}
}

func TestApplyMappingMissingField(t *testing.T) {
	spec := &MappingSpec{
		Fields: []FieldMap{
			{Target: "close", Source: "$.close_px", Transform: "decimal"},
			{Target: "missing_field", Source: "$.nonexistent", Transform: "decimal"},
		},
	}
	raw := map[string]any{"close_px": "100"}
	_, issues, err := ApplyMapping(spec, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	if issues[0].Fatal {
		t.Fatal("missing optional field should not be fatal")
	}
}

func TestApplyMappingFatalTransform(t *testing.T) {
	spec := &MappingSpec{
		Fields: []FieldMap{
			{Target: "close", Source: "$.close_px", Transform: "decimal"},
		},
	}
	raw := map[string]any{"close_px": "not_a_number_for_sure"}
	_, issues, err := ApplyMapping(spec, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	if !issues[0].Fatal {
		t.Fatal("transform error should be fatal")
	}
	if qualityStateFromIssues(issues) != "QUARANTINED" {
		t.Fatal("fatal issues should yield QUARANTINED")
	}
}

func TestApplyMappingIntegerRejectsNonInteger(t *testing.T) {
	spec := &MappingSpec{
		Fields: []FieldMap{
			{Target: "count", Source: "$.count", Transform: "integer"},
		},
	}
	// 1.9 must not silently truncate to 1.
	raw := map[string]any{"count": "1.9"}
	_, issues, err := ApplyMapping(spec, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || !issues[0].Fatal {
		t.Fatalf("expected 1 fatal issue for non-integer 1.9, got %v", issues)
	}

	// 100.0 is a whole-number float, should be accepted.
	raw2 := map[string]any{"count": "100.0"}
	payload, issues2, err := ApplyMapping(spec, raw2)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues2) != 0 {
		t.Fatalf("expected no issues for 100.0, got %v", issues2)
	}
	if payload["count"] != int64(100) {
		t.Fatalf("count = %v, want 100", payload["count"])
	}

	// Plain integer string accepted.
	raw3 := map[string]any{"count": "42"}
	payload3, _, err := ApplyMapping(spec, raw3)
	if err != nil {
		t.Fatal(err)
	}
	if payload3["count"] != int64(42) {
		t.Fatalf("count = %v, want 42", payload3["count"])
	}
}

func TestApplyMappingIntegerBoundaries(t *testing.T) {
	spec := &MappingSpec{
		Fields: []FieldMap{
			{Target: "n", Source: "$.n", Transform: "integer"},
		},
	}
	cases := []struct {
		input string
		fatal bool
		want  int64
	}{
		// Exact int64 boundaries must be accepted.
		{"9223372036854775807", false, 9223372036854775807},   // MaxInt64
		{"-9223372036854775808", false, -9223372036854775808}, // MinInt64
		// Values that overflow int64 must be rejected.
		{"9223372036854775808", true, 0},  // MaxInt64+1
		{"-9223372036854775809", true, 0}, // MinInt64-1
		// Decimal-exact whole numbers that float64 would corrupt must be accepted correctly.
		// float64 rounds "9007199254740993.0" to 9007199254740992; decimal keeps the right value.
		{"9007199254740993.0", false, 9007199254740993},
		// A value that looks close to 1 but has fractional digits must be rejected.
		{"1.00000000000000001", true, 0},
	}
	for _, tc := range cases {
		raw := map[string]any{"n": tc.input}
		payload, issues, err := ApplyMapping(spec, raw)
		if err != nil {
			t.Fatalf("input %q: unexpected error %v", tc.input, err)
		}
		if tc.fatal {
			if len(issues) != 1 || !issues[0].Fatal {
				t.Errorf("input %q: expected 1 fatal issue, got %v", tc.input, issues)
			}
		} else {
			if len(issues) != 0 {
				t.Errorf("input %q: unexpected issues %v", tc.input, issues)
			}
			if payload["n"] != tc.want {
				t.Errorf("input %q: got %v, want %d", tc.input, payload["n"], tc.want)
			}
		}
	}
}

func TestQualityStateFromIssues(t *testing.T) {
	if s := qualityStateFromIssues(nil); s != "PASS" {
		t.Fatalf("nil → %s, want PASS", s)
	}
	if s := qualityStateFromIssues([]QualityIssue{{Message: "warn"}}); s != "WARN" {
		t.Fatalf("warn → %s, want WARN", s)
	}
	if s := qualityStateFromIssues([]QualityIssue{{Message: "warn", Fatal: true}}); s != "QUARANTINED" {
		t.Fatalf("fatal → %s, want QUARANTINED", s)
	}
}
