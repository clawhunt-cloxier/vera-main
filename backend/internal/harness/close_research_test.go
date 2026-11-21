package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

// Explicitly synthetic canonical records used to exercise the real-close
// research rules without consuming a vendor account.
func closeTestBuilder() *builder {
	id := uuid.New()
	subject := dataservice.EntityRow{ID: id, Code: "600001.SH", Kind: "stock", Name: "测试对象", Attrs: map[string]any{"tushare_research_subject": true}}
	at := time.Date(2026, 10, 29, 16, 0, 0, 0, cst)
	return &builder{S: facts.NewSheet(uuid.New(), 1), B: newBundle(at, subject, map[uuid.UUID]dataservice.EntityRow{id: subject}, nil)}
}

func addCloseRecord(b *builder, ds string, payload map[string]any) dataservice.StoredRecord {
	r := dataservice.StoredRecord{ID: uuid.New(), EntityID: &b.B.Subject.ID, DatasetID: ds, ProviderID: "unit_test", AvailableAt: b.B.AsOf, Payload: payload}
	k := key(ds, *r.EntityID)
	b.B.byKey[k] = append(b.B.byKey[k], r)
	return r
}

func TestVendorFlowCumulativeClaimsFromFrozenSheet(t *testing.T) {
	b := closeTestBuilder()
	b.basis = map[string]string{"flow.order_size": "测试厂商按单笔成交金额分档"}
	b.B.Days = []string{"2026-10-27", "2026-10-28"}
	for _, day := range b.B.Days {
		addCloseRecord(b, "flow.order_size", map[string]any{"trade_date": day, "xl_net": "10000", "l_net": "-20000", "s_net": "30000"})
	}
	if !b.vendorFlow(b.B.Subject.ID, len(b.B.Days), "flow.vendor") || len(b.S.Facts) != 3 {
		t.Fatal("missing order-size facts")
	}
	b.S.Finalize()
	ledger, err := json.Marshal(b.S)
	if err != nil {
		t.Fatal(err)
	}
	var restored facts.Sheet
	if err := json.Unmarshal(ledger, &restored); err != nil {
		t.Fatal(err)
	}
	s7 := Section{Groups: []string{"flow.vendor"}, Kinds: []string{KComp, KFact}}
	cx := (&Engine{}).checkCtx(&taskRow{AsOf: b.B.AsOf}, &restored, s7)
	for i, text := range []string{"超大单净额累计为 2.00 万元。", "大单净额累计为 -4.00 万元。", "小单净额累计为 6.00 万元。"} {
		f := restored.Facts[i]
		if f.Kind != facts.KindVendorDerived || len(f.Evidence) != 2 {
			t.Fatalf("lost vendor provenance on %s", f.ID)
		}
		claim := check.Claim{Kind: KComp, Text: "按测试厂商分档口径，" + text, Refs: []string{f.ID}}
		if failure := check.Check(claim, cx); failure != nil {
			t.Errorf("%s: %v", f.ID, failure)
		}
		claim.Text = text
		if failure := check.Check(claim, cx); failure == nil || failure.Rule != "R-07" {
			t.Errorf("%s without vendor attribution: got %v, want R-07", f.ID, failure)
		}
	}
	// All three fields in a row share its evidence UUID. Substituting a value
	// from another field must fail even when the two-day total stays the same.
	calc := &restored.Calcs[0]
	calc.Inputs[0].Value = decimal.NewFromInt(30_000) // s_net in the first row
	calc.Inputs[1].Value = decimal.NewFromInt(-10_000)
	if restored.SupportsComputedClaim(restored.Facts[0]) {
		t.Fatal("accepted altered per-row inputs after ledger restore")
	}
}

func TestCloseMarketDoesNotInventFactorsOrCrossDates(t *testing.T) {
	b := closeTestBuilder()
	e := b.B.Subject.ID
	b.B.Days = []string{"2026-10-26", "2026-10-27"}
	for _, day := range b.B.Days {
		addCloseRecord(b, "mkt.daily_bar", map[string]any{"trade_date": day, "close": "18", "amount": "90"})
		addCloseRecord(b, "flow.margin", map[string]any{"trade_date": day, "financing_balance": "20"})
	}
	addCloseRecord(b, "mkt.valuation_daily", map[string]any{"trade_date": "2026-10-28", "float_mv": "999"})
	b.market(e, 2, "mkt", "S6")
	b.margin(e, 2, "flow.margin", "S7")
	for _, f := range b.S.Facts {
		if f.Label == "融资余额占流通市值比例" || strings.Contains(f.Subject+f.Label, "演示") {
			t.Fatalf("unexpected fact %s: %s", f.Subject, f.Label)
		}
		if slices.Contains([]string{"区间涨跌幅（复权）", "日均换手率", "融资买入额合计"}, f.Label) && f.Value != nil {
			t.Errorf("%s unexpectedly has value %s", f.Label, f.Value)
		}
	}
	if v := b.S.Charts[0].Lines[0].Points[0][1]; v != nil {
		t.Fatalf("missing factor produced adjusted price %s", *v)
	}
	if benchmark, name := b.benchmark(); benchmark != "000300.SH" || name != "沪深 300 指数" {
		t.Fatalf("benchmark = %s %s", benchmark, name)
	}
}

func TestCloseSegmentsOnlyCompareWithinDimension(t *testing.T) {
	b := closeTestBuilder()
	addSegmentFiling(b, "2025-12-31", "filing-A", b.B.AsOf)
	row := func(p, dim, name, rev string) {
		addCloseRecord(b, "fin.segment", map[string]any{"report_period": p, "filing_id": "filing-A", "dimension": dim, "segment_name": name, "revenue": rev})
	}
	row("2026-06-30", "product", "甲", "30")
	if b.segments(b.B.Subject.ID, "seg", "S3") {
		t.Fatal("interim record generated an annual table")
	}
	row("2025-12-31", "product", "甲", "30")
	row("2025-12-31", "product", "乙", "70")
	row("2025-12-31", "region", "甲", "100")
	if !b.segments(b.B.Subject.ID, "seg", "S3") {
		t.Fatal("missing annual table")
	}
	if len(b.S.Calcs) != 3 || b.S.Calcs[0].Result.String() != "0.3" || b.S.Calcs[2].Result.String() != "1" ||
		b.S.Calcs[0].ID == b.S.Calcs[2].ID {
		t.Fatalf("unexpected segment calculations %+v", b.S.Calcs)
	}
}

func addSegmentFiling(b *builder, period, id string, at time.Time) {
	end, _ := time.Parse(time.DateOnly, period)
	addCloseRecord(b, "doc.announcement", map[string]any{"category": "periodic_report", "announcement_id": id})
	docs := b.B.Rows("doc.announcement", b.B.Subject.ID)
	docs[len(docs)-1].Period, docs[len(docs)-1].PublishedAt = &end, &at
}

func TestCloseSegmentsRequireCurrentAnnualEdition(t *testing.T) {
	for _, tc := range []struct {
		name           string
		edition        string
		newPeriod      string
		newDay         int
		includeCurrent bool
	}{
		{"legacy unbound", "", "", 0, false},
		{"amended annual report", "filing-A", "2025-12-31", 1, false},
		{"ambiguous filing pair", "filing-A", "2025-12-31", 0, false},
		{"new annual report has no segments", "filing-A", "2026-12-31", 1, false},
		{"verified replacement", "filing-A", "2025-12-31", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := closeTestBuilder()
			id := b.B.Subject.ID
			date := b.B.AsOf.AddDate(0, -1, 0)
			addSegmentFiling(b, "2025-12-31", "filing-A", date)
			if tc.newPeriod != "" {
				addSegmentFiling(b, tc.newPeriod, "filing-B", date.AddDate(0, 0, tc.newDay))
			}
			add := func(filing, revenue string) dataservice.StoredRecord {
				return addCloseRecord(b, "fin.segment", map[string]any{"report_period": "2025-12-31", "filing_id": filing, "dimension": "product", "segment_name": "产品", "revenue": revenue})
			}
			add(tc.edition, "50")
			var current dataservice.StoredRecord
			if tc.includeCurrent {
				current = add("filing-B", "40")
			}
			ok := b.segments(id, "seg", "S3")
			if ok != tc.includeCurrent {
				t.Fatalf("accepted segment facts: %v", ok)
			}
			if !ok && (len(b.S.Facts)+len(b.S.Calcs)+len(b.S.Tables["S3"])+len(b.S.SourceProviders) != 0) {
				t.Fatal("obsolete segment produced report inputs")
			}
			if ok && (len(b.S.Calcs) != 1 || !slices.Equal(b.S.Tables["S3"][0].Evidence, []uuid.UUID{current.ID}) || b.S.Calcs[0].Result.String() != "1") {
				t.Fatal("replacement must exclude previous row from the denominator and table")
			}
		})
	}
}

func TestCloseNewsUsesPublicationAndFinancialsKeepMissing(t *testing.T) {
	b := closeTestBuilder()
	r := addCloseRecord(b, "doc.announcement", map[string]any{"title": "上一年度公告"})
	before := b.B.AsOf.AddDate(0, -5, 0)
	b.B.Rows("doc.announcement", *r.EntityID)[0].PublishedAt = &before
	if b.news(*r.EntityID, "news") != 0 {
		t.Fatal("backfill changed the apparent publication date")
	}
	for _, y := range []string{"2024", "2025"} {
		end, _ := time.Parse("2006-01-02", y+"-12-31")
		addCloseRecord(b, "doc.announcement", map[string]any{"category": "periodic_report", "announcement_id": y})
		docs := b.B.Rows("doc.announcement", *r.EntityID)
		docs[len(docs)-1].Period, docs[len(docs)-1].PublishedAt = &end, &b.B.AsOf
		for _, item := range []string{"IS.revenue", "BS.total_assets", "BS.long_term_borrowing"} {
			addCloseRecord(b, "fin.statement", map[string]any{"report_period": y + "-12-31", "report_type": "FY", "scope": "consolidated", "item_code": item, "value": "70", "filing_id": y})
		}
	}
	if !b.financials(b.B.Subject.ID, 4, "S3") {
		t.Fatal("financial sample did not run")
	}
	for _, f := range b.S.Facts {
		if slices.Contains([]string{"毛利率", "资产负债率", "归母净利率", "有息负债合计"}, f.Label) && f.Value != nil {
			t.Errorf("%s fabricated a value from a missing input", f.Label)
		}
	}
}

func TestCloseResearchRegistryScopesPlans(t *testing.T) {
	registry := TuShareSkills()
	target := closeTestBuilder().B.Subject
	peer := target
	peer.Attrs = nil
	for id, sk := range registry {
		if sk.Version == Skills()[id].Version || !slices.Equal(sk.Subjects, []string{"stock"}) {
			t.Fatalf("registry metadata for %s", id)
		}
		if _, err := sk.Validate(target, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := sk.Validate(peer, nil); err == nil {
			t.Fatalf("peer-only coverage accepted by %s", id)
		}
	}
	if _, err := registry["deep_research"].Validate(target, map[string]any{"mode": "company_industry"}); err == nil {
		t.Fatal("partial sector coverage accepted as a full industry study")
	}
}

func TestCloseDisclosureSupersedesOlderPDF(t *testing.T) {
	b := closeTestBuilder()
	entity := b.B.Subject.ID
	end, _ := time.Parse("2006-01-02", "2025-12-31")
	old, newer := b.B.AsOf.AddDate(0, -5, 0), b.B.AsOf.AddDate(0, -1, 0)
	for i, version := range []string{"file-A", "file-B"} {
		addCloseRecord(b, "doc.announcement", map[string]any{"category": "periodic_report", "announcement_id": version})
		rows := b.B.Rows("doc.announcement", entity)
		rows[i].Period = &end
		rows[i].PublishedAt = []*time.Time{&old, &newer}[i]
	}
	addCloseRecord(b, "doc.periodic_text", map[string]any{"report_period": "2025-12-31", "section": "business_overview", "announcement_id": "file-A", "text": "旧年报内容"})
	if b.periodicText(entity, []string{"business_overview"}, 3, 250, 1) != 0 {
		t.Fatal("old annual report survived replacement with unavailable text")
	}
	addCloseRecord(b, "doc.periodic_text", map[string]any{"report_period": "2025-12-31", "section": "business_overview", "announcement_id": "file-B", "text": "更新后的主营业务内容"})
	if b.periodicText(entity, []string{"business_overview"}, 3, 250, 1) != 1 || len(b.S.Facts) != 1 || b.S.Facts[0].Text != "更新后的主营业务内容" {
		t.Fatal("failed to select the current annual report")
	}
}

func TestCloseRightsCoverSimAndRevocation(t *testing.T) {
	e := &Engine{Skills: TuShareSkills(), Synthetic: true}
	if !e.realResearchDisabled() {
		t.Fatal("real-data sim bypassed research switch")
	}
	e.RealResearch = true
	called := false
	send := func() error { called = true; return nil }
	var denied *abstainError
	if err := e.withCloseRights(context.Background(), send); !errors.As(err, &denied) || called {
		t.Fatal("missing rights callback allowed send")
	}
	e.WithCloseResearchUse = func(context.Context, func() error) error {
		return &dataservice.ProviderError{Class: dataservice.ErrAuthFailed, Err: errors.New("revoked")}
	}
	if err := e.withCloseRights(context.Background(), send); !errors.As(err, &denied) || denied.status != "BLOCKED" || called {
		t.Fatal("revocation allowed send")
	}
}

func TestCloseCalendarThroughPreviousSession(t *testing.T) {
	rows := []dataservice.StoredRecord{}
	for _, day := range []string{"2026-11-18", "2026-11-19", "2026-11-20"} {
		rows = append(rows, dataservice.StoredRecord{DatasetID: "ref.trading_calendar", Payload: map[string]any{"is_open": true, "trade_date": day}})
	}
	for _, tc := range []struct{ date, want string }{
		{"2026-11-20T11:00:00", "2026-11-18"}, // Friday morning
		{"2026-11-21T16:00:00", "2026-11-19"}, // Saturday
		{"2026-11-23T16:00:00", "2026-11-20"}, // Monday close is still publishing
		{"2026-12-01T16:00:00", ""},           // stale calendar
	} {
		at, err := time.ParseInLocation("2006-01-02T15:04:05", tc.date, cst)
		if err != nil {
			t.Fatal(err)
		}
		b := newBundle(at, dataservice.EntityRow{}, nil, rows)
		if got := previousCloseSession(b, rows); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.date, got, tc.want)
		}
	}
}

func TestClosePlanRequiresPublicationCutoff(t *testing.T) {
	e := &Engine{Skills: TuShareSkills(), RealResearch: true}
	subject := closeTestBuilder().B.Subject
	if _, err := e.BuildPlan(t.Context(), uuid.New(), "stock_analysis", subject, nil); err == nil {
		t.Fatal("plan bypassed database publication clock")
	}
	want := errors.New("publication clock unavailable")
	e.CloseResearchCutoff = func(context.Context) (time.Time, error) { return time.Time{}, want }
	if _, err := e.BuildPlan(t.Context(), uuid.New(), "stock_analysis", subject, nil); !errors.Is(err, want) {
		t.Fatalf("unexpected cutoff failure %v", err)
	}
}
