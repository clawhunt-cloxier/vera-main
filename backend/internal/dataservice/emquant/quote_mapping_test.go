package emquant

import (
	"context"
	"testing"
	"time"
)

func TestMapArchivedQuoteRequiresBoundSourceAndPolicyReferences(t *testing.T) {
	now := time.Now().UTC()
	local := now.In(time.FixedZone("CST", 8*60*60))
	hhmmss := int64(local.Hour()*10000 + local.Minute()*100 + local.Second())
	f := &testFetcher{quote: Quote{ObservedAt: now, Data: Cube{
		Codes: []string{"A.SH"}, Indicators: []string{"NOW", "TIME"}, Dates: []string{local.Format("2006-01-02")},
		Values: []Cell{{Type: 11, Value: 10.25}, {Type: 6, Value: hhmmss}},
	}}}
	s := &testRawStore{ref: "object://test/quote"}
	_, ref, err := (Collector{Fetcher: f, Store: s}).CollectQuote(context.Background(), "A.SH", "TIME,NOW", "")
	if err != nil {
		t.Fatal(err)
	}
	policy := QuoteMappingPolicy{TimeConvention: "hhmmss-asia-shanghai", TimeEvidence: "synthetic-time-proof", PriceUnit: "CNY", PriceEvidence: "synthetic-unit-proof", LatencyMode: "unknown", LicenseRef: "synthetic-license", MaxAge: time.Minute}
	cutoff := time.Now().UTC()
	got, err := MapArchivedQuote(s.body, ref, "A.SH", cutoff, policy)
	if err != nil || got.Price.Value != "10.25" || got.DeclaredLatencyMode != "unknown" || got.Source.BodySHA256 != ref.BodySHA256 || got.Locators["NOW"] != "$.data.Values[0]" || got.Locators["TIME"] != "$.data.Values[1]" {
		t.Fatalf("mapping: %#v, %v", got, err)
	}
	policy.LatencyMode = "real_time"
	if _, err = MapArchivedQuote(s.body, ref, "A.SH", cutoff, policy); err == nil {
		t.Fatal("unverified realtime status accepted")
	}
	policy.LatencyEvidence = "synthetic-latency-proof"
	if _, err = MapArchivedQuote(s.body, ref, "A.SH", cutoff, policy); err != nil {
		t.Fatalf("referenced policy: %v", err)
	}
	policy.TimeEvidence = ""
	if _, err = MapArchivedQuote(s.body, ref, "A.SH", cutoff, policy); err == nil {
		t.Fatal("unverified time convention accepted")
	}
	policy.TimeEvidence = "synthetic-time-proof"
	if _, err = MapArchivedQuote(s.body, ref, "B.SZ", cutoff, policy); err == nil {
		t.Fatal("wrong request accepted")
	}
	if _, err = MapArchivedQuote(append(s.body, 'x'), ref, "A.SH", cutoff, policy); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	if _, err = MapArchivedQuote(s.body, ref, "A.SH", ref.ArchivedAt.Add(-time.Nanosecond), policy); err == nil {
		t.Fatal("archive after cutoff accepted")
	}
	if _, err = MapArchivedQuote(s.body, ref, "A.SH", cutoff.Add(time.Minute), policy); err == nil {
		t.Fatal("stale quote accepted")
	}
}

func TestMapArchivedQuoteMapsTypedTradingStatus(t *testing.T) {
	now := time.Now().UTC()
	local := now.In(time.FixedZone("CST", 8*60*60))
	hhmmss := int64(local.Hour()*10000 + local.Minute()*100 + local.Second())
	f := &testFetcher{quote: Quote{ObservedAt: now, Data: Cube{
		Codes: []string{"A.SH"}, Indicators: []string{"TIME", "NOW", "TRADESTATUS"}, Dates: []string{local.Format("2006-01-02")},
		Values: []Cell{{Type: 6, Value: hhmmss}, {Type: 11, Value: 10.25}, {Type: 6, Value: int64(1)}},
	}}}
	s := &testRawStore{ref: "object://test/status"}
	_, ref, err := (Collector{Fetcher: f, Store: s}).CollectQuote(context.Background(), "A.SH", "TIME,NOW,TRADESTATUS", "")
	if err != nil {
		t.Fatal(err)
	}
	policy := QuoteMappingPolicy{
		TimeConvention: "hhmmss-asia-shanghai", TimeEvidence: "time-proof", PriceUnit: "CNY", PriceEvidence: "unit-proof",
		LatencyMode: "real_time", LatencyEvidence: "latency-proof", LicenseRef: "license", MaxAge: time.Minute,
		StatusIndicator: "TRADESTATUS", StatusEvidence: "status-proof", StatusMapping: map[string]string{"i:1": "continuous", "i:2": "suspended"},
	}
	got, err := MapArchivedQuote(s.body, ref, "A.SH", now.Add(time.Second), policy)
	if err != nil || got.MarketStatus != "continuous" || got.Locators["TRADESTATUS"] != "$.data.Values[2]" {
		t.Fatalf("status mapping: %#v, %v", got, err)
	}
	for _, invalid := range []map[string]string{
		{"i:01": "continuous"}, {"i:+1": "continuous"}, {"i:1x": "continuous"}, {"u:-1": "continuous"}, {"x:1": "continuous"},
	} {
		policy.StatusMapping = invalid
		if _, err := QuotePolicyFields(policy); err == nil {
			t.Fatalf("invalid typed status mapping accepted: %#v", invalid)
		}
	}
	policy.StatusMapping = map[string]string{"i:99": "continuous"}
	got, err = MapArchivedQuote(s.body, ref, "A.SH", now.Add(time.Second), policy)
	if err != nil || got.MarketStatus != "unknown" {
		t.Fatalf("unmapped status should remain unknown: %#v, %v", got, err)
	}
}
