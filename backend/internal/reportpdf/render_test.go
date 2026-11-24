package reportpdf

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"testing"
)

func sampleReport(nClaims int) (Report, map[string]Series) {
	r := Report{Title: "测试公司 深度研究", SkillID: "deep_research", Subject: Subject{Name: "测试公司", Symbol: sp("000001.TEST")},
		DataAsOf: "2026-06-30 20:00（北京时间）", ResultKind: "limited", Synthetic: true, Version: 1, DeliveredAt: "2026-11-07T06:22:00+08:00",
		Limitations: []string{"S2 外部观点：已接入数据不含研报"},
		Sources:     []Source{{Provider: "演示数据", DatasetID: "fin.statement", UpdatedAt: "2026-05-15T18:00:00+08:00"}},
		Disclaimer:  "仅供研究参考，不构成投资建议。"}
	sec := Section{ID: "S1", Title: "核心要点", Status: "ok"}
	for i := 0; i < nClaims; i++ {
		ref := fmt.Sprintf("S1.%d", i+1)
		kind := kindOrder[i%len(kindOrder)]
		c := Claim{Ref: ref, Kind: kind, Text: fmt.Sprintf("2025年度营业收入%d.00亿元，同比增长33.00%%；证据不足时明确说明缺失，不用流畅文字代替依据。", 100+i)}
		if kind == "scenario" {
			c.Scenario = &Scenario{Trigger: "增速延续", Invalidation: "增速转负"}
		}
		r.Claims = append(r.Claims, c)
		sec.ClaimRefs = append(sec.ClaimRefs, ref)
	}
	sec.Tables = []Table{{Title: "主要财务数据", Columns: []string{"指标", "2024", "2025"},
		Rows: [][]*string{{sp("营业收入"), sp("98.37"), sp("130.83")}, {sp("归母净利润"), nil, sp("12.56")}}}}
	sec.Charts = []Chart{{SeriesID: "rev", Kind: "bar", Title: "营收"}, {SeriesID: "px", Kind: "line", Title: "价格"}}
	omitted := Section{ID: "S2", Title: "外部观点", Status: "omitted", Note: sp("已接入数据不含研报")}
	r.Sections = []Section{sec, omitted}
	series := map[string]Series{
		"rev": {ID: "rev", Title: "营业收入", Kind: "bar", X: "report_period", Unit: "亿元",
			Lines: []SeriesLine{{Name: "营业收入", Points: [][2]*string{{sp("2024-12-31"), sp("98.37")}, {sp("2025-12-31"), sp("130.83")}}}}},
		"px": {ID: "px", Title: "收盘价", Kind: "line", X: "trade_date", Unit: "元",
			Lines: []SeriesLine{{Name: "收盘价", Points: [][2]*string{{sp("2026-06-01"), sp("10.5")}, {sp("2026-06-02"), nil}, {sp("2026-06-03"), sp("11.2")}}}}},
	}
	return r, series
}

func mustRender(t *testing.T, r Report, s map[string]Series) []byte {
	t.Helper()
	out, err := Render(r, s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) || !bytes.Contains(out[max(0, len(out)-64):], []byte("%%EOF")) {
		t.Fatalf("not a complete PDF (%d bytes)", len(out))
	}
	return out
}

func TestRenderEmbedsFontsAndSubsets(t *testing.T) {
	r, s := sampleReport(8)
	out := mustRender(t, r, s)
	if !bytes.Contains(out, []byte("SourceHanSansCN")) {
		t.Fatal("Source Han Sans is not embedded")
	}
	if len(out) < 10_000 || len(out) > 2<<20 {
		t.Fatalf("size %d: fonts must be subset, not embedded whole", len(out))
	}
}

func TestRenderPaginates(t *testing.T) {
	r1, s1 := sampleReport(2)
	r2, s2 := sampleReport(150)
	short, long := mustRender(t, r1, s1), mustRender(t, r2, s2)
	if len(long) <= len(short) {
		t.Fatalf("150 claims (%d bytes) should be larger than 2 claims (%d bytes)", len(long), len(short))
	}
	if n := bytes.Count(long, []byte("/Type /Page\n")) + bytes.Count(long, []byte("/Type/Page/")); n > 0 && n < 4 {
		t.Fatalf("150 claims produced only %d pages", n)
	}
}

func TestRenderEmptyReport(t *testing.T) {
	mustRender(t, Report{Title: "空报告", SkillID: "stock_analysis", ResultKind: "full"}, nil)
}

func TestRenderMissingSeriesAndNilCells(t *testing.T) {
	r, _ := sampleReport(3)
	mustRender(t, r, nil) // charts without data are skipped, nil table cells print as a dash
}

func TestChartValuesAlignPeriodsAndPreserveMissingValues(t *testing.T) {
	dates, values, low, high := chartValues(Series{Lines: []SeriesLine{
		{Points: [][2]*string{{sp("2025-12-31"), sp("19.50")}, {sp("2024-12-31"), sp("0")}}},
		{Points: [][2]*string{{sp("2023-12-31"), sp("-2.80")}, {sp("2025-12-31"), sp("999")}, {sp("2025-12-31"), nil}}},
	}})
	if !slices.Equal(dates, []string{"2023-12-31", "2024-12-31", "2025-12-31"}) {
		t.Fatalf("periods: %v", dates)
	}
	if !math.IsNaN(values[0][0]) || values[0][1] != 0 || values[0][2] != 19.50 ||
		values[1][0] != -2.80 || !math.IsNaN(values[1][1]) || !math.IsNaN(values[1][2]) {
		t.Fatalf("aligned values: %v", values)
	}
	if low != -2.80 || high != 19.50 {
		t.Fatalf("chart domain: %v to %v", low, high)
	}
}
