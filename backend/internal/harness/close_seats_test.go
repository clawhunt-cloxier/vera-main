package harness

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type seatEvaluation struct {
	ID      string   `json:"id"`
	Days    []string `json:"days"`
	Records []struct {
		Dataset string         `json:"dataset"`
		Payload map[string]any `json:"payload"`
	} `json:"records"`
	Availability string   `json:"availability"`
	Listed       *int64   `json:"listed"`
	Partial      bool     `json:"partial"`
	ListingRows  int      `json:"listing_rows"`
	SeatRows     int      `json:"seat_rows"`
	Amounts      []string `json:"amounts"`
}

func TestCloseSeatEvaluations(t *testing.T) {
	data, err := os.ReadFile("../../../tests/fixtures/close-seat-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version string           `json:"skill_version"`
		Cases   []seatEvaluation `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil || fixture.Version != CloseDisclosureVersion || len(fixture.Cases) < 6 {
		t.Fatalf("invalid seat fixture: %v", err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			b := closeTestBuilder()
			b.B.Days = tc.Days
			b.disclosedSeats = true
			var records []dataservice.StoredRecord
			for _, item := range tc.Records {
				records = append(records, addCloseRecord(b, item.Dataset, item.Payload))
			}
			e := b.B.Subject.ID
			n := len(tc.Days)
			d := b.B.seatDisclosures(e, n)
			if got := d.availability(); got.State != tc.Availability {
				t.Fatalf("availability %+v", got)
			}
			b.seats(e, n, "flow.seats", "S4")
			var count bool
			var amounts []string
			for _, f := range b.S.Facts {
				if strings.Contains(f.Label, "上榜日数") || f.Label == "龙虎榜上榜交易日数" {
					count = true
					if tc.Listed == nil || f.Value.IntPart() != *tc.Listed || tc.Partial != strings.Contains(f.Label, "已核实") {
						t.Fatalf("unexpected count %+v", f)
					}
					var ids []uuid.UUID
					for _, day := range d.days {
						ids = append(ids, day.coverage.ID)
					}
					if !slices.Equal(ids, f.Evidence) || len(ids) == 0 {
						t.Fatalf("missing query proof: %+v", f)
					}
				}
				if strings.Contains(f.Label, "机构专用") {
					amounts = append(amounts, f.Value.String())
					if f.CalcID == nil || len(f.Evidence) == 0 {
						t.Fatal("no traceable ranking calculation")
					}
				}
			}
			if count != (tc.Listed != nil) || !slices.Equal(amounts, tc.Amounts) {
				t.Fatalf("count=%v, amounts=%v", count, amounts)
			}
			listings, seats := 0, 0
			for _, table := range b.S.Tables["S4"] {
				if len(table.Rows) != len(table.Evidence) {
					t.Fatal("row without evidence")
				}
				if strings.Contains(table.Title, "上榜披露") {
					listings = len(table.Rows)
					continue
				}
				seats = len(table.Rows)
				for i, ref := range table.Evidence {
					index := slices.IndexFunc(records, func(r dataservice.StoredRecord) bool { return r.ID == ref })
					if index < 0 {
						t.Fatal("unknown row reference")
					}
					record := records[index]
					if str(record, "edition_id") == "obsolete" || !strings.Contains(*table.Rows[i][3], seatSide(str(record, "side"))) {
						t.Fatal("incorrect seat edition or board")
					}
					for j, field := range []string{"buy_amount", "sell_amount"} {
						value, ok := dec(record, field)
						cell := table.Rows[i][5+j]
						if (cell != nil) != ok || (ok && *cell != value.Shift(-4).StringFixed(2)) {
							t.Fatalf("incorrect %s cell: %v", field, cell)
						}
					}
				}
			}
			if listings != tc.ListingRows || seats != tc.SeatRows {
				t.Fatalf("listing rows=%d, seat rows=%d", listings, seats)
			}
		})
	}
}

func TestCloseDisclosureVersionResolution(t *testing.T) {
	e := &Engine{Skills: TuShareSkills()}
	for _, tc := range []struct {
		skill, version string
		allowed        bool
	}{
		{"stock_analysis", CloseDisclosureVersion, true}, {"capital_flow", CloseDisclosureVersion, true},
		{"deep_research", CloseDisclosureVersion, false}, {"stock_analysis", CloseCompanyVersion, true},
		{"capital_flow", CloseResearchVersion, true}, {"capital_flow", CloseCompanyVersion, false},
	} {
		sk := e.SkillVersion(tc.skill, tc.version)
		if (sk != nil) != tc.allowed || (sk != nil && sk.Version != tc.version) {
			t.Errorf("skill %s version %s: %+v", tc.skill, tc.version, sk)
		}
	}
	if (&Engine{Skills: Skills()}).SkillVersion("capital_flow", CloseResearchVersion) != nil {
		t.Fatal("fixture resolved TuShare history")
	}
}
