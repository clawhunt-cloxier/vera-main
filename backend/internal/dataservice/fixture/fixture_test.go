package fixture

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

const manifest = "../../../../tests/fixtures/demo-universe.json"

func TestGenerationIsDeterministicAndCoversProfiles(t *testing.T) {
	a, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New(manifest)
	ra, _ := a.Records(context.Background())
	rb, _ := b.Records(context.Background())
	if len(ra) != len(rb) || len(ra) < 10000 {
		t.Fatalf("records %d vs %d", len(ra), len(rb))
	}
	for i := range ra {
		if ra[i].RecordKey != rb[i].RecordKey || ra[i].Hash() != rb[i].Hash() {
			t.Fatalf("non-deterministic at %d: %s", i, ra[i].RecordKey)
		}
	}
	count := map[string]map[string]int{}
	for _, r := range ra {
		if count[r.DatasetID] == nil {
			count[r.DatasetID] = map[string]int{}
		}
		count[r.DatasetID][r.EntityCode]++
		if r.AvailableAt.IsZero() || r.SourceTier == "" || r.Origin == "" {
			t.Fatalf("incomplete record %s", r.RecordKey)
		}
	}
	if count["fin.statement"]["DM0006.DEMO"] != 0 {
		t.Fatal("DM0006 must have no statements")
	}
	if count["mkt.valuation_daily"]["DM0003.DEMO"] != 0 || count["flow.margin"]["DM0003.DEMO"] != 0 {
		t.Fatal("DM0003 must lack valuation and margin")
	}
	if n := count["mkt.daily_bar"]["DM0005.DEMO"]; n != 40 {
		t.Fatalf("DM0005 bars = %d, want 40", n)
	}
	for _, r := range ra {
		if r.DatasetID == "fin.statement" && r.EntityCode == "DM0004.DEMO" && r.Payload["item_code"] == "IS.operating_cost" {
			t.Fatal("financial company must not have operating cost")
		}
		if r.DatasetID == "mkt.daily_bar" {
			for _, f := range []string{"open", "close", "amount"} {
				if _, err := decimal.NewFromString(r.Payload[f].(string)); err != nil {
					t.Fatalf("%s %s: %v", r.RecordKey, f, err)
				}
			}
		}
	}
}
