package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type testFetcher struct {
	cube  Cube
	quote Quote
	err   error
	calls int
}

func (f *testFetcher) CSD(_, _, _, _, _ string) (Cube, error)      { f.calls++; return f.cube, f.err }
func (f *testFetcher) QuoteSnapshot(_, _, _ string) (Quote, error) { f.calls++; return f.quote, f.err }

type testRawStore struct {
	hash  string
	body  []byte
	ref   string
	err   error
	calls int
}

func (s *testRawStore) Put(_ context.Context, hash string, body []byte) (string, error) {
	s.calls++
	s.hash = hash
	s.body = append([]byte(nil), body...)
	return s.ref, s.err
}

func TestCollectorArchivesBeforeReturning(t *testing.T) {
	cube := Cube{Codes: []string{"A"}, Indicators: []string{"X"}, Dates: []string{"2026-11-12"}, Values: []Cell{{Type: 11, Value: 12.5}}}
	f := &testFetcher{cube: cube}
	s := &testRawStore{ref: "object://test/1"}
	got, ref, err := (Collector{Fetcher: f, Store: s}).CollectCSD(context.Background(), "A", "X", "2026-11-12", "2026-11-12", "private-option")
	if err != nil || len(got.Values) != 1 || s.calls != 1 || ref.BodyRef != s.ref || ref.BodySHA256 != digest(s.body) || ref.BodySHA256 != s.hash || len(ref.RequestFingerprint) != 64 {
		t.Fatalf("archive failed: ref=%+v err=%v", ref, err)
	}
	if strings.Contains(string(s.body), "private-option") {
		t.Fatal("request options leaked into archive")
	}
	var body archiveBody
	if err := json.Unmarshal(s.body, &body); err != nil || body.Format != "emquant-sdk-decoded-v1" || body.ReceivedAt.IsZero() {
		t.Fatalf("invalid archived body: %v", err)
	}
	_, ref2, err := (Collector{Fetcher: f, Store: s}).CollectCSD(context.Background(), "A", "X", "2026-11-12", "2026-11-12", "different-option")
	if err != nil || ref.RequestFingerprint == ref2.RequestFingerprint {
		t.Fatalf("request fingerprint did not change: %v", err)
	}
}

func TestCollectorFailsClosed(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{"A"}}}
	s := &testRawStore{err: errors.New("storage unavailable")}
	c := Collector{Fetcher: f, Store: s}
	if got, _, err := c.CollectCSD(context.Background(), "A", "X", "D1", "D2", ""); err == nil || len(got.Codes) != 0 {
		t.Fatal("storage failure exposed data")
	}
	s.err = nil
	if _, _, err := c.CollectCSD(context.Background(), "A", "X", "D1", "D2", ""); err == nil {
		t.Fatal("empty reference accepted")
	}
	f.err = errors.New("SDK unavailable")
	s.ref = "object://test/1"
	calls := s.calls
	if _, _, err := c.CollectCSD(context.Background(), "A", "X", "D1", "D2", ""); err == nil || s.calls != calls {
		t.Fatal("failed SDK request was archived")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = f.calls
	if _, _, err := c.CollectCSD(ctx, "A", "X", "D1", "D2", ""); !errors.Is(err, context.Canceled) || f.calls != calls {
		t.Fatal("cancelled request reached SDK")
	}
}

func TestCollectQuotePreservesObservationTime(t *testing.T) {
	at := time.Date(2026, 11, 12, 5, 42, 4, 0, time.UTC)
	f := &testFetcher{quote: Quote{ObservedAt: at, Data: Cube{Codes: []string{"A"}}}}
	s := &testRawStore{ref: "object://test/quote"}
	_, ref, err := (Collector{Fetcher: f, Store: s}).CollectQuote(context.Background(), "A", "TIME,NOW", "")
	if err != nil || !ref.ReceivedAt.Equal(at) {
		t.Fatalf("observation time: %+v, %v", ref, err)
	}
	f.quote.ObservedAt = time.Time{}
	if _, _, err = (Collector{Fetcher: f, Store: s}).CollectQuote(context.Background(), "A", "TIME,NOW", ""); err == nil {
		t.Fatal("missing observation time accepted")
	}
}

func TestDecodeArchiveRetainsTypesAndChecksIntegrity(t *testing.T) {
	at := time.Date(2026, 11, 12, 5, 42, 4, 0, time.UTC)
	cube := Cube{Codes: []string{"A"}, Indicators: []string{"TIME", "SIGNED", "UNSIGNED", "FLOAT", "TEXT", "NULL"}, Dates: []string{"2026-11-12"}, Values: []Cell{{Type: 6, Value: int64(134200)}, {Type: 8, Value: int64(9007199254740993)}, {Type: 9, Value: uint64(18446744073709551615)}, {Type: 11, Value: 1.25}, {Type: 13, Value: "交易"}, {Type: 0}}}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v1", ReceivedAt: at, Data: cube})
	if err != nil {
		t.Fatal(err)
	}
	restored, received, err := DecodeArchive(body, digest(body))
	if err != nil || !received.Equal(at) || restored.Values[1].Value != int64(9007199254740993) || restored.Values[2].Value != uint64(18446744073709551615) || restored.Values[4].Value != "交易" {
		t.Fatalf("replay: %#v, %v", restored, err)
	}
	if _, _, err = DecodeArchive(body, strings.Repeat("0", 64)); err == nil {
		t.Fatal("incorrect hash accepted")
	}
	bad := append(append([]byte(nil), body...), []byte(" {}")...)
	if _, _, err = DecodeArchive(bad, digest(bad)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	cube.Values[0] = Cell{Type: 6, Value: "bad"}
	bad, err = json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v1", ReceivedAt: at, Data: cube})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = DecodeArchive(bad, digest(bad)); err == nil {
		t.Fatal("wrong value type accepted")
	}
}
