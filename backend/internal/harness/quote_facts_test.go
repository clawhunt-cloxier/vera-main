package harness

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
)

func TestQuoteFactPreservesSourcePrecisionAndLimits(t *testing.T) {
	entity, task, stage := uuid.New(), uuid.New(), uuid.New()
	event := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	q := emquant.PublishedQuote{StageID: stage, EntityID: entity, ProviderID: "synthetic-provider", Code: "A.SH", Price: "10.125",
		EventAt: event, ReceivedAt: event.Add(time.Second), AvailableAt: event.Add(2 * time.Second), LatencyMode: "unknown", MarketState: "unknown",
		RawSHA256: strings.Repeat("a", 64), Locators: map[string]string{"NOW": "$.data.Values[1]", "TIME": "$.data.Values[0]"}}
	view := emquant.QuoteView{Cutoff: event.Add(3 * time.Second), Requirement: emquant.QuoteLatestAvailable, Quotes: []emquant.PublishedQuote{q}}
	sheet, ev, err := quoteFactSheet(task, entity, 1, view)
	if err != nil || len(sheet.Facts) != 1 || sheet.Facts[0].Display != "10.125" || sheet.Facts[0].Unit != "元" || len(sheet.Limitations) != 2 || ev.ID != stage || ev.RawLocator != q.Locators["NOW"] || ev.TimeLocator != q.Locators["TIME"] {
		t.Fatalf("quote fact or provenance: %v", err)
	}
	claim := check.Claim{Kind: "fact", Text: "行情最新价为10.125元。", Refs: []string{"F1"}, Numbers: []check.Number{{Text: "10.125", Fact: "F1"}}}
	if failure := check.Check(claim, check.Context{Sheet: sheet, Allowed: map[string]bool{"F1": true}, Kinds: []string{"fact"}, AsOf: view.Cutoff}); failure != nil {
		t.Fatalf("grounded quote fact rejected: %v", failure)
	}
	claim.Text = "建议买入，行情最新价为10.125元。"
	if failure := check.Check(claim, check.Context{Sheet: sheet, Allowed: map[string]bool{"F1": true}, Kinds: []string{"fact"}, AsOf: view.Cutoff}); failure == nil {
		t.Fatal("quote fact bypassed forbidden-expression rules")
	}
	view.Requirement = emquant.QuoteRealtimeTrade
	if _, _, err := quoteFactSheet(task, entity, 1, view); err == nil {
		t.Fatal("unknown quote satisfied realtime requirement")
	}
	view.Requirement = emquant.QuoteLatestAvailable
	view.Quotes[0].AvailableAt = view.Cutoff.Add(time.Second)
	if _, _, err := quoteFactSheet(task, entity, 1, view); err == nil {
		t.Fatal("future quote entered fact sheet")
	}
}
