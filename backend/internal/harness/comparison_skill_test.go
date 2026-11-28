package harness

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func TestStockComparisonAlignsPricesAndCalculations(t *testing.T) {
	days := []string{"2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19", "2026-11-20"}
	at := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	anchor, peer, another, sparse := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	entities := map[uuid.UUID]dataservice.EntityRow{
		anchor:  {ID: anchor, Kind: "stock", Name: "示例甲", Code: "DEMO-A"},
		peer:    {ID: peer, Kind: "stock", Name: "示例乙", Code: "DEMO-B"},
		another: {ID: another, Kind: "stock", Name: "示例丁", Code: "DEMO-D"},
		sparse:  {ID: sparse, Kind: "stock", Name: "示例丙", Code: "DEMO-C"},
	}
	var records []dataservice.StoredRecord
	add := func(id uuid.UUID, day int, close, factor string, suspended bool) {
		records = append(records, dataservice.StoredRecord{ID: uuid.New(), DatasetID: "mkt.daily_bar", EntityID: &id,
			Payload: map[string]any{"trade_date": days[day], "close": close, "adj_factor": factor, "suspended": suspended}})
	}
	for _, day := range days {
		records = append(records, dataservice.StoredRecord{ID: uuid.New(), DatasetID: "ref.trading_calendar", Payload: map[string]any{"trade_date": day, "is_open": true}})
	}
	add(anchor, 0, "10", "1", false)
	add(anchor, 1, "11", "1", false)
	add(anchor, 2, "12", "1", false)
	add(anchor, 3, "6.25", "2", false)
	add(anchor, 4, "6.5", "2", false)
	add(peer, 1, "20", "1", false)
	add(peer, 2, "21", "1", false)
	add(peer, 3, "21", "1", false)
	add(peer, 4, "22", "1", false)
	add(another, 1, "20", "1", false)
	add(another, 2, "20", "1", false)
	add(another, 3, "22", "1", false)
	add(another, 4, "24", "1", false)
	add(sparse, 0, "2", "1", true)
	add(sparse, 1, "2", "", false)
	add(sparse, 2, "2", "1", false)
	add(sparse, 4, "3", "1", false)
	bundle := newBundle(at, entities[anchor], entities, records)
	p := Params{Window: 5, Comparison: &ComparisonParams{OtherIDs: []uuid.UUID{peer, another, sparse}}}
	skill := stockComparisonSkill(ComparisonCloseVersion)
	sheet := facts.NewSheet(uuid.New(), 1)
	skill.Compute(&builder{S: sheet, B: bundle}, p)
	sheet.Finalize()
	if len(sheet.Charts) != 1 || len(sheet.Charts[0].Lines) != 3 || len(sheet.Tables["S2"]) != 1 || len(sheet.Calcs) != 5 {
		t.Fatalf("incomplete comparison: %+v", sheet)
	}
	table := sheet.Tables["S2"][0]
	if len(table.Rows) != 4 {
		t.Fatalf("the excluded stock must stay in the table: %+v", table)
	}
	if *table.Rows[0][1] != "11" || *table.Rows[0][2] != "6.5" || *table.Rows[0][3] != "18.18%" {
		t.Fatalf("adjusted return or unadjusted close is wrong: %+v", table)
	}
	if *table.Rows[1][3] != "10.00%" || *table.Rows[1][4] != "-8.18" ||
		*table.Rows[2][3] != "20.00%" || *table.Rows[2][4] != "1.82" || table.Rows[3][3] != nil {
		t.Fatalf("relative return must use the same endpoints and exclude inadequate coverage: %+v", table)
	}
	for _, line := range sheet.Charts[0].Lines {
		if line.Points[0][1] != nil || *line.Points[1][0] != days[1] || *line.Points[1][1] != "100.00" {
			t.Fatalf("series did not use a common baseline: %+v", line)
		}
	}
	section := skill.Sections(p)[0]
	for _, claim := range skill.ClaimsFromFacts(sheet, section) {
		if fail := check.Check(claim, check.Context{Sheet: sheet, AsOf: at, Kinds: section.Kinds, MaxChars: section.MaxChars}); fail != nil {
			t.Fatalf("comparison claim failed %s: %s (%s)", fail.Rule, fail.Reason, claim.Text)
		}
	}

	// Four valid days per stock are insufficient if their valid dates do not align.
	withoutCommon := slices.DeleteFunc(slices.Clone(records), func(r dataservice.StoredRecord) bool {
		return r.EntityID != nil && *r.EntityID == anchor && str(r, "trade_date") == days[1]
	})
	if alignComparison(newBundle(at, entities[anchor], entities, withoutCommon), p).comparable() {
		t.Fatal("80% coverage is required on shared dates")
	}
}

func TestStockComparisonPinsRankedSample(t *testing.T) {
	const sampleDay = "2026-11-20"
	anchor, peer := uuid.New(), uuid.New()
	entities := map[uuid.UUID]dataservice.EntityRow{
		anchor: {ID: anchor, Code: "600000.SH"},
		peer:   {ID: peer, Code: "600001.SH"},
	}
	hash := "ranked-input-proof"
	sample := ComparisonParams{OtherIDs: []uuid.UUID{peer}, SampleDate: sampleDay, SampleProof: hash}
	record := dataservice.StoredRecord{ID: uuid.New(), DatasetID: dataservice.ComparisonSampleDataset, EntityID: &anchor,
		RawSHA256: &hash, Payload: map[string]any{"trade_date": sampleDay, "level2_code": "801011.SI", "level2_name": "模拟二级", "peer_codes": "600001.SH"}}
	bundle := newBundle(time.Now(), entities[anchor], entities, []dataservice.StoredRecord{record})
	if name, invalid := comparisonIndustry(bundle, []uuid.UUID{anchor, peer}, sample); invalid || name != "模拟二级" {
		t.Fatalf("frozen sample not accepted: %s (%v)", name, invalid)
	}
	sample.SampleProof = "different-observation"
	if _, invalid := comparisonIndustry(bundle, []uuid.UUID{anchor, peer}, sample); !invalid {
		t.Fatal("a different observation cannot justify the original ranked sample")
	}
}

func TestStockComparisonRetainsMissingInputsAcrossSnapshot(t *testing.T) {
	ctx := t.Context()
	e, pool := testEngine(t)
	anchor, err := e.Store.EntityByCode(ctx, "DM0001.DEMO")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := e.Store.EntityByCode(ctx, "DM0004.DEMO")
	if err != nil {
		t.Fatal(err)
	}
	var sparse uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic)
		VALUES('stock','MISSING.DEMO','演示无行情',true) RETURNING id`).Scan(&sparse); err != nil {
		t.Fatal(err)
	}
	at := e.Now()
	p := Params{Window: 20, Comparison: &ComparisonParams{
		OtherIDs: []uuid.UUID{peer.ID, sparse},
		ComparisonBenchmarks: dataservice.ComparisonBenchmarks{
			BroadIndexes: []string{"000852.SH"},
		},
	}}
	collected, err := collectComparison(ctx, e, anchor, p, at, ComparisonFixtureVersion)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := e.Store.Freeze(ctx, at, collected.IDs)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := e.bundleFromSnapshot(ctx, anchor, collectOut{Snapshot: hash, Peers: collected.Peers}, at)
	if err != nil {
		t.Fatal(err)
	}
	coverage := alignComparison(frozen, p)
	if !coverage.comparable() || coverage.lineCount() != 2 || len(coverage.lines) != 4 ||
		coverage.lines[2].name != "演示无行情" || coverage.lines[2].missing != "覆盖不足" ||
		coverage.lines[3].name != "中证1000" || coverage.lines[3].missing != "暂无可用数据" {
		t.Fatalf("missing inputs lost or valid stocks dropped: %+v", coverage)
	}
}
