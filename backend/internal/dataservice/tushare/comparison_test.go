package tushare

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type comparisonRequestLog struct {
	api, code, from, to string
}

type comparisonTestSource struct {
	*marketTestSource
	requests []comparisonRequestLog
}

func (s *comparisonTestSource) Rows(ctx context.Context, api string, params map[string]string, fields []string, limit int) ([]Row, error) {
	s.requests = append(s.requests, comparisonRequestLog{api, params["ts_code"], params["start_date"], params["end_date"]})
	rows, err := s.marketTestSource.Rows(ctx, api, params, fields, limit)
	return slices.DeleteFunc(rows, func(row Row) bool {
		day := row.Values["trade_date"]
		return day != "" && (day < params["start_date"] || params["end_date"] != "" && day > params["end_date"])
	}), err
}

func TestComparisonPreparationUsesCanonicalCoverage(t *testing.T) {
	const (
		anchor = "699991.SH"
		peer   = "699992.SH"
		broad  = ds.ComparisonDefaultBroadIndex
		sw     = "801011.SI"
	)
	ctx := t.Context()
	pool := dbtest.New(t)
	cat, err := ds.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	store := ds.NewResearchStore(pool, cat, "tushare", []string{anchor}, true)
	src := &comparisonTestSource{marketTestSource: &marketTestSource{
		data: map[string][]map[string]string{}, objects: map[string][]byte{}, fail: map[string]error{},
	}}
	p := &Provider{source: src, allowModel: true}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	through := start.AddDate(0, 0, 11)
	var seed Batch
	raw := Row{RawRef: "synthetic/ref", RawSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RawLocator: "test", ReceivedAt: through}
	for _, code := range []string{anchor, peer} {
		seed.Entities = append(seed.Entities, ds.Entity{Kind: "stock", Code: code, Name: code, Status: "listed", Exchange: "SH"})
	}
	for day := start; !day.After(through); day = day.AddDate(0, 0, 1) {
		date := day.Format(marketDay)
		open := day.Weekday() != time.Saturday && day.Weekday() != time.Sunday
		seed.Records = append(seed.Records, marketRecord(raw, "ref.trading_calendar", "", date, "exchange", "L1", "test", &day,
			map[string]any{"trade_date": day.Format(time.DateOnly), "is_open": open}))
		if !open {
			continue
		}
		for _, code := range []string{anchor, peer} {
			src.data["daily"] = append(src.data["daily"], map[string]string{"ts_code": code, "trade_date": date,
				"open": "10", "high": "11", "low": "9", "close": "10", "pre_close": "10", "vol": "5", "amount": "5"})
			src.data["adj_factor"] = append(src.data["adj_factor"], map[string]string{"ts_code": code, "trade_date": date, "adj_factor": "1"})
		}
		src.data["index_daily"] = append(src.data["index_daily"], map[string]string{"ts_code": broad,
			"trade_date": date, "close": "3500", "amount": "10"})
	}
	for _, code := range []string{anchor, peer} {
		c := marketCollector{Source: src, ctx: ctx, code: code, from: "20240108", day: "20240112", until: "20240112"}
		if _, err := c.closingPrices(false); err != nil {
			t.Fatal(err)
		}
		seed.Records = append(seed.Records, c.Records...)
	}
	seed.Records = append(seed.Records, marketRecord(raw, ds.ComparisonSampleDataset, anchor, "test-sample", "vendor_derived", "L2", "test", &through,
		map[string]any{"trade_date": through.Format(time.DateOnly), "level2_code": sw, "level2_name": "模拟申万二级", "peer_codes": peer}))
	if _, err := store.SyncObserved(ctx, p.ID(), func(context.Context) (ds.Provider, error) { return observation{p, seed}, nil }); err != nil {
		t.Fatal(err)
	}
	src.requests = nil
	request := ds.ComparisonPreparation{AnchorCode: anchor, Window: 5, CloseCutoff: through.Format(time.DateOnly)}

	// A hot read completes even while an unrelated vendor fetch owns the collection lock.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('tushare',508418))`)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('tushare',508418))`); err != nil {
		t.Fatal(err)
	}
	hotCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	sample, err := p.PrepareComparison(hotCtx, store, request)
	cancel()
	if _, unlockErr := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('tushare',508418))`); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	if err != nil || !slices.Equal(sample.Codes, []string{peer}) || sample.ProofSHA256 != raw.RawSHA256 || len(src.requests) != 0 {
		t.Fatalf("cache hit: sample=%+v calls=%+v err=%v", sample, src.requests, err)
	}

	request.OtherCodes = []string{peer}
	request.IndustryIndex, request.SampleIndustry = true, sw
	request.BroadIndexes = []string{broad}
	src.fail["sw_daily"] = &ds.ProviderError{Class: ds.ErrRateLimited, Err: errors.New("synthetic rate limit")}
	if _, err := p.PrepareComparison(ctx, store, request); err != nil {
		t.Fatalf("optional industry index blocked the other data: %v", err)
	}
	want := []comparisonRequestLog{{"index_daily", broad, "20240108", "20240112"}, {"sw_daily", sw, "20240108", "20240112"}}
	if !slices.Equal(src.requests, want) {
		t.Fatalf("adding indexes requested %+v, want %+v", src.requests, want)
	}

	src.requests = nil
	request.IndustryIndex, request.SampleIndustry, request.Window = false, "", 10
	if _, err := p.PrepareComparison(ctx, store, request); err != nil {
		t.Fatal(err)
	}
	want = nil
	for _, code := range []string{anchor, peer} {
		for _, api := range []string{"daily", "suspend_d", "adj_factor"} {
			want = append(want, comparisonRequestLog{api, code, "20240101", "20240105"})
		}
	}
	want = append(want, comparisonRequestLog{"index_daily", broad, "20240101", "20240105"})
	if !slices.Equal(src.requests, want) {
		t.Fatalf("extending the window requested %+v, want %+v", src.requests, want)
	}
	src.requests = nil
	if _, err := p.PrepareComparison(ctx, store, request); err != nil || len(src.requests) != 0 {
		t.Fatalf("the wider window was not reused: calls=%+v err=%v", src.requests, err)
	}

	for _, row := range seed.Records {
		if row.DatasetID == "mkt.daily_bar" && row.EntityCode == anchor && row.Period.Equal(through) {
			row.QualityState = "QUARANTINED"
			seed.Records = []ds.Record{row}
			break
		}
	}
	if _, err := store.SyncObserved(ctx, p.ID(), func(context.Context) (ds.Provider, error) { return observation{p, seed}, nil }); err != nil {
		t.Fatal(err)
	}
	cache := store.ComparisonCache(time.Now())
	stock, err := cache.Stock(ctx, anchor)
	if err != nil {
		t.Fatal(err)
	}
	gaps, err := cache.StockGaps(ctx, stock, []time.Time{through})
	if err != nil || !slices.Equal(gaps, []ds.ComparisonDateRange{{From: through, To: through}}) {
		t.Fatalf("a quarantined revision allowed the older bar to count: %+v, %v", gaps, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='tushare'`); err != nil {
		t.Fatal(err)
	}
	_, err = p.PrepareComparison(ctx, store, request)
	var denied *ds.ProviderError
	if !errors.As(err, &denied) || denied.Class != ds.ErrAuthFailed || len(src.requests) != 0 {
		t.Fatalf("revoked research rights: calls=%+v err=%v", src.requests, err)
	}
}
