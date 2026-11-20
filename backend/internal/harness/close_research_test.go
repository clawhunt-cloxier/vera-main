package harness

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
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
	row := func(p, dim, name, rev string) {
		addCloseRecord(b, "fin.segment", map[string]any{"report_period": p, "dimension": dim, "segment_name": name, "revenue": rev})
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
