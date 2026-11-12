package emquant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrStaleQuote = errors.New("emquant: quote is stale or has an invalid timestamp")

// EventTime validates a quote against the research cutoff. Vendor TIME is
// HHMMSS in China local time; this interpretation is limited to the observed
// integer form. Unsupported timestamp forms fail closed until verified.
// maxAge must be chosen by the caller's registered data policy.
func (q Quote) EventTime(code int, asOf time.Time, maxAge time.Duration) (time.Time, error) {
	if asOf.IsZero() || q.ObservedAt.IsZero() || maxAge <= 0 || q.ObservedAt.After(asOf) || len(q.Data.Dates) != 1 {
		return time.Time{}, ErrStaleQuote
	}
	index := -1
	for i, name := range q.Data.Indicators {
		if strings.EqualFold(name, "TIME") {
			if index != -1 {
				return time.Time{}, fmt.Errorf("%w: duplicate TIME", ErrStaleQuote)
			}
			index = i
		}
	}
	if index < 0 {
		return time.Time{}, fmt.Errorf("%w: missing TIME", ErrStaleQuote)
	}
	cell, err := q.Data.At(code, index, 0)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrStaleQuote, err)
	}
	n, ok := cell.Value.(int64)
	if !ok || cell.Type != 6 || n < 0 || n > 235959 {
		return time.Time{}, fmt.Errorf("%w: unsupported TIME", ErrStaleQuote)
	}
	hour, minute, second := n/10000, (n/100)%100, n%100
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, fmt.Errorf("%w: invalid TIME", ErrStaleQuote)
	}
	china := time.FixedZone("CST", 8*60*60)
	day, err := time.ParseInLocation("2006-01-02", q.Data.Dates[0], china)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid date", ErrStaleQuote)
	}
	event := day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute + time.Duration(second)*time.Second)
	if event.After(q.ObservedAt) || event.After(asOf) || asOf.Sub(event) > maxAge {
		return time.Time{}, ErrStaleQuote
	}
	return event.UTC(), nil
}
