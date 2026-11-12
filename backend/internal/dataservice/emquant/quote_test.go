package emquant

import (
	"errors"
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
