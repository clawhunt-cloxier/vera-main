// Package harness runs research tasks: plan → collect → compute → write → verify → summarise → render → deliver.
package harness

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// Bundle is the in-memory view of a frozen snapshot used by fact builders.
type Bundle struct {
	AsOf     time.Time
	Subject  dataservice.EntityRow
	Entities map[uuid.UUID]dataservice.EntityRow
	byKey    map[string][]dataservice.StoredRecord // dataset|entity → records sorted by period
	global   map[string][]dataservice.StoredRecord // dataset → entity-less records
	Days     []string                              // open trading days ≤ as_of, ascending
	IDs      []uuid.UUID
	Members  []uuid.UUID // industry/market members used for aggregation
	Peers    []uuid.UUID
}

func key(ds string, e uuid.UUID) string { return ds + "|" + e.String() }

func newBundle(asOf time.Time, subject dataservice.EntityRow, ents map[uuid.UUID]dataservice.EntityRow, recs []dataservice.StoredRecord) *Bundle {
	b := &Bundle{AsOf: asOf, Subject: subject, Entities: ents, byKey: map[string][]dataservice.StoredRecord{},
		global: map[string][]dataservice.StoredRecord{}}
	for _, r := range recs {
		b.IDs = append(b.IDs, r.ID)
		if r.EntityID == nil {
			b.global[r.DatasetID] = append(b.global[r.DatasetID], r)
			continue
		}
		k := key(r.DatasetID, *r.EntityID)
		b.byKey[k] = append(b.byKey[k], r)
	}
	for _, r := range b.global["ref.trading_calendar"] {
		if v, _ := r.Payload["is_open"].(bool); v {
			b.Days = append(b.Days, str(r, "trade_date"))
		}
	}
	slices.Sort(b.Days)
	return b
}

func (b *Bundle) Rows(ds string, e uuid.UUID) []dataservice.StoredRecord { return b.byKey[key(ds, e)] }
func (b *Bundle) Has(ds string, e uuid.UUID) bool                        { return len(b.byKey[key(ds, e)]) > 0 }

// Index returns index bars for an index code, ascending by trade date.
func (b *Bundle) Index(code string) []dataservice.StoredRecord {
	var out []dataservice.StoredRecord
	for _, r := range b.global["mkt.index_bar"] {
		if str(r, "index_code") == code {
			out = append(out, r)
		}
	}
	return out
}

func (b *Bundle) Global(ds string) []dataservice.StoredRecord { return b.global[ds] }

// Window returns the last n open trading days.
func (b *Bundle) Window(n int) []string {
	if n > len(b.Days) {
		n = len(b.Days)
	}
	return b.Days[len(b.Days)-n:]
}

func str(r dataservice.StoredRecord, f string) string {
	s, _ := r.Payload[f].(string)
	return s
}

func dec(r dataservice.StoredRecord, f string) (decimal.Decimal, bool) {
	s, ok := r.Payload[f].(string)
	if !ok {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(s)
	return d, err == nil
}

func boolean(r dataservice.StoredRecord, f string) bool {
	v, _ := r.Payload[f].(bool)
	return v
}

func num(r dataservice.StoredRecord, f string) int {
	switch v := r.Payload[f].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// Loader resolves entity groups and loads records for a skill.
type Loader struct {
	Store *dataservice.Store
}

// Members returns stocks classified into an industry (level-2 code) as of asOf, using point-in-time ref.industry.
func (l Loader) Members(ctx context.Context, industryCode string, asOf time.Time) ([]uuid.UUID, error) {
	recs, err := l.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: []string{"ref.industry"}, AsOf: asOf})
	if err != nil {
		return nil, err
	}
	day := asOf.Format("2006-01-02")
	latest := map[uuid.UUID]dataservice.StoredRecord{}
	for _, r := range recs {
		if r.EntityID == nil || str(r, "valid_from") > day {
			continue
		}
		if to := str(r, "valid_to"); to != "" && to < day {
			continue
		}
		if cur, ok := latest[*r.EntityID]; !ok || str(r, "valid_from") > str(cur, "valid_from") {
			latest[*r.EntityID] = r
		}
	}
	var out []uuid.UUID
	for id, r := range latest {
		if industryCode == "" || str(r, "level2_code") == industryCode {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out, nil
}

// Load fetches the given datasets for entity groups plus global datasets.
func (l Loader) Load(ctx context.Context, asOf time.Time, groups map[uuid.UUID][]string, global []string) ([]dataservice.StoredRecord, error) {
	byDatasets := map[string][]uuid.UUID{}
	for e, dss := range groups {
		slices.Sort(dss)
		k := ""
		for _, d := range dss {
			k += d + ","
		}
		byDatasets[k] = append(byDatasets[k], e)
	}
	var out []dataservice.StoredRecord
	for k, ents := range byDatasets {
		dss := splitComma(k)
		recs, err := l.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: dss, EntityIDs: ents, AsOf: asOf})
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	if len(global) > 0 {
		recs, err := l.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: global, AsOf: asOf})
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if r.EntityID == nil {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	return out
}
