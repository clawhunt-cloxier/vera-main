package harness

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func TestUnifiedCloseKeepsLegacyRequirements(t *testing.T) {
	e := &Engine{Skills: UnifiedCloseSkills()}
	for _, tc := range []struct {
		id       string
		version  string
		required []string
	}{
		{"stock_analysis", CloseUnifiedVersion, []string{"S2", "S6"}},
		{"stock_analysis", CloseDisclosureVersion, []string{"S2", "S3", "S4", "S6", "S10"}},
		{"stock_analysis", CloseCompanyVersion, []string{"S2", "S3", "S4", "S6", "S10"}},
		{"capital_flow", CloseUnifiedVersion, []string{"S2", "S3", "S8"}},
		{"capital_flow", CloseDisclosureVersion, []string{"S2", "S3", "S8"}},
		{"capital_flow", CloseResearchVersion, []string{"S2", "S3", "S8"}},
		{"deep_research", CloseUnifiedVersion, []string{"S2", "S11"}},
		{"deep_research", CloseCompanyVersion, []string{"S2", "S4", "S5", "S8", "S10"}},
	} {
		sk := e.SkillVersion(tc.id, tc.version)
		if sk == nil || sk.Version != tc.version {
			t.Fatalf("%s %s was not resolved", tc.id, tc.version)
		}
		var required []string
		for _, s := range sk.Sections(Params{Mode: "company"}) {
			if s.Required {
				required = append(required, s.ID)
			}
		}
		if !slices.Equal(required, tc.required) {
			t.Errorf("%s %s required = %v, want %v", tc.id, tc.version, required, tc.required)
		}
	}
	if e.SkillVersion("deep_research", CloseDisclosureVersion) != nil ||
		(&Engine{Skills: Skills()}).SkillVersion("stock_analysis", CloseUnifiedVersion) != nil {
		t.Fatal("unsupported service resolved a real research version")
	}
}

func unifiedCloseTestBuilder() *builder {
	b := closeTestBuilder()
	for _, day := range []string{"2026-10-27", "2026-10-28", "2026-10-29"} {
		b.B.Days = append(b.B.Days, day)
		b.B.global["ref.trading_calendar"] = append(b.B.global["ref.trading_calendar"], dataservice.StoredRecord{
			Payload: map[string]any{"trade_date": day, "is_open": true},
		})
	}
	addCloseRecord(b, "ref.security", map[string]any{"list_date": "2000-10-01"})
	addCloseRecord(b, "ref.industry", map[string]any{
		"valid_from": "2020-01-01", "taxonomy": "SW", "level1_name": "食品饮料", "level2_name": "白酒",
	})
	addCloseRecord(b, "mkt.daily_bar", map[string]any{"trade_date": "2026-10-27", "close": "18", "adj_factor": "1", "amount": "90"})
	addCloseRecord(b, "mkt.daily_bar", map[string]any{"trade_date": "2026-10-28", "close": "20", "adj_factor": "1", "amount": "70"})
	return b
}

func TestUnifiedCloseCoreRejectsStaleOrUnusableMarket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		update   func(*Bundle, map[string]any)
		wantCore bool
	}{
		{"previous completed session", func(*Bundle, map[string]any) {}, true},
		{"stale", func(_ *Bundle, row map[string]any) { row["trade_date"] = "2026-10-26" }, false},
		{"suspended", func(_ *Bundle, row map[string]any) { row["suspended"] = true }, false},
		{"invalid price", func(_ *Bundle, row map[string]any) { row["close"] = "0" }, false},
		{"uncompleted session", func(b *Bundle, row map[string]any) {
			b.AsOf = time.Date(2026, 10, 29, 12, 0, 0, 0, cst)
			row["trade_date"] = "2026-10-29"
			// No positive observations on the two sessions eligible for this plan.
			b.Rows("mkt.daily_bar", b.Subject.ID)[0].Payload["close"] = "0"
		}, false},
		{"unknown session", func(b *Bundle, row map[string]any) {
			b.Days = b.Days[:2]
			row["trade_date"] = "2026-10-29"
			b.Rows("mkt.daily_bar", b.Subject.ID)[0].Payload["close"] = "0"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := unifiedCloseTestBuilder()
			tc.update(b.B, b.B.Rows("mkt.daily_bar", b.B.Subject.ID)[1].Payload)
			for skill, section := range map[string]string{"stock_analysis": "S6", "capital_flow": "S2", "deep_research": "S11"} {
				av := map[string]Availability{"S3": unavailable("未取得融资披露")}
				applyUnifiedCloseCoverage(b.B, skill, av)
				if (av[section].State == "available") != tc.wantCore {
					t.Errorf("%s %s = %+v, want executable = %t", skill, section, av[section], tc.wantCore)
				}
				if av["S3"].State != "unavailable" {
					t.Errorf("%s altered blocked financial or margin section", skill)
				}
			}
		})
	}
}

func TestUnifiedCloseCompanyWithoutFilingsHasEvidenceOnlyCore(t *testing.T) {
	registry := UnifiedCloseSkills()
	original := map[string]Availability{"S2": unavailable("缺少年报原文"), "S3": unavailable("缺少报表"), "S4": unavailable("缺少报表")}
	for _, id := range []string{"stock_analysis", "deep_research"} {
		b := unifiedCloseTestBuilder()
		av := maps.Clone(original)
		applyUnifiedCloseCoverage(b.B, id, av)
		if av["S2"].State != "limited" || av["S3"] != original["S3"] || av["S4"] != original["S4"] {
			t.Fatalf("%s did not preserve unavailable filing-dependent coverage: %+v", id, av)
		}
		p := Params{Window: 20, Mode: "company"}
		registry[id].Compute(b, p)
		for _, s := range registry[id].Sections(p) {
			if !s.Required {
				continue
			}
			eligible := allowed(b.S, s)
			label := "最近收盘价"
			if s.ID == "S2" {
				label = "上市日期"
			}
			if !slices.ContainsFunc(eligible, func(f facts.Fact) bool { return f.Label == label && len(f.Evidence) == 1 }) {
				t.Errorf("%s %s missing evidence for %s", id, s.ID, label)
			}
		}
		if len(b.S.Select([]string{"fin.annual", "fin.quality", "text.business_overview", "text.risk_factors"})) != 0 {
			t.Errorf("%s computed an unavailable annual fact", id)
		}
		if id == "deep_research" && !slices.ContainsFunc(b.S.Charts, func(c facts.Series) bool { return c.Section == "S11" && len(c.Evidence) > 0 }) {
			t.Error("company deep research did not attach its evidence-backed price chart")
		}
		for _, ds := range []string{"ref.security", "ref.industry"} {
			saved := b.B.byKey[key(ds, b.B.Subject.ID)]
			delete(b.B.byKey, key(ds, b.B.Subject.ID))
			applyUnifiedCloseCoverage(b.B, id, av)
			b.B.byKey[key(ds, b.B.Subject.ID)] = saved
			if av["S2"].State != "unavailable" {
				t.Errorf("%s company section accepted without %s", id, ds)
			}
		}
	}
}
