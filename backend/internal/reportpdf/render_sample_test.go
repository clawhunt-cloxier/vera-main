package reportpdf

import (
	"encoding/json"
	"os"
	"testing"
)

// TestRenderSample typesets a report fetched from the API. Set REPORTPDF_REPORT, REPORTPDF_SERIES (optional) and REPORTPDF_OUT.
func TestRenderSample(t *testing.T) {
	rp, sp, out := os.Getenv("REPORTPDF_REPORT"), os.Getenv("REPORTPDF_SERIES"), os.Getenv("REPORTPDF_OUT")
	if rp == "" || out == "" {
		t.Skip("REPORTPDF_REPORT and REPORTPDF_OUT not set")
	}
	b, err := os.ReadFile(rp)
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	series := map[string]Series{}
	if sp != "" {
		sb, err := os.ReadFile(sp)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(sb, &series); err != nil {
			t.Fatal(err)
		}
	}
	pdf, err := Render(r, series)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d bytes", len(pdf))
}
