package emquant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStockCodeCandidatesArchivePreservesAmbiguity(t *testing.T) {
	f := &testFetcher{table: Table{Columns: []string{"CODE", "FULLCODES", "SYNTHETIC_ID"}, Rows: 1, Values: []Cell{{Type: 13, Value: "000001"}, {Type: 13, Value: "000001.SZ,000001.SH"}, {Type: 8, Value: int64(9007199254740993)}}}}
	s := &testRawStore{ref: "s3://test/code"}
	c := Collector{Fetcher: f, Store: s}
	got, ref, err := c.CollectStockCodeCandidates(context.Background(), "000001")
	if err != nil || got.Rows != 1 || ref.BodySHA256 != digest(s.body) || ref.RequestFingerprint == "" {
		t.Fatalf("archive: %v", err)
	}
	replayed, observed, fingerprint, err := DecodeTableArchive(s.body, ref.BodySHA256)
	if err != nil || !observed.Equal(ref.ReceivedAt) || fingerprint != ref.RequestFingerprint || replayed.Values[1].Value != "000001.SZ,000001.SH" || replayed.Values[2].Value != int64(9007199254740993) {
		t.Fatalf("table replay: %v", err)
	}
	if _, _, _, err := DecodeTableArchive(s.body, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong hash accepted")
	}
	before := f.calls
	for _, codes := range []string{"", "000001.SZ", "000001,000001", "abcdef", "000001;"} {
		if _, _, err := c.CollectStockCodeCandidates(context.Background(), codes); err == nil || f.calls != before {
			t.Fatal("invalid code query reached the vendor")
		}
	}
	s.err = errors.New("store unavailable")
	if result, _, err := c.CollectStockCodeCandidates(context.Background(), "000001"); err == nil || len(result.Values) != 0 {
		t.Fatal("archive failure exposed candidates")
	}
}

func TestStockNameCandidates(t *testing.T) {
	f := &testFetcher{table: Table{Columns: []string{"SECURITYSHORTNAME", "SECUCODE"}, Rows: 2, Values: []Cell{{Type: 13, Value: "模拟"}, {Type: 13, Value: "A"}, {Type: 13, Value: "模拟"}, {Type: 13, Value: "B"}}}}
	store := &testRawStore{ref: "object://test"}
	c := Collector{Fetcher: f, Store: store}
	result, ref, err := c.CollectStockNameCandidates(context.Background(), "模拟证券")
	if err != nil || result.Rows != 2 || ref.BodySHA256 != digest(store.body) {
		t.Fatalf("name archive: %v", err)
	}
	if strings.Contains(string(store.body), "SecuName=") {
		t.Fatal("request options leaked")
	}
	before := f.calls
	for _, name := range []string{"", " 模拟", "模拟,Priority=1", "模拟\n证券", strings.Repeat("a", 33), string([]byte{0xff})} {
		if _, _, err := c.CollectStockNameCandidates(context.Background(), name); err == nil || f.calls != before {
			t.Fatal("invalid name reached vendor")
		}
	}
	store.err = errors.New("unavailable")
	if result, _, err := c.CollectStockNameCandidates(context.Background(), "模拟证券"); err == nil || len(result.Values) != 0 {
		t.Fatal("archive failure exposed candidates")
	}
}
