package facts

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// The compute step stores the sheet in the step ledger; a redelivered run reloads it. Nothing may be lost.
func TestSheetSurvivesStepLedgerRoundTrip(t *testing.T) {
	s := NewSheet(uuid.New(), 3)
	ev := uuid.New()
	s.Value("fin", "演示制造", "营业收入", "2025", KindReported, "合并", "CNY", decimal.RequireFromString("12000000000"), ev)
	s.Computed("fin", "演示制造", "营业收入同比", "2025", YoY(n("120"), n("90")), "yoy").Flag("small_base")
	s.Computed("fin", "演示制造", "ROE", "2025", ROE(n("1"), n("-5"), n("-3")), "roe")
	s.Charts = append(s.Charts, Series{ID: "c1", Title: "x", Section: "S3"})
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Sheet
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Facts, back.Facts) {
		t.Fatalf("facts changed:\n%+v\n%+v", s.Facts, back.Facts)
	}
	if back.Charts[0].Section != "S3" {
		t.Fatal("chart section lost")
	}
	if f := back.Note("x", "y", "z", "p", KindReported, "v"); f.ID != "F4" {
		t.Fatalf("next fact id %s after reload, want F4", f.ID)
	}
	// The public series payload keeps its contract shape: no section field.
	cb, _ := json.Marshal(s.Charts[0])
	var m map[string]any
	_ = json.Unmarshal(cb, &m)
	if _, ok := m["section"]; ok {
		t.Fatal("series JSON grew a section field")
	}
}

func TestFinalizeOrdersByGroupStably(t *testing.T) {
	s := NewSheet(uuid.New(), 1)
	s.Note("mkt", "a", "1", "", KindMarket, "x")
	s.Note("fin", "a", "2", "", KindReported, "x")
	s.Note("mkt", "a", "3", "", KindMarket, "x")
	s.Note("fin", "a", "4", "", KindReported, "x")
	s.Finalize()
	var got string
	for _, f := range s.Facts {
		got += f.ID + ":" + f.Label + " "
	}
	if got != "F1:2 F2:4 F3:1 F4:3 " {
		t.Fatalf("order %s", got)
	}
}
