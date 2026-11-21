package tushare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const (
	marketDay            = "20060102"
	marketBenchmark      = "000300.SH"
	marketValuationBasis = "TuShare 每日指标：PE、PS 为 TTM；PB 按总市值除以净资产扣除其他权益工具；亏损 PE 无值"
	marketOrderBasis     = "TuShare moneyflow 按主动买卖成交金额分档，各档净额为买入额减卖出额"
)

var marketZone = time.FixedZone("Asia/Shanghai", 8*3600)

type marketCollector struct {
	Source
	Batch
	ctx               context.Context
	code              string
	from, until, day  string
	latestVal         Row
	requestedIdentity *Row // exact archived stock_basic row verified before on-demand collection
}

func collectMarket(ctx context.Context, src Source, code string, end time.Time, requestedIdentity *Row) (Batch, error) {
	local := end.In(marketZone)
	if local.Hour() < 15 {
		local = local.AddDate(0, 0, -1)
	}
	c := marketCollector{Source: src, ctx: ctx, code: code, from: local.AddDate(-5, 0, -7).Format(marketDay), until: local.Format(marketDay), requestedIdentity: requestedIdentity}
	for _, step := range []func() error{c.calendar, c.stockHistory, c.directory, c.flows, c.dividends, c.dragonTiger, c.fundPositions} {
		if err := step(); err != nil {
			return Batch{}, err
		}
	}
	return c.Batch, nil
}

func (c *marketCollector) rows(api string, params map[string]string, fields string, limit int, optional bool) ([]Row, error) {
	rs, err := c.Rows(c.ctx, api, params, strings.Fields(fields), limit)
	if err != nil {
		var pe *dataservice.ProviderError
		if optional && errors.As(err, &pe) && (pe.Class == dataservice.ErrNotCovered || pe.Class == dataservice.ErrEmptyValid) {
			c.Notes = append(c.Notes, "TuShare "+api+" 暂无可用数据或接口权限")
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", api, err)
	}
	// Validate the identity and date before any cross-interface join. A bad vendor
	// filter must not put another security or a future trading session in a report.
	for _, r := range rs {
		for _, f := range []string{"ts_code", "l2_code", "exchange", "trade_date", "is_new", "list_status"} {
			if q := params[f]; q != "" && r.Values[f] != q {
				return nil, marketBad(api, f)
			}
		}
		if d := r.Values["trade_date"]; d != "" {
			if _, e := time.Parse(marketDay, d); e != nil || d > c.until || params["start_date"] != "" && d < params["start_date"] || params["end_date"] != "" && d > params["end_date"] {
				return nil, marketBad(api, "trade_date")
			}
		}
	}
	return rs, nil
}

func marketBad(api, field string) error {
	return &dataservice.ProviderError{Class: dataservice.ErrSchemaChanged, Err: fmt.Errorf("%s: invalid or contradictory %s", api, field)}
}

func (c *marketCollector) window(code, start string) map[string]string {
	return map[string]string{"ts_code": code, "start_date": start, "end_date": c.day}
}

func marketDate(s string) (time.Time, error) {
	d, err := time.Parse(marketDay, s)
	if err != nil {
		return d, marketBad("TuShare", "date")
	}
	return d, nil
}

func marketRecord(row Row, dataset, code, key, origin, tier, basis string, period *time.Time, payload map[string]any) dataservice.Record {
	key = "tushare|" + dataset + "|" + key
	return dataservice.Record{DatasetID: dataset, EntityCode: code, RecordKey: key, LogicalKey: key, Version: 1, Period: period,
		AvailableAt: row.ReceivedAt, Origin: origin, SourceTier: tier, Basis: basis, Payload: payload, RawRef: row.RawRef, RawSHA256: row.RawSHA256, RawLocator: row.RawLocator, MappingVersion: 1}
}

// One content-addressed manifest per batch of joined records; a locator identifies
// a day (or a security) and all participating raw response rows inside the object.
type marketInput struct {
	Ref     string `json:"raw_ref"`
	Hash    string `json:"sha256"`
	Locator string `json:"locator"`
}
type marketJoins map[string]map[string]marketInput

func (joins marketJoins) add(key, api string, r Row) {
	if joins[key] == nil {
		joins[key] = map[string]marketInput{}
	}
	joins[key][api] = marketInput{r.RawRef, r.RawSHA256, r.RawLocator}
}
func (c *marketCollector) archiveJoins(joins marketJoins, records []dataservice.Record, keys []string) error {
	if len(records) == 0 {
		return nil
	}
	raw, err := json.Marshal(joins)
	if err != nil {
		return err
	}
	ref, sum, err := c.Archive(c.ctx, "joins", raw)
	if err != nil {
		return err
	}
	for i := range records {
		records[i].RawRef, records[i].RawSHA256, records[i].RawLocator = ref, sum, fmt.Sprintf("$[%q]", keys[i])
	}
	c.Records = append(c.Records, records...)
	return nil
}

func marketUnique(rs []Row, key string) (map[string]Row, error) {
	m := make(map[string]Row, len(rs))
	for _, r := range rs {
		k := r.Values[key]
		if k == "" {
			return nil, marketBad("TuShare", key)
		}
		if prev, ok := m[k]; ok && !maps.Equal(prev.Values, r.Values) {
			return nil, marketBad("TuShare", "duplicate "+key)
		}
		m[k] = r
	}
	return m, nil
}

type marketField struct {
	src, dst string
	shift    int32
	required bool
}

// The Pro JSON representation is parsed as decimal, including exponent notation.
// Optional nulls stay absent. A missing required cell omits the row (never a zero).
func marketNumbers(r Row, dest map[string]any, fields ...marketField) (bool, error) {
	for _, f := range fields {
		s := r.Values[f.src]
		if s == "" {
			if f.required {
				return false, nil
			}
			continue
		}
		v, err := decimal.NewFromString(s)
		if err != nil || v.Exponent() > 18 || v.Exponent() < -30 {
			return false, marketBad("TuShare", f.src)
		}
		dest[f.dst] = v.Shift(f.shift).String()
	}
	return true, nil
}

func (c *marketCollector) calendar() error {
	// The canonical research calendar is the common mainland stock session calendar.
	rs, err := c.rows("trade_cal", map[string]string{"exchange": "SSE", "start_date": c.from, "end_date": c.until}, "exchange cal_date is_open", 3000, false)
	if err != nil {
		return err
	}
	days, err := marketUnique(rs, "cal_date")
	if err != nil {
		return err
	}
	for _, s := range slices.Sorted(maps.Keys(days)) {
		r := days[s]
		d, e := marketDate(s)
		if e != nil || s < c.from || s > c.until {
			return marketBad("trade_cal", "cal_date")
		}
		open := r.Values["is_open"]
		if open != "0" && open != "1" {
			return marketBad("trade_cal", "is_open")
		}
		if open == "1" {
			c.day = s
		}
		c.Records = append(c.Records, marketRecord(r, "ref.trading_calendar", "", s, "exchange", "L2", "TuShare 上交所交易日历", &d, map[string]any{"trade_date": d.Format(time.DateOnly), "is_open": open == "1"}))
	}
	if c.day == "" {
		return fmt.Errorf("TuShare: no completed trading day in the requested calendar")
	}
	return nil
}
