package emquant

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

// QuoteProbe is diagnostic metadata. It has no price or archive reference
// and cannot be used as a published research observation.
type QuoteProbe struct {
	Code        string     `json:"code"`
	SDKCalled   bool       `json:"sdk_called"`
	VendorCode  *int       `json:"vendor_code,omitempty"`
	ReceivedAt  *time.Time `json:"received_at,omitempty"`
	VendorDate  string     `json:"vendor_date,omitempty"`
	VendorTime  *int64     `json:"vendor_time,omitempty"`
	PositiveNOW bool       `json:"positive_now"`
}

func ValidQuoteProbeCode(code string) bool { return validBriefCode(code) }

func (r *Runtime) ProbeQuote(ctx context.Context, code string) (QuoteProbe, error) {
	p := QuoteProbe{Code: code}
	if !ValidQuoteProbeCode(code) || r == nil || r.collector.Gate == nil || r.collector.Fetcher == nil {
		return p, errors.New("emquant: invalid quote probe or missing gated SDK")
	}
	var q Quote
	err := r.collector.Gate.Do(ctx, func() error {
		p.SDKCalled = true
		result, err := r.collector.Fetcher.QuoteSnapshot(code, "TIME,NOW", "")
		if err == nil {
			zero := 0
			p.VendorCode, p.ReceivedAt = &zero, &result.ObservedAt
			q = result
		} else {
			var sdk *SDKError
			if errors.As(err, &sdk) {
				p.VendorCode = &sdk.Code
			}
		}
		return err
	})
	if err != nil {
		return p, err
	}
	if len(q.Data.Codes) != 1 || !strings.EqualFold(q.Data.Codes[0], code) || len(q.Data.Indicators) != 2 || len(q.Data.Dates) != 1 || q.ObservedAt.IsZero() {
		return p, ErrShape
	}
	day, err := time.Parse("2006-01-02", q.Data.Dates[0])
	if err != nil {
		return p, ErrShape
	}
	p.VendorDate = day.Format("2006-01-02")
	for i, field := range q.Data.Indicators {
		cell, err := q.Data.At(0, i, 0)
		if err != nil {
			return p, ErrShape
		}
		switch strings.ToUpper(field) {
		case "TIME":
			v, ok := cell.Value.(int64)
			if ok && cell.Type == 6 && v >= 0 && v <= 235959 && v/100%100 < 60 && v%100 < 60 {
				p.VendorTime = &v
			}
		case "NOW":
			v, ok := cell.Value.(float64)
			p.PositiveNOW = ok && (cell.Type == 10 || cell.Type == 11) && v > 0 && !math.IsInf(v, 0)
		}
	}
	if p.VendorTime == nil || !p.PositiveNOW {
		return p, ErrInvalidQuote
	}
	return p, nil
}
