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
