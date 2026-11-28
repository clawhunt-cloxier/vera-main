package tushare

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// PrepareIndicator reads authorized canonical records and fills the missing ranges
// under the same publication lock as other TuShare research.
func (p *Provider) PrepareIndicator(ctx context.Context, store *ds.Store, request ds.IndicatorPreparation) error {
	if !instrument.MatchString(request.Code) || !store.AllowsIndicatorSubject(request.Code) ||
		!slices.Contains(ds.IndicatorWindows(request.DatasetID), request.Window) {
		return classified(ds.ErrNotCovered, "indicator scope")
	}
	if err := store.WithResearchUse(ctx, func() error { return nil }); err != nil {
		return err
	}
	at, err := store.PublicationCutoff(ctx)
	if err != nil {
		return err
	}
	through, err := time.Parse(time.DateOnly, request.CloseCutoff)
	if err != nil || through.After(ds.ComparisonCloseDay(at)) {
		return classified(ds.ErrNotCovered, "indicator cutoff")
	}
	if _, err = p.collectIndicator(ctx, request, store.MarketWindowCache(at), through, true); !errors.Is(err, errMarketWindowCacheMiss) {
		return err
	}
	_, err = store.SyncResearchObserved(ctx, p.ID(), func(ctx context.Context) (ds.Provider, error) {
		return p.collectIndicator(ctx, request, store.MarketWindowCache(p.Now()), through, false)
	})
	return err
}

func (p *Provider) collectIndicator(ctx context.Context, request ds.IndicatorPreparation, cache ds.MarketWindowCache, through time.Time, cacheOnly bool) (ds.Provider, error) {
	c := marketWindowCollector{
		marketCollector: marketCollector{Source: p.source, ctx: ctx, code: request.Code, until: through.Format(marketDay)},
		cache:           cache, cacheOnly: cacheOnly,
	}
	days, calendars, err := c.tradingDays(through, request.Window)
	if err != nil {
		return nil, err
	}
	known, err := cache.Stock(ctx, request.Code)
	if err != nil {
		return nil, err
	}
	gaps, err := cache.IndicatorGaps(ctx, known, days, request.DatasetID)
	if err != nil {
		return nil, err
	}
	if cacheOnly && (len(gaps) > 0 || !knownComparisonStock(known)) {
		return nil, errMarketWindowCacheMiss
	}
	slog.Info("indicator data coverage", "stock", request.Code, "dataset", request.DatasetID, "window", request.Window,
		"calendar_ranges_fetched", calendars, "indicator_ranges_needed", len(gaps), "cache_only", cacheOnly)
	if cacheOnly {
		return nil, nil
	}
	entity, err := p.comparisonStock(ctx, request.Code, known, Row{})
	if err != nil {
		return nil, err
	}
	c.Entities = append(c.Entities, entity)
	for _, gap := range gaps {
		part := marketCollector{Source: p.source, ctx: ctx, code: request.Code, until: c.until,
			from: gap.From.Format(marketDay), day: gap.To.Format(marketDay)}
		if request.DatasetID == "flow.margin" {
			err = part.margin(part.window(part.code, part.from))
		} else {
			_, err = part.closingPrices(false)
		}
		if err != nil {
			return nil, err
		}
		c.Records = append(c.Records, part.Records...)
	}
	return observation{p, c.Batch}, nil
}
