package emquant

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestQuoteEventTimeAndFreshness(t *testing.T) {
	asOf := time.Date(2026, 11, 12, 5, 42, 4, 0, time.UTC)
	q := Quote{ObservedAt: asOf, Data: Cube{Codes: []string{"A"}, Indicators: []string{"NOW", "TIME"}, Dates: []string{"2026-11-12"}, Values: []Cell{{Type: 11, Value: 1.0}, {Type: 6, Value: int64(134200)}}}}
	event, err := q.EventTime(0, asOf, 10*time.Second)
	if err != nil || !event.Equal(asOf.Add(-4*time.Second)) {
		t.Fatalf("event=%s err=%v", event, err)
	}
	if _, err := q.EventTime(0, asOf, 3*time.Second); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := q.EventTime(0, asOf.Add(-time.Second), time.Minute); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("observation after cutoff: %v", err)
	}
	q.Data.Values[1].Value = int64(134205)
	if _, err := q.EventTime(0, asOf, time.Minute); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("future event: %v", err)
	}
	q.Data.Values[1].Value = int64(136100)
	if _, err := q.EventTime(0, asOf, time.Minute); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("invalid clock: %v", err)
	}
	q.Data.Values[1].Value = "134200"
	if _, err := q.EventTime(0, asOf, time.Minute); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("unsupported type: %v", err)
	}
}

func TestLastPriceAtValidatesIdentityAndValue(t *testing.T) {
	at := time.Date(2026, 11, 12, 5, 42, 4, 0, time.UTC)
	q := Quote{ObservedAt: at, Data: Cube{Codes: []string{"A.SH", "B.SZ"}, Indicators: []string{"TIME", "NOW"}, Dates: []string{"2026-11-12"}, Values: []Cell{{Type: 6, Value: int64(134200)}, {Type: 11, Value: 10.125}, {Type: 6, Value: int64(134201)}, {Type: 11, Value: 20.5}}}}
	price, err := q.LastPriceAt("b.sz", at, 10*time.Second)
	if err != nil || price.Code != "B.SZ" || price.Value != "20.5" || !price.EventAt.Equal(at.Add(-3*time.Second)) || !price.ObservedAt.Equal(at) {
		t.Fatalf("price=%+v err=%v", price, err)
	}
	if _, err := q.LastPriceAt("C.SH", at, 10*time.Second); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("wrong security: %v", err)
	}
	if _, err := q.LastPriceAt("B.SZ", at, time.Second); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("stale quote: %v", err)
	}
	for _, cell := range []Cell{{Type: 0}, {Type: 11, Value: 0.0}, {Type: 11, Value: math.NaN()}, {Type: 11, Value: math.Inf(1)}, {Type: 13, Value: "20.5"}} {
		q.Data.Values[3] = cell
		if _, err := q.LastPriceAt("B.SZ", at, 10*time.Second); !errors.Is(err, ErrInvalidQuote) {
			t.Fatalf("invalid value %#v: %v", cell, err)
		}
	}
	q.Data.Codes[0] = "b.sz"
	if _, err := q.LastPriceAt("B.SZ", at, 10*time.Second); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("duplicate security: %v", err)
	}
}

func TestQuoteRejectsMissingAndAmbiguousTime(t *testing.T) {
	asOf := time.Date(2026, 11, 12, 5, 42, 4, 0, time.UTC)
	q := Quote{ObservedAt: asOf, Data: Cube{Codes: []string{"A"}, Indicators: []string{"NOW"}, Dates: []string{"2026-11-12"}, Values: []Cell{{Type: 11, Value: 1.0}}}}
	if _, err := q.EventTime(0, asOf, time.Minute); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("missing TIME: %v", err)
	}
	q.Data.Indicators = []string{"TIME", "time"}
	if _, err := q.EventTime(0, asOf, time.Minute); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("duplicate TIME: %v", err)
	}
}
