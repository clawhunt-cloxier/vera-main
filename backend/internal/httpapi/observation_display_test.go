package httpapi

import (
	"slices"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func TestDailyTradeReportUnits(t *testing.T) {
	for _, tc := range []struct {
		version    string
		title      string
		wantAmount string
	}{
		{harness.CloseUnifiedVersion, "Choice 日成交观察", "0.000105"},
		{emquant.ObservationBriefVersion, "Choice 日成交观察（非实时）", "0.00"},
		{harness.MixedObservationVersion, "Choice 日成交观察（非实时）", "0.00"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			trades := facts.Table{
				Title:   tc.title,
				Columns: []string{"厂商返回日期", "成交量（股）", "成交额（元）", "换手率（%）"},
				Rows: [][]*string{
					{facts.Ptr("2026-11-20"), facts.Ptr("101"), facts.Ptr("1.05"), facts.Ptr("1.6")},
					{facts.Ptr("2026-11-23"), nil, facts.Ptr("10000"), nil},
				},
			}
			price := facts.Table{Columns: []string{"交易日", "收盘价（元）"}, Rows: [][]*string{{facts.Ptr("2026-11-20"), facts.Ptr("12.34")}}}
			d := harness.Document{SkillVersion: tc.version, Sections: []harness.DocSection{{Tables: []facts.Table{trades, price}}}}
			for range 2 {
				if err := formatDailyTradeTables(&d); err != nil {
					t.Fatal(err)
				}
			}
			got := d.Sections[0].Tables[0]
			if !slices.Equal(got.Columns, []string{"厂商返回日期", "成交量（手）", "成交额（万元）", "换手率（%）"}) {
				t.Fatalf("display columns: %v", got.Columns)
			}
			row := got.Rows[0]
			if *row[1] != "1.01" || *row[2] != tc.wantAmount || *row[3] != "1.6" {
				t.Fatalf("display values: %s, %s, %s", *row[1], *row[2], *row[3])
			}
			if got.Rows[1][1] != nil || *got.Rows[1][2] != "1.00" || got.Rows[1][3] != nil {
				t.Fatal("missing volume or turnover, or amount scaling changed")
			}
			if gotPrice := d.Sections[0].Tables[1]; gotPrice.Columns[1] != "收盘价（元）" || *gotPrice.Rows[0][1] != "12.34" {
				t.Fatal("the quoted price changed")
			}
		})
	}
}
