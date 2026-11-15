package dataservice

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

// LoadMappings reads all YAML files under dir (recursively) and returns a map keyed by
// "<provider>/<provider_dataset>" for quick lookup during normalization.
func LoadMappings(dir string) (map[string]*MappingSpec, error) {
	out := map[string]*MappingSpec{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var spec MappingSpec
		if err := yaml.Unmarshal(b, &spec); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		key := spec.Provider + "/" + spec.ProviderDataset
		out[key] = &spec
		return nil
	})
	return out, err
}

// ApplyMapping applies a MappingSpec to a raw JSON object and returns a canonical payload
// plus any quality issues. The caller decides the final QualityState based on issues.
func ApplyMapping(spec *MappingSpec, raw map[string]any) (payload map[string]any, issues []QualityIssue, err error) {
	payload = make(map[string]any, len(spec.Fields))
	for _, fm := range spec.Fields {
		val, found := jsonPath(raw, fm.Source)
		if !found || val == nil {
			issues = append(issues, QualityIssue{
				Field:   fm.Target,
				Message: fmt.Sprintf("source %q not found", fm.Source),
				Fatal:   false,
			})
			continue
		}
		converted, convErr := applyTransform(fm.Transform, fm.Unit, val)
		if convErr != nil {
			issues = append(issues, QualityIssue{
				Field:   fm.Target,
				Message: fmt.Sprintf("transform %q failed: %v", fm.Transform, convErr),
				Fatal:   true,
			})
			continue
		}
		payload[fm.Target] = converted
	}
	return payload, issues, nil
}

// jsonPath resolves a simple JSONPath expression ($.field or $.a.b.c) against a map.
func jsonPath(obj map[string]any, expr string) (any, bool) {
	parts := strings.Split(strings.TrimPrefix(expr, "$."), ".")
	var cur any = obj
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// applyTransform converts a raw value to the canonical type.
func applyTransform(transform, unit string, val any) (any, error) {
	str := fmt.Sprintf("%v", val)
	switch transform {
	case "decimal", "":
		d, err := decimal.NewFromString(str)
		if err != nil {
			// try via float
			if f, err2 := strconv.ParseFloat(str, 64); err2 == nil {
				d = decimal.NewFromFloat(f)
			} else {
				return nil, err
			}
		}
		// scale CNY_10K → CNY (multiply by 10000)
		if unit == "CNY_10K" {
			d = d.Mul(decimal.NewFromInt(10000))
		}
		return d.String(), nil
	case "scale_to_base":
		d, err := decimal.NewFromString(str)
		if err != nil {
			return nil, err
		}
		if unit == "CNY_10K" {
			d = d.Mul(decimal.NewFromInt(10000))
		}
		return d.String(), nil
	case "date_yyyymmdd":
		s := strings.ReplaceAll(str, "-", "")
		if len(s) != 8 {
			return nil, fmt.Errorf("expected YYYYMMDD, got %q", str)
		}
		t, err := time.ParseInLocation("20060102", s, time.UTC)
		if err != nil {
			return nil, err
		}
		return t.Format("2006-01-02"), nil
	case "string":
		return str, nil
	case "integer":
		// Reject non-integer values (e.g. 1.9 must not silently truncate to 1).
		// Use decimal for exact parsing: float64 loses precision beyond 2^53 and
		// cannot distinguish "9223372036854775808" from MaxInt64, or
		// "9007199254740993.0" from "9007199254740992".
		n, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			// Accept only whole-number decimals (e.g. "100.0" is fine, "1.9" is not).
			d, derr := decimal.NewFromString(str)
			if derr != nil {
				return nil, fmt.Errorf("integer: cannot parse %q: %w", str, err)
			}
			if !d.Equal(d.Truncate(0)) {
				return nil, fmt.Errorf("integer: %q is not an integer value", str)
			}
			// Check range using exact decimal comparison.
			if d.GreaterThan(decimal.NewFromInt(math.MaxInt64)) || d.LessThan(decimal.NewFromInt(math.MinInt64)) {
				return nil, fmt.Errorf("integer: %q overflows int64", str)
			}
			return d.BigInt().Int64(), nil
		}
		return n, nil
	default:
		return nil, fmt.Errorf("unknown transform %q", transform)
	}
}

// qualityStateFromIssues returns the worst QualityState implied by issues.
func qualityStateFromIssues(issues []QualityIssue) string {
	for _, q := range issues {
		if q.Fatal {
			return "QUARANTINED"
		}
	}
	if len(issues) > 0 {
		return "WARN"
	}
	return "PASS"
}

// MappingTestCase is one sample pair for CI mapping tests.
type MappingTestCase struct {
	Raw    json.RawMessage `yaml:"raw"`
	Expect json.RawMessage `yaml:"expect"`
}
