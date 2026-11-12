package emquant

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var ErrStaleQuote = errors.New("emquant: quote is stale or has an invalid timestamp")
var ErrInvalidQuote = errors.New("emquant: invalid quote value")

// LastPrice is a validated vendor observation, not a canonical dataset
// record: currency, units, authorization and archive lineage are determined
// by the eventual mapping and governance layer.
type LastPrice struct {
	Code       string
	Value      string
	EventAt    time.Time
	ObservedAt time.Time
}

// LastPriceAt checks the requested security, NOW and TIME before exposing a
// price. Missing, duplicate, non-numeric and non-positive prices fail closed.
func (q Quote) LastPriceAt(code string, asOf time.Time, maxAge time.Duration) (LastPrice, error) {
	if code == "" || strings.TrimSpace(code) != code {
		return LastPrice{}, ErrInvalidQuote
	}
	codeIndex, nowIndex := -1, -1
	for i, candidate := range q.Data.Codes {
		if strings.EqualFold(candidate, code) {
			if codeIndex >= 0 {
				return LastPrice{}, fmt.Errorf("%w: duplicate security", ErrInvalidQuote)
			}
			codeIndex = i
		}
	}
	for i, indicator := range q.Data.Indicators {
		if strings.EqualFold(indicator, "NOW") {
			if nowIndex >= 0 {
				return LastPrice{}, fmt.Errorf("%w: duplicate NOW", ErrInvalidQuote)
			}
			nowIndex = i
		}
	}
	if codeIndex < 0 || nowIndex < 0 {
		return LastPrice{}, fmt.Errorf("%w: security or NOW missing", ErrInvalidQuote)
	}
	event, err := q.EventTime(codeIndex, asOf, maxAge)
	if err != nil {
		return LastPrice{}, err
	}
	cell, err := q.Data.At(codeIndex, nowIndex, 0)
	if err != nil {
		return LastPrice{}, fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	var value string
	switch cell.Type {
	case 10, 11:
		n, ok := cell.Value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
			return LastPrice{}, ErrInvalidQuote
		}
		value = strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return LastPrice{}, fmt.Errorf("%w: unsupported NOW type", ErrInvalidQuote)
	}
	return LastPrice{Code: q.Data.Codes[codeIndex], Value: value, EventAt: event, ObservedAt: q.ObservedAt.UTC()}, nil
}

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
