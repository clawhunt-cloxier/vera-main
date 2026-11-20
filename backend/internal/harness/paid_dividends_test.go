package harness

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func TestCashDividendFactsKeepImplementedPeriodsSeparate(t *testing.T) {
	b := closeTestBuilder()
	newEvent := func(period, pay, per10, base, total string) uuid.UUID {
		r := addCloseRecord(b, "corp.dividend", map[string]any{"report_period": period, "fiscal_year": period[:4],
			"implementation_date": period, "payment_date": pay, "cash_per_10_shares": per10, "base_shares": base, "total_cash": total})
		return r.ID
	}
	interim := newEvent("2025-06-30", "2025-09-15", "3.2", "1000", "320")
	final := newEvent("2025-12-31", "2026-05-15", "5.5", "990", "544.5")
	newEvent("2026-06-30", "2026-11-25", "4", "900", "360")
	newEvent("2025-09-30", "2025-12-12", "3", "100", "300000")
	e := b.B.Subject.ID
	if b.corp(e, "corp") {
		t.Fatal("legacy reader treated an event as a fiscal year total")
	}
	b.paidDividendEvents = true
	if !b.corp(e, "corp") {
		t.Fatal("missing cash events")
	}
	tables := b.S.Tables["S8"]
	if len(tables) != 1 || !slices.Equal(tables[0].Evidence, []uuid.UUID{final, interim}) || len(tables[0].Rows) != 2 || *tables[0].Rows[0][5] != "544.5" {
		t.Fatalf("event table %+v", tables)
	}
	if len(b.S.Facts) != 4 || len(b.S.Calcs) != 2 || len(b.S.Limitations) != 1 || !strings.Contains(b.S.Limitations[0], "全年度") {
		t.Fatalf("facts %v calculations %v limitations %v", b.S.Facts, b.S.Calcs, b.S.Limitations)
	}
	for _, fact := range b.S.Facts {
		if strings.Contains(fact.Label, "总额") || len(fact.Evidence) != 1 {
			t.Fatalf("event labelled as fiscal aggregate or mixed evidence: %+v", fact)
		}
		if fact.Kind == facts.KindComputed && fact.CalcID == nil {
			t.Fatal("computed cash lacks calculation")
		}
	}
	for i, calc := range b.S.Calcs {
		if len(calc.Inputs) != 2 || calc.Inputs[0].EvidenceID != tables[0].Evidence[i] || calc.Inputs[1].EvidenceID != tables[0].Evidence[i] {
			t.Fatalf("missing calculation operands: %+v", calc)
		}
	}
}

func TestCashDividendLegacyAnnualAggregate(t *testing.T) {
	b := closeTestBuilder()
	rec := addCloseRecord(b, "corp.dividend", map[string]any{"fiscal_year": "2025", "cash_per_10_shares": "4", "total_cash": "400"})
	if !b.corp(b.B.Subject.ID, "corp") || len(b.S.Facts) != 2 || b.S.Facts[1].Label != "现金分红总额" || b.S.Facts[1].Evidence[0] != rec.ID {
		t.Fatalf("legacy aggregate: %+v", b.S.Facts)
	}
}
