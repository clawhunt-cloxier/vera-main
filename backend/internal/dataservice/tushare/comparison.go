package tushare

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

var errMarketWindowCacheMiss = errors.New("comparison coverage missing")

// PrepareComparison serves authorized canonical records on a complete cache hit.
// On a miss, recheck coverage inside the provider's collection lock so concurrent
// preparations can reuse each other's published records.
func (p *Provider) PrepareComparison(ctx context.Context, store *ds.Store, request ds.ComparisonPreparation) (ds.ComparisonSample, error) {
	var sample ds.ComparisonSample
	if err := store.WithResearchUse(ctx, func() error { return nil }); err != nil {
		return sample, err
	}
	at, err := store.PublicationCutoff(ctx)
	if err != nil {
		return sample, err
	}
	_, sample, err = p.collectComparison(ctx, request, store.MarketWindowCache(at), at, true)
	if !errors.Is(err, errMarketWindowCacheMiss) {
		return sample, err
	}
	_, err = store.SyncResearchObserved(ctx, p.ID(), func(ctx context.Context) (ds.Provider, error) {
		at := p.Now()
		batch, selected, err := p.collectComparison(ctx, request, store.MarketWindowCache(at), at, false)
		sample = selected
		return batch, err
	})
	return sample, err
}

type marketWindowCollector struct {
	marketCollector
	cache     ds.MarketWindowCache
	cacheOnly bool
}

// collectComparison uses the canonical closing mappings. The sample is picked
// against the dated industry roster before inspecting any closing prices.
func (p *Provider) collectComparison(ctx context.Context, request ds.ComparisonPreparation, cache ds.MarketWindowCache, end time.Time, cacheOnly bool) (ds.Provider, ds.ComparisonSample, error) {
	var sample ds.ComparisonSample
	anchor, others, benchmarks := request.AnchorCode, request.OtherCodes, request.ComparisonBenchmarks
	codes := append([]string{anchor}, others...)
	if len(codes) > ds.ComparisonSubjectLimit || !benchmarks.Valid() ||
		(request.SingleStock && (len(others) != 0 || !benchmarks.HasIndexes())) ||
		(request.SampleIndustry != "" && (!benchmarks.IndustryIndex || !ds.IsComparisonIndustryCode(request.SampleIndustry))) {
		return nil, sample, classified(ds.ErrNotCovered, "comparison scope")
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if !instrument.MatchString(code) || seen[code] {
			return nil, sample, classified(ds.ErrNotCovered, "comparison scope")
		}
		seen[code] = true
	}
	window := request.Window
	if window == 0 {
		window = 60 // existing queued job format
	}
	if !slices.Contains(ds.ComparisonWindows(), window) {
		return nil, sample, classified(ds.ErrNotCovered, "comparison window")
	}
	through := ds.ComparisonCloseDay(end)
	if request.CloseCutoff != "" {
		day, err := time.Parse(time.DateOnly, request.CloseCutoff)
		if err != nil || day.After(through) {
			return nil, sample, classified(ds.ErrNotCovered, "comparison cutoff")
		}
		through = day
	}
	c := marketWindowCollector{
		marketCollector: marketCollector{Source: p.source, ctx: ctx, code: anchor, until: through.Format(marketDay)},
		cache:           cache, cacheOnly: cacheOnly,
	}
	days, calendarRanges, err := c.tradingDays(through, window)
	if err != nil {
		return nil, sample, err
	}
	c.from, c.day = days[0].Format(marketDay), days[len(days)-1].Format(marketDay)
	directory := make(map[string]ds.EntityRow, ds.ComparisonSubjectLimit)
	directory[anchor], err = cache.Stock(ctx, anchor)
	if err != nil {
		return nil, sample, err
	}
	var members map[string]Row
	reusedSample := false
	if len(others) == 0 && !request.SingleStock {
		sample, err = cache.Sample(ctx, directory[anchor], days[len(days)-1])
		if err != nil {
			return nil, sample, err
		}
		reusedSample = sample.ProofSHA256 != ""
		if !reusedSample {
			if cacheOnly {
				return nil, sample, errMarketWindowCacheMiss
			}
			if _, err := c.stockValuations(c.window(anchor, days[max(0, len(days)-2)].Format(marketDay))); err != nil {
				return nil, sample, err
			}
			sample, members, err = c.comparisonPeers()
			if err != nil {
				return nil, sample, err
			}
		}
		others = sample.Codes
	}
	stockRanges := 0
	for _, code := range append([]string{anchor}, others...) {
		known, ok := directory[code]
		if !ok {
			known, err = cache.Stock(ctx, code)
			if err != nil {
				return nil, sample, err
			}
		}
		gaps, err := cache.StockGaps(ctx, known, days)
		if err != nil {
			return nil, sample, err
		}
		if cacheOnly && (len(gaps) > 0 || !knownComparisonStock(known)) {
			return nil, sample, errMarketWindowCacheMiss
		}
		if cacheOnly {
			continue
		}
		entity, err := p.comparisonStock(ctx, code, known, members[code])
		if err != nil {
			return nil, sample, err
		}
		c.Entities = append(c.Entities, entity)
		if members != nil {
			record, err := marketMembership(members[code])
			if err != nil {
				return nil, sample, err
			}
			c.Records = append(c.Records, record)
		}
		stockRanges += len(gaps)
		for _, gap := range gaps {
			partial := marketCollector{Source: p.source, ctx: ctx, code: code,
				from: gap.From.Format(marketDay), day: gap.To.Format(marketDay), until: c.until}
			if _, err := partial.closingPrices(false); err != nil {
				return nil, sample, err
			}
			slog.Debug("comparison stock range processed", "stock", code, "from", partial.from, "to", partial.day)
			c.Records = append(c.Records, partial.Records...)
		}
	}
	indexRanges, err := c.comparisonIndexes(directory[anchor], benchmarks, cmp.Or(request.SampleIndustry, sample.IndustryCode), days)
	if err != nil {
		return nil, sample, err
	}
	slog.Info("comparison data coverage", "anchor", anchor, "window", window, "calendar_ranges_fetched", calendarRanges,
		"stock_ranges_fetched", stockRanges, "index_ranges_requested", indexRanges, "reused_peer_sample", reusedSample, "cache_only", cacheOnly)
	if cacheOnly {
		return nil, sample, nil
	}
	return observation{p, c.Batch}, sample, nil
}

// Each optional index is collected independently. A vendor error cannot
// discard the stock batch or publish part of a malformed index response.
func (c *marketWindowCollector) comparisonIndexes(anchor ds.EntityRow, b ds.ComparisonBenchmarks,
	industryCode string, days []time.Time) (int, error) {
	if !b.HasIndexes() {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(c.ctx, 35*time.Second)
	defer cancel()
	partial := marketCollector{Source: c.Source, ctx: ctx, code: c.code, until: c.until, day: c.day}
	total := 0
	fetch := func(api, code string, amountShift int32) error {
		gaps, err := c.cache.IndexGaps(c.ctx, code, days)
		if err != nil {
			return err
		}
		if c.cacheOnly && len(gaps) > 0 {
			return errMarketWindowCacheMiss
		}
		for _, gap := range gaps {
			if ctx.Err() != nil {
				break
			}
			total++
			partial.from, partial.day = gap.From.Format(marketDay), gap.To.Format(marketDay)
			partial.Records = nil
			if err := partial.index(api, code, amountShift); err != nil {
				reason := "collection_error"
				var providerErr *ds.ProviderError
				if errors.As(err, &providerErr) {
					reason = string(providerErr.Class)
				} else if ctx.Err() != nil {
					reason = ctx.Err().Error()
				}
				slog.Warn("optional comparison index unavailable", "api", api, "index", code,
					"from", partial.from, "to", partial.day, "reason", reason)
				break
			}
			c.Records = append(c.Records, partial.Records...)
		}
		return nil
	}
	for _, code := range b.BroadIndexes {
		if err := fetch("index_daily", code, 3); err != nil {
			return total, err
		}
	}
	if !b.IndustryIndex {
		return total, c.ctx.Err()
	}
	if industryCode == "" {
		var err error
		industryCode, err = c.cache.Industry(c.ctx, anchor, days[len(days)-1])
		if err != nil {
			return total, err
		}
	}
	if industryCode == "" {
		if c.cacheOnly {
			return total, errMarketWindowCacheMiss
		}
		// Use the actual latest session after fetching any earlier index gaps.
		partial.day = c.day
		member, err := partial.comparisonAnchorIndustry(c.day)
		if err != nil {
			slog.Warn("comparison anchor industry unavailable", "stock", c.code)
		} else if ds.IsComparisonIndustryCode(member.Values["l2_code"]) {
			record, err := marketMembership(member)
			if err == nil {
				c.Records = append(c.Records, record)
				industryCode = member.Values["l2_code"]
			}
		}
	}
	if ds.IsComparisonIndustryCode(industryCode) {
		if err := fetch("sw_daily", industryCode, 4); err != nil {
			return total, err
		}
	}
	return total, c.ctx.Err()
}

func (c *marketWindowCollector) tradingDays(through time.Time, window int) ([]time.Time, int, error) {
	from := through.AddDate(0, 0, -(window*2 + 25))
	known, err := c.cache.Calendar(c.ctx, from, through)
	if err != nil {
		return nil, 0, err
	}
	var dates []time.Time
	covered := make(map[string]bool, len(known))
	remaining := window
	for day := through; !day.Before(from); day = day.AddDate(0, 0, -1) {
		date := day.Format(time.DateOnly)
		open, exists := known[date]
		covered[date] = exists
		dates = append(dates, day)
		if open {
			remaining--
			if remaining == 0 {
				break
			}
		}
	}
	slices.Reverse(dates)
	gaps := ds.MarketMissingRanges(dates, covered)
	if c.cacheOnly && len(gaps) > 0 {
		return nil, 0, errMarketWindowCacheMiss
	}
	for _, gap := range gaps {
		partial := marketCollector{Source: c.Source, ctx: c.ctx, from: gap.From.Format(marketDay), until: gap.To.Format(marketDay)}
		if err := partial.collectCalendar(); err != nil {
			return nil, 0, err
		}
		c.Records = append(c.Records, partial.Records...)
		for _, record := range partial.Records {
			day, _ := record.Payload["trade_date"].(string)
			known[day], _ = record.Payload["is_open"].(bool)
		}
	}
	days := slices.DeleteFunc(dates, func(day time.Time) bool { return !known[day.Format(time.DateOnly)] })
	if len(days) == 0 {
		return nil, len(gaps), classified(ds.ErrEmptyValid, "trade_cal")
	}
	return days[max(0, len(days)-window):], len(gaps), nil
}

func knownComparisonStock(known ds.EntityRow) bool {
	return known.Kind == "stock" && !known.Synthetic && (known.Status == "listed" || known.Status == "st")
}

func (p *Provider) comparisonStock(ctx context.Context, code string, known ds.EntityRow, member Row) (ds.Entity, error) {
	if knownComparisonStock(known) {
		return ds.Entity{Kind: known.Kind, Code: known.Code, Name: known.Name, Exchange: derefString(known.Exchange),
			Status: known.Status, Attrs: known.Attrs, Classification: derefString(known.Classification), IsFinancial: known.IsFinancial}, nil
	}
	identity, err := p.listedStock(ctx, code)
	if err != nil {
		return ds.Entity{}, fmt.Errorf("verify comparison stock %s: %w", code, err)
	}
	entity := comparisonEntity(identity, member)
	if slices.Contains(p.codes, code) {
		entity.Attrs["tushare_research_subject"] = true
	}
	return entity, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (c *marketCollector) comparisonAnchorIndustry(day string) (Row, error) {
	rows, err := c.members("ts_code", c.code)
	if err != nil {
		return Row{}, err
	}
	current, err := marketCurrent(rows, day)
	return current[c.code], err
}

func (c *marketCollector) comparisonPeers() (ds.ComparisonSample, map[string]Row, error) {
	var sample ds.ComparisonSample
	day := c.latestVal.Values["trade_date"]
	if day == "" {
		return sample, nil, classified(ds.ErrEmptyValid, "daily_basic")
	}
	anchorRows, err := c.members("ts_code", c.code)
	if err != nil {
		return sample, nil, err
	}
	current, err := marketCurrent(anchorRows, day)
	if err != nil {
		return sample, nil, err
	}
	anchor, ok := current[c.code]
	if !ok {
		return sample, nil, classified(ds.ErrNotCovered, "index_member_all")
	}
	industry := anchor.Values["l2_code"]
	industryRows, err := c.members("l2_code", industry)
	if err != nil {
		return sample, nil, err
	}
	members, err := marketCurrent(industryRows, day)
	if err != nil {
		return sample, nil, err
	}
	if member, ok := members[c.code]; !ok || member.Values["in_date"] != anchor.Values["in_date"] {
		return sample, nil, marketBad("index_member_all", "inconsistent comparison membership")
	}
	maps.DeleteFunc(members, func(code string, _ Row) bool { return !instrument.MatchString(code) })
	valuationRows, err := c.rows("daily_basic", map[string]string{"trade_date": day}, marketValFields, 6000, false)
	if err != nil {
		return sample, nil, err
	}
	values, err := marketUnique(valuationRows, "ts_code")
	if err != nil {
		return sample, nil, err
	}
	if row, ok := values[c.code]; ok && !maps.Equal(row.Values, c.latestVal.Values) {
		return sample, nil, marketBad("daily_basic", "conflicting observations")
	}
	values[c.code] = c.latestVal
	peers := marketPeers(c.code, members, values)
	if len(peers) == 0 {
		return sample, nil, classified(ds.ErrEmptyValid, "industry comparison")
	}
	peers = peers[:min(len(peers), ds.ComparisonPeerLimit)]
	for _, code := range peers {
		rec, ok, err := marketValuation(values[code])
		if err != nil {
			return sample, nil, err
		}
		if !ok {
			return sample, nil, marketBad("daily_basic", "comparison market value")
		}
		c.Records = append(c.Records, rec)
	}
	date, err := marketDate(day)
	if err != nil {
		return sample, nil, err
	}
	sample = ds.ComparisonSample{Codes: peers, Date: date.Format(time.DateOnly), IndustryCode: industry}
	if err := c.archiveComparisonSample(&sample, date, anchor.Values["l2_name"], anchorRows, industryRows, valuationRows); err != nil {
		return ds.ComparisonSample{}, nil, err
	}
	return sample, members, nil
}

// Pin the original inputs, including other candidates. A later canonical
// industry or valuation revision must not change the selection on a follow-up.
func (c *marketCollector) archiveComparisonSample(sample *ds.ComparisonSample, date time.Time, industryName string, anchorRows, industryRows, valuationRows []Row) error {
	payload := map[string]any{
		"trade_date": sample.Date, "level2_code": sample.IndustryCode, "level2_name": industryName,
		"peer_codes": strings.Join(sample.Codes, ","),
	}
	pages := map[string]map[string]marketInput{}
	available := c.latestVal.ReceivedAt
	for api, rows := range map[string][]Row{"anchor_membership": anchorRows, "industry_membership": industryRows, "market_valuations": valuationRows} {
		pages[api] = map[string]marketInput{}
		for _, row := range rows {
			pages[api][row.RawRef] = marketInput{row.RawRef, row.RawSHA256, "response_ref#$.data.items"}
			if row.ReceivedAt.After(available) {
				available = row.ReceivedAt
			}
		}
	}
	doc, err := json.Marshal(struct {
		Version         int                               `json:"version"`
		Anchor          string                            `json:"anchor"`
		Sample          map[string]any                    `json:"sample"`
		Inputs          map[string]map[string]marketInput `json:"input_pages"`
		AnchorValuation marketInput                       `json:"anchor_valuation"`
	}{1, c.code, payload, pages, marketInput{c.latestVal.RawRef, c.latestVal.RawSHA256, c.latestVal.RawLocator}})
	if err != nil {
		return err
	}
	ref, hash, err := c.Archive(c.ctx, "comparison-sample", doc)
	if err != nil {
		return err
	}
	sample.ProofSHA256 = hash
	r := Row{ReceivedAt: available, RawRef: ref, RawSHA256: hash, RawLocator: "$.sample"}
	c.Records = append(c.Records, marketRecord(r, ds.ComparisonSampleDataset, c.code, c.code+"|"+sample.Date+"|"+hash,
		"vendor_derived", "L2", "申万 2021 版二级行业有效成员，按与基准的当日总市值差距排序，差距相同按证券代码排序", &date, payload))
	return nil
}

func comparisonEntity(identity, member Row) ds.Entity {
	v, m := identity.Values, member.Values
	_, exchange, _ := strings.Cut(v["ts_code"], ".")
	status := "listed"
	if strings.HasPrefix(v["name"], "ST") || strings.HasPrefix(v["name"], "*ST") {
		status = "st"
	}
	e := ds.Entity{Kind: "stock", Code: v["ts_code"], Name: v["name"], Exchange: exchange, Status: status,
		Attrs: map[string]any{}, Aliases: []string{v["symbol"], v["name"]}}
	for _, alias := range []string{v["fullname"], v["cnspell"]} {
		if alias != "" && !slices.Contains(e.Aliases, alias) {
			e.Aliases = append(e.Aliases, alias)
		}
	}
	if m != nil {
		e.Classification = "SW2021 " + m["l1_name"] + " > " + m["l2_name"]
		e.Attrs["industry_code"] = m["l2_code"]
		e.Attrs["index_code"] = m["l2_code"]
		e.Attrs["benchmark_code"] = marketBenchmark
		e.IsFinancial = m["l1_code"] == "801780.SI" || m["l1_code"] == "801790.SI"
	}
	return e
}

func marketMembership(row Row) (ds.Record, error) {
	v := row.Values
	start, err := marketDate(v["in_date"])
	if err != nil {
		return ds.Record{}, err
	}
	payload := map[string]any{"taxonomy": "SW2021", "level1_code": v["l1_code"], "level1_name": v["l1_name"],
		"level2_code": v["l2_code"], "level2_name": v["l2_name"], "valid_from": start.Format(time.DateOnly)}
	if out := v["out_date"]; out != "" {
		to, err := marketDate(out)
		if err != nil {
			return ds.Record{}, err
		}
		payload["valid_to"] = to.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	k := v["ts_code"] + "|" + v["in_date"] + "|" + v["l2_code"]
	return marketRecord(row, "ref.industry", v["ts_code"], k, "vendor_derived", "L2", "申万 2021 版分类", &start, payload), nil
}
