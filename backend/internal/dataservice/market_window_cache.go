package dataservice

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// MarketWindowCache reads the canonical vendor publications at a fixed cutoff.
// The collector uses it inside the provider's publication lock.
type MarketWindowCache struct {
	store *Store
	asOf  time.Time
}

func (s *Store) MarketWindowCache(asOf time.Time) MarketWindowCache {
	return MarketWindowCache{store: s, asOf: asOf}
}

type MarketDateRange struct {
	From, To time.Time
}

// MarketMissingRanges returns runs of missing dates in the supplied calendar.
// Dates must be in order. The resulting queries can span nontrading days.
func MarketMissingRanges(days []time.Time, covered map[string]bool) []MarketDateRange {
	var gaps []MarketDateRange
	open := false
	for _, day := range days {
		if covered[day.Format(time.DateOnly)] {
			open = false
			continue
		}
		if open {
			gaps[len(gaps)-1].To = day
		} else {
			gaps = append(gaps, MarketDateRange{From: day, To: day})
			open = true
		}
	}
	return gaps
}

func (c MarketWindowCache) Calendar(ctx context.Context, from, to time.Time) (map[string]bool, error) {
	rows, err := c.rows(ctx, "ref.trading_calendar", nil, nil, &from, &to)
	days := make(map[string]bool, len(rows))
	for _, row := range rows {
		if open, ok := row.Payload["is_open"].(bool); ok {
			days[comparisonString(row, "trade_date")] = open
		}
	}
	return days, err
}

func (c MarketWindowCache) Stock(ctx context.Context, code string) (EntityRow, error) {
	if c.store == nil {
		return EntityRow{}, nil
	}
	entity, err := c.store.EntityByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return EntityRow{}, nil
	}
	return entity, err
}

func (c MarketWindowCache) StockGaps(ctx context.Context, entity EntityRow, days []time.Time) ([]MarketDateRange, error) {
	return c.stockGaps(ctx, entity, days, "mkt.daily_bar", func(row StoredRecord) bool {
		suspended, _ := row.Payload["suspended"].(bool)
		return suspended || (comparisonPositive(row, "close") && comparisonPositive(row, "adj_factor"))
	})
}

func (c MarketWindowCache) IndicatorGaps(ctx context.Context, entity EntityRow, days []time.Time, dataset string) ([]MarketDateRange, error) {
	return c.stockGaps(ctx, entity, days, dataset, func(row StoredRecord) bool {
		if dataset == "flow.margin" {
			return nonnegativeField(row, "financing_balance")
		}
		suspended, _ := row.Payload["suspended"].(bool)
		return suspended || (nonnegativeField(row, "amount") && nonnegativeField(row, "volume"))
	})
}

func (c MarketWindowCache) stockGaps(ctx context.Context, entity EntityRow, days []time.Time, dataset string, validRow func(StoredRecord) bool) ([]MarketDateRange, error) {
	if len(days) == 0 {
		return nil, nil
	}
	if entity.ID == uuid.Nil {
		return MarketMissingRanges(days, nil), nil
	}
	rows, err := c.rows(ctx, dataset, []uuid.UUID{entity.ID}, nil, &days[0], &days[len(days)-1])
	valid := make(map[string]bool, len(rows))
	for _, row := range rows {
		if validRow(row) {
			valid[comparisonString(row, "trade_date")] = true
		}
	}
	return MarketMissingRanges(days, valid), err
}

func (c MarketWindowCache) IndexGaps(ctx context.Context, code string, days []time.Time) ([]MarketDateRange, error) {
	rows, err := c.rows(ctx, "mkt.index_bar", nil, []string{code}, &days[0], &days[len(days)-1])
	valid := make(map[string]bool, len(rows))
	for _, row := range rows {
		if comparisonPositive(row, "close") {
			valid[comparisonString(row, "trade_date")] = true
		}
	}
	return MarketMissingRanges(days, valid), err
}

func (c MarketWindowCache) Industry(ctx context.Context, anchor EntityRow, day time.Time) (string, error) {
	if anchor.ID == uuid.Nil {
		return "", nil
	}
	rows, err := c.rows(ctx, "ref.industry", []uuid.UUID{anchor.ID}, nil, nil, &day)
	date, code, newest := day.Format(time.DateOnly), "", ""
	for _, row := range rows {
		from, to := comparisonString(row, "valid_from"), comparisonString(row, "valid_to")
		candidate := comparisonString(row, "level2_code")
		if IsComparisonIndustryCode(candidate) && from > newest && from <= date && (to == "" || to >= date) {
			code, newest = candidate, from
		}
	}
	return code, err
}

func (c MarketWindowCache) Sample(ctx context.Context, anchor EntityRow, day time.Time) (ComparisonSample, error) {
	var sample ComparisonSample
	if anchor.ID == uuid.Nil {
		return sample, nil
	}
	rows, err := c.rows(ctx, ComparisonSampleDataset, []uuid.UUID{anchor.ID}, nil, &day, &day)
	var latest time.Time
	for _, row := range rows {
		code := comparisonString(row, "level2_code")
		peers := strings.Split(comparisonString(row, "peer_codes"), ",")
		if row.RawSHA256 == nil || !IsComparisonIndustryCode(code) || peers[0] == "" || len(peers) > ComparisonPeerLimit ||
			row.AvailableAt.Before(latest) {
			continue
		}
		sample = ComparisonSample{Codes: peers, Date: day.Format(time.DateOnly), IndustryCode: code, ProofSHA256: *row.RawSHA256}
		latest = row.AvailableAt
	}
	return sample, err
}

func (c MarketWindowCache) rows(ctx context.Context, dataset string, entities []uuid.UUID, indexes []string, from, to *time.Time) ([]StoredRecord, error) {
	if c.store == nil {
		return nil, nil
	}
	return c.store.Query(ctx, Query{DatasetIDs: []string{dataset}, EntityIDs: entities, IndexCodes: indexes,
		From: from, To: to, AsOf: c.asOf, ModelUse: true})
}

func comparisonString(row StoredRecord, field string) string {
	value, _ := row.Payload[field].(string)
	return value
}

func comparisonPositive(row StoredRecord, field string) bool {
	value, err := decimal.NewFromString(comparisonString(row, field))
	return err == nil && value.IsPositive()
}

func nonnegativeField(row StoredRecord, field string) bool {
	value, err := decimal.NewFromString(comparisonString(row, field))
	return err == nil && !value.IsNegative()
}
