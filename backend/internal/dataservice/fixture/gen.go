package fixture

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"time"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

var cst = time.FixedZone("CST", 8*3600)

// gen holds deterministic generation state.
type gen struct {
	m       *Manifest
	days    []time.Time // trading days, ascending, up to as_of date
	records []dataservice.Record
	bars    map[string][]bar // code -> bars
	fin     map[string]map[string]finYear
}

type bar struct {
	day                              time.Time
	open, high, low, close, preClose float64
	volume, amount                   float64
	suspended                        bool
}

func rngFor(seed int64, key string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return rand.New(rand.NewPCG(uint64(seed), h.Sum64()))
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func at(day time.Time, hour int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, cst)
}

func dp(t time.Time) *time.Time { return &t }

func dec(v float64, places int32) string {
	return decimal.NewFromFloat(v).Round(places).StringFixed(places)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// fictional holidays keep the calendar deterministic without claiming to be the real exchange calendar.
func holiday(d time.Time) bool {
	m, day := d.Month(), d.Day()
	switch {
	case m == time.January && day == 1:
		return true
	case m == time.February && day >= 3 && day <= 7:
		return true
	case m == time.May && day <= 3:
		return true
	case m == time.October && day <= 7:
		return true
	}
	return false
}

func (g *gen) calendar() {
	start, _ := time.Parse("2006-01-02", g.m.HistoryStart)
	end := g.m.AsOf.In(cst)
	last := date(end.Year(), end.Month(), end.Day())
	for d := start; !d.After(last); d = d.AddDate(0, 0, 1) {
		open := d.Weekday() != time.Saturday && d.Weekday() != time.Sunday && !holiday(d)
		if open {
			g.days = append(g.days, d)
		}
		g.add(dataservice.Record{DatasetID: "ref.trading_calendar", RecordKey: d.Format("2006-01-02"), Period: dp(d),
			AvailableAt: at(start, 0), Origin: "exchange", SourceTier: "L1",
			Payload: map[string]any{"trade_date": d.Format("2006-01-02"), "is_open": open}})
	}
}

func (g *gen) add(r dataservice.Record) {
	if r.Version == 0 {
		r.Version = 1
	}
	g.records = append(g.records, r)
}

// dayIndex returns the index of the last trading day <= d.
func (g *gen) dayIndex(d time.Time) int {
	i := len(g.days) - 1
	for i > 0 && g.days[i].After(d) {
		i--
	}
	return i
}
