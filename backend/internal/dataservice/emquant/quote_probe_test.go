package emquant

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuoteProbeUsesAccountGateAndChecksResponse(t *testing.T) {
	ctx := context.Background()
	f := &testFetcher{quote: Quote{ObservedAt: time.Now(), Data: Cube{Codes: []string{"600000.SH"}, Indicators: []string{"NOW", "TIME"}, Dates: []string{"2026-11-18"}, Values: []Cell{{Type: 11, Value: 23.5}, {Type: 6, Value: int64(150000)}}}}}
	r := Runtime{collector: Collector{Fetcher: f, Gate: rejectGate{}}}
	if p, err := r.ProbeQuote(ctx, "600000.SH"); !errors.Is(err, ErrRateLimited) || p.SDKCalled || f.calls != 0 {
		t.Fatal(p, err)
	}
	r.collector.Gate = testGateFunc(func(_ context.Context, fn func() error) error { return fn() })
	if p, err := r.ProbeQuote(ctx, "600000.SH"); err != nil || !p.SDKCalled || p.VendorCode == nil || *p.VendorCode != 0 || p.VendorTime == nil || *p.VendorTime != 150000 || !p.PositiveNOW || f.calls != 1 {
		t.Fatal(p, err)
	}
	if _, err := r.ProbeQuote(ctx, "000001.SZ"); !errors.Is(err, ErrShape) {
		t.Fatal(err)
	}
	f.quote.Data.Values[0] = Cell{}
	if p, err := r.ProbeQuote(ctx, "600000.SH"); !errors.Is(err, ErrInvalidQuote) || p.PositiveNOW {
		t.Fatal(p, err)
	}
	f.err = &SDKError{Operation: "csqsnapshot", Code: 10000016}
	if p, err := r.ProbeQuote(ctx, "600000.SH"); !errors.Is(err, ErrRateLimited) || p.VendorCode == nil || *p.VendorCode != 10000016 {
		t.Fatal(p, err)
	}
}
