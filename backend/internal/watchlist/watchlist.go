// Package watchlist implements private watch items, alerts and close-based snapshots (skills/watchlist.md).
// No model calls; snapshots use the same unified datasets as research.
package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

const (
	MaxItems  = 100
	MaxAlerts = 5
)

type Alert struct {
	Kind      string  `json:"kind"`
	Threshold *string `json:"threshold"`
	Enabled   bool    `json:"enabled"`
}

type Snapshot struct {
	TradeDate      string   `json:"trade_date"`
	Close          string   `json:"close"`
	ChangePct      string   `json:"change_pct"`
	SinceAddedPct  *string  `json:"since_added_pct"`
	PE             *string  `json:"pe_ttm"`
	MarginChange5d *string  `json:"margin_balance_change_5d"`
	Triggered      []string `json:"triggered"`
}

type Item struct {
	ID       uuid.UUID `json:"id"`
	Version  int64     `json:"version"`
	EntityID uuid.UUID `json:"entity_id"`
	Tags     []string  `json:"tags"`
	Note     string    `json:"note"`
	Alerts   []Alert   `json:"alerts"`
	AddedAt  time.Time `json:"added_at"`
	Snapshot *Snapshot `json:"snapshot"`
}

type Service struct {
	Pool  *pgxpool.Pool
	Store *dataservice.Store
	Now   func() time.Time
}

var kinds = []string{"price_above", "price_below", "daily_change", "margin_change", "new_filing"}

func validate(tags []string, note string, alerts []Alert) error {
	if len(tags) > 5 {
		return errs.Unprocessable("INVALID_WATCH_ITEM", "最多 5 个标签")
	}
	for _, t := range tags {
		if n := utf8.RuneCountInString(t); n == 0 || n > 10 {
			return errs.Unprocessable("INVALID_WATCH_ITEM", "标签长度为 1–10 字")
		}
	}
	if utf8.RuneCountInString(note) > 500 {
		return errs.Unprocessable("INVALID_WATCH_ITEM", "备注不超过 500 字")
	}
	if len(alerts) > MaxAlerts {
		return errs.Unprocessable("INVALID_WATCH_ITEM", "每只股票最多 5 条提醒")
	}
	for _, a := range alerts {
		if !slices.Contains(kinds, a.Kind) {
			return errs.Unprocessable("INVALID_WATCH_ITEM", "提醒类型无效")
		}
		if a.Kind == "new_filing" {
			continue
		}
		if a.Threshold == nil {
			return errs.Unprocessable("INVALID_WATCH_ITEM", "该提醒需要阈值")
		}
		v, err := decimal.NewFromString(*a.Threshold)
		if err != nil || (a.Kind != "price_above" && a.Kind != "price_below" && !v.IsPositive()) || v.IsNegative() {
			return errs.Unprocessable("INVALID_WATCH_ITEM", "提醒阈值无效")
		}
	}
	return nil
}

const cols = `id, version, entity_id, tags, note, alerts, added_at`

func scan(row pgx.Row) (Item, error) {
	var it Item
	var alerts []byte
	err := row.Scan(&it.ID, &it.Version, &it.EntityID, &it.Tags, &it.Note, &alerts, &it.AddedAt)
	_ = json.Unmarshal(alerts, &it.Alerts)
	if it.Alerts == nil {
		it.Alerts = []Alert{}
	}
	if it.Tags == nil {
		it.Tags = []string{}
	}
	return it, err
}

func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Item, error) {
	var out []Item
	err := db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+cols+` FROM watchlist_items ORDER BY added_at DESC, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, it)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			out[i].Snapshot, err = latestSnapshot(ctx, tx, out[i].ID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func latestSnapshot(ctx context.Context, tx pgx.Tx, item uuid.UUID) (*Snapshot, error) {
	var m []byte
	var trig []string
	err := tx.QueryRow(ctx, `SELECT metrics, triggered FROM watchlist_snapshots WHERE item_id=$1 ORDER BY trade_date DESC LIMIT 1`, item).Scan(&m, &trig)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sn Snapshot
	_ = json.Unmarshal(m, &sn)
	sn.Triggered = trig
	if sn.Triggered == nil {
		sn.Triggered = []string{}
	}
	return &sn, nil
}

func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Item, error) {
	var it Item
	err := db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		var err error
		it, err = scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM watchlist_items WHERE id=$1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.NotFound()
		}
		if err != nil {
			return err
		}
		it.Snapshot, err = latestSnapshot(ctx, tx, id)
		return err
	})
	return it, err
}

// Add creates an item (stocks only) and computes its first snapshot.
func (s *Service) Add(ctx context.Context, ws uuid.UUID, entity uuid.UUID, tags []string, note string, alerts []Alert) (Item, error) {
	if tags == nil {
		tags = []string{}
	}
	if alerts == nil {
		alerts = []Alert{}
	}
	if err := validate(tags, note, alerts); err != nil {
		return Item{}, err
	}
	ents, err := s.Store.Entities(ctx, []uuid.UUID{entity})
	if err != nil {
		return Item{}, err
	}
	if e, ok := ents[entity]; !ok || e.Kind != "stock" {
		return Item{}, errs.Unprocessable("INVALID_WATCH_ITEM", "自选股只能添加 A 股个股")
	}
	bar, _ := s.latestBar(ctx, entity)
	var id uuid.UUID
	ab, _ := json.Marshal(alerts)
	err = db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM watchlist_items`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxItems {
			return errs.Unprocessable("WATCHLIST_FULL", "自选股最多 100 只")
		}
		var price *decimal.Decimal
		var day *string
		if bar != nil {
			p := adjClose(*bar)
			price = &p
			d, _ := bar.Payload["trade_date"].(string)
			day = &d
		}
		err := tx.QueryRow(ctx, `INSERT INTO watchlist_items(workspace_id,entity_id,tags,note,alerts,added_price,added_trade_date) VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (workspace_id, entity_id) DO NOTHING RETURNING id`, ws, entity, tags, note, ab, price, day).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.Conflict("ALREADY_IN_WATCHLIST", "该股票已在自选股中")
		}
		return err
	})
	if err != nil {
		return Item{}, err
	}
	if err := s.Refresh(ctx, ws, id); err != nil {
		return Item{}, err
	}
	return s.Get(ctx, ws, id)
}

// Update applies a patch with optimistic concurrency.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, expected int64, tags *[]string, note *string, alerts *[]Alert) (Item, error) {
	cur, err := s.Get(ctx, ws, id)
	if err != nil {
		return Item{}, err
	}
	if tags != nil {
		cur.Tags = *tags
	}
	if note != nil {
		cur.Note = *note
	}
	if alerts != nil {
		cur.Alerts = *alerts
	}
	if err := validate(cur.Tags, cur.Note, cur.Alerts); err != nil {
		return Item{}, err
	}
	ab, _ := json.Marshal(cur.Alerts)
	err = db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE watchlist_items SET tags=$3, note=$4, alerts=$5, version=version+1 WHERE id=$1 AND version=$2`,
			id, expected, cur.Tags, cur.Note, ab)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errs.Conflict("VERSION_MISMATCH", "自选股已被修改，请刷新后重试")
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	if err := s.Refresh(ctx, ws, id); err != nil {
		return Item{}, err
	}
	return s.Get(ctx, ws, id)
}

// Remove deletes the item; reports are kept.
func (s *Service) Remove(ctx context.Context, ws, id uuid.UUID) error {
	return db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM watchlist_items WHERE id=$1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return errs.NotFound()
		}
		return err
	})
}

func adjClose(r dataservice.StoredRecord) decimal.Decimal {
	c, _ := decimal.NewFromString(str(r, "close"))
	if a, err := decimal.NewFromString(str(r, "adj_factor")); err == nil {
		return c.Mul(a)
	}
	return c
}

func str(r dataservice.StoredRecord, f string) string { s, _ := r.Payload[f].(string); return s }

func (s *Service) series(ctx context.Context, ds string, e uuid.UUID, days int) ([]dataservice.StoredRecord, error) {
	from := s.Now().AddDate(0, 0, -days)
	return s.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{ds}, EntityIDs: []uuid.UUID{e}, From: &from, AsOf: s.Now()})
}

func (s *Service) latestBar(ctx context.Context, e uuid.UUID) (*dataservice.StoredRecord, error) {
	rs, err := s.series(ctx, "mkt.daily_bar", e, 20)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	for i := len(rs) - 1; i >= 0; i-- {
		if b, _ := rs[i].Payload["suspended"].(bool); !b {
			return &rs[i], nil
		}
	}
	return &rs[len(rs)-1], nil
}

var hundred = decimal.NewFromInt(100)

// Refresh computes the snapshot for the latest trade date of the data clock and evaluates alerts.
func (s *Service) Refresh(ctx context.Context, ws, id uuid.UUID) error {
	var entity uuid.UUID
	var added *decimal.Decimal
	var alerts []byte
	err := db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT entity_id, added_price, alerts FROM watchlist_items WHERE id=$1`, id).Scan(&entity, &added, &alerts)
	})
	if err != nil {
		return err
	}
	var al []Alert
	_ = json.Unmarshal(alerts, &al)
	bars, err := s.series(ctx, "mkt.daily_bar", entity, 20)
	if err != nil || len(bars) == 0 {
		return err
	}
	last := bars[len(bars)-1]
	suspended, _ := last.Payload["suspended"].(bool)
	closePx, _ := decimal.NewFromString(str(last, "close"))
	pre, _ := decimal.NewFromString(str(last, "pre_close"))
	sn := Snapshot{TradeDate: str(last, "trade_date"), Close: closePx.StringFixed(2), ChangePct: "0.00", Triggered: []string{}}
	chg := decimal.Zero
	if pre.IsPositive() && !suspended {
		chg = closePx.Div(pre).Sub(decimal.NewFromInt(1)).Mul(hundred)
		sn.ChangePct = chg.StringFixed(2)
	}
	if added != nil && added.IsPositive() {
		v := adjClose(last).Div(*added).Sub(decimal.NewFromInt(1)).Mul(hundred).StringFixed(2)
		sn.SinceAddedPct = &v
	}
	if vs, err := s.series(ctx, "mkt.valuation_daily", entity, 20); err == nil && len(vs) > 0 {
		if pe := str(vs[len(vs)-1], "pe_ttm"); pe != "" {
			d, _ := decimal.NewFromString(pe)
			v := d.StringFixed(2)
			sn.PE = &v
		}
	}
	var marginChg *decimal.Decimal
	if ms, err := s.series(ctx, "flow.margin", entity, 20); err == nil && len(ms) >= 6 {
		a, _ := decimal.NewFromString(str(ms[len(ms)-6], "financing_balance"))
		b, _ := decimal.NewFromString(str(ms[len(ms)-1], "financing_balance"))
		if a.IsPositive() {
			pct := b.Div(a).Sub(decimal.NewFromInt(1)).Mul(hundred)
			marginChg = &pct
			v := pct.StringFixed(2)
			sn.MarginChange5d = &v
		}
	}
	for _, a := range al {
		if !a.Enabled {
			continue
		}
		th := decimal.Zero
		if a.Threshold != nil {
			th, _ = decimal.NewFromString(*a.Threshold)
		}
		switch a.Kind {
		case "price_above":
			if !suspended && closePx.GreaterThan(th) {
				sn.Triggered = append(sn.Triggered, fmt.Sprintf("%s 收盘价 %s 元，高于设定的 %s 元（不复权）", sn.TradeDate, sn.Close, th.String()))
			}
		case "price_below":
			if !suspended && closePx.LessThan(th) {
				sn.Triggered = append(sn.Triggered, fmt.Sprintf("%s 收盘价 %s 元，低于设定的 %s 元（不复权）", sn.TradeDate, sn.Close, th.String()))
			}
		case "daily_change":
			if !suspended && chg.Abs().GreaterThanOrEqual(th) {
				sn.Triggered = append(sn.Triggered, fmt.Sprintf("%s 涨跌幅 %s%%，绝对值达到设定的 %s%%", sn.TradeDate, sn.ChangePct, th.String()))
			}
		case "margin_change":
			if marginChg != nil && marginChg.Abs().GreaterThanOrEqual(th) {
				sn.Triggered = append(sn.Triggered, fmt.Sprintf("融资余额 5 个交易日变化 %s%%，绝对值达到设定的 %s%%", *sn.MarginChange5d, th.String()))
			}
		case "new_filing":
			anns, _ := s.series(ctx, "doc.announcement", entity, 7)
			for _, r := range anns {
				if c := str(r, "category"); c == "periodic_report" || c == "forecast" || c == "express" {
					sn.Triggered = append(sn.Triggered, "近 7 日有新公告："+str(r, "title"))
				}
			}
		}
	}
	m, _ := json.Marshal(sn)
	return db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO watchlist_snapshots(workspace_id,item_id,trade_date,metrics,triggered) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (item_id, trade_date) DO UPDATE SET metrics=excluded.metrics, triggered=excluded.triggered`, ws, id, sn.TradeDate, m, sn.Triggered)
		return err
	})
}

// RefreshAll recomputes snapshots for every workspace (watchlist.snapshot job; runs as the owner role).
func (s *Service) RefreshAll(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT workspace_id, id FROM watchlist_items`)
	if err != nil {
		return 0, err
	}
	type pair struct{ ws, id uuid.UUID }
	var ps []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.ws, &p.id); err != nil {
			rows.Close()
			return 0, err
		}
		ps = append(ps, p)
	}
	rows.Close()
	for _, p := range ps {
		if err := s.Refresh(ctx, p.ws, p.id); err != nil {
			return 0, err
		}
	}
	return len(ps), nil
}
