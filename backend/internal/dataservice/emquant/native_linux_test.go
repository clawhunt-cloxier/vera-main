//go:build linux && cgo

package emquant

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

func TestOpenRejectsInvalidAndMissingLibrary(t *testing.T) {
	for _, path := range []string{"relative.so", filepath.Join(t.TempDir(), "missing.so")} {
		if c, err := Open(path, "user", "pass"); err == nil {
			_ = c.Close()
			t.Fatalf("Open(%q) unexpectedly succeeded", path)
		}
	}
}

func TestNativeLifecycleAndResponse(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "fake.so")
	if output, err := exec.Command("cc", "-shared", "-fPIC", "-o", lib, "testdata/fake.c").CombinedOutput(); err != nil {
		t.Fatalf("compile fake library: %v: %s", err, output)
	}
	if _, err := Open(lib, "user", "wrong"); err == nil || !strings.Contains(err.Error(), "77") {
		t.Fatalf("login error: %v", err)
	}
	c, err := Open(lib, "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	copyOfClient := *c
	if err := copyOfClient.Close(); err == nil {
		t.Fatal("copied client was accepted")
	}
	if _, err = Open(lib, "user", "pass"); err == nil {
		t.Fatal("second session accepted")
	}
	result, err := c.CSD("A,B", "X,Y", "D1", "D2", "")
	if err != nil {
		t.Fatal(err)
	}
	value, err := result.At(1, 0, 1)
	if err != nil || value.Value != int64(6) {
		t.Fatalf("value: %#v, %v", value, err)
	}
	value, err = result.At(1, 1, 1)
	if err != nil || value.Type != 0 || value.Value != nil {
		t.Fatalf("null: %#v, %v", value, err)
	}
	if _, err = c.CSD("TRUNCATED", "X", "D1", "D2", ""); err == nil {
		t.Fatal("accepted truncated result")
	}
	if _, err = c.CSD("BADUTF", "X", "D1", "D2", ""); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	if _, err = c.CSD("BADLENGTH", "X", "D1", "D2", ""); err == nil {
		t.Fatal("accepted invalid SDK string length")
	}
	if _, err = c.CSD("ERROR", "X", "D1", "D2", ""); err == nil || !strings.Contains(err.Error(), "88") {
		t.Fatalf("query error: %v", err)
	}
	if _, err = c.CSD("A", "X", "D1", "D2", "FILLDATA=1"); err == nil {
		t.Fatal("carry-forward option accepted")
	}
	if _, err = c.CSS("A", "X", "SHOWBLANK=0"); err == nil {
		t.Fatal("blank replacement option accepted")
	}
	snapshot, err := c.CSS("A,B", "X,Y", "")
	if err != nil {
		t.Fatal(err)
	}
	if value, err = snapshot.At(1, 0, 1); err != nil || value.Value != int64(6) {
		t.Fatalf("snapshot: %#v, %v", value, err)
	}
	if _, err = c.CSS("ERROR", "X", ""); err == nil || !strings.Contains(err.Error(), "88") {
		t.Fatalf("snapshot error: %v", err)
	}
	before := time.Now().UTC()
	quote, err := c.QuoteSnapshot("A,B", "X,Y", "")
	if err != nil || quote.ObservedAt.Before(before) || quote.ObservedAt.After(time.Now().UTC()) {
		t.Fatalf("quote timing: %v", err)
	}
	if value, err = quote.Data.At(1, 0, 1); err != nil || value.Value != int64(6) {
		t.Fatalf("quote value: %#v, %v", value, err)
	}
	if _, err = c.QuoteSnapshot("ERROR", "X", ""); err == nil || !strings.Contains(err.Error(), "88") {
		t.Fatalf("quote error: %v", err)
	}
	news, err := c.CFN("A,B", "regularreport", 2, "count=10")
	if err != nil {
		t.Fatal(err)
	}
	if value, err = news.At(1, 0, 1); err != nil || value.Value != int64(6) {
		t.Fatalf("news: %#v, %v", value, err)
	}
	if _, err = c.CFN("A", "regularreport", 3, "count=10"); err == nil {
		t.Fatal("invalid news mode accepted")
	}
	if _, err = c.CFN("ERROR", "regularreport", 2, "count=10"); err == nil || !strings.Contains(err.Error(), "88") {
		t.Fatalf("news error: %v", err)
	}
	if data, err := c.CSD("DISCONNECT", "X", "D1", "D2", ""); !errors.Is(err, ErrUnavailable) || len(data.Values) != 0 {
		t.Fatalf("disconnect callback returned data: %v", err)
	}
	if _, err := c.CSD("A", "X", "D1", "D2", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("subsequent call ignored disconnect: %v", err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CSD("A", "X", "D1", "D2", ""); err == nil {
		t.Fatal("closed client accepted query")
	}
	var zero Client
	if err = zero.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestLiveCFN is separate because account permission and response shape for
// announcements have not been verified. It never logs response contents.
func TestLiveCFN(t *testing.T) {
	if os.Getenv("EMQUANT_INTEGRATION") != "1" || os.Getenv("EMQUANT_LIVE_CFN") != "1" {
		t.Skip("live vendor CFN test not requested")
	}
	c, err := Open(os.Getenv("EMQUANT_LIBRARY"), os.Getenv("EMQUANT_USER"), os.Getenv("EMQUANT_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := &testRawStore{ref: "object://test/cfn-live"}
	cube, ref, err := (Collector{Fetcher: c, Store: store}).CollectCFN(context.Background(), "300059.SZ", "regularreport", 2, "count=5")
	if err != nil {
		t.Fatal(err)
	}
	if len(cube.Codes) == 0 || len(cube.Indicators) == 0 || len(cube.Values) == 0 || ref.BodySHA256 != digest(store.body) {
		t.Fatal("unexpected or empty CFN response")
	}
	if _, _, err := DecodeArchive(store.body, ref.BodySHA256); err != nil {
		t.Fatalf("CFN archive replay: %v", err)
	}
}

// This test is explicitly opt-in: it consumes vendor quota and needs a local
// runtime copy. It never prints credentials or market data.
func TestLiveSDK(t *testing.T) {
	if os.Getenv("EMQUANT_INTEGRATION") != "1" {
		t.Skip("live vendor SDK test not requested")
	}
	c, err := Open(os.Getenv("EMQUANT_LIBRARY"), os.Getenv("EMQUANT_USER"), os.Getenv("EMQUANT_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := Open(os.Getenv("EMQUANT_LIBRARY"), "x", "y"); err == nil {
		t.Fatal("second session was accepted")
	}
	liquidityStore := &testRawStore{ref: "object://test/daily"}
	liquidity, err := (Collector{Fetcher: c, Store: liquidityStore}).CollectDailyLiquidity(context.Background(), "300059.SZ", "2026-11-02", "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(liquidity) != 1 || liquidity[0].VolumeShares == "" || liquidity[0].AmountCNY == "" || liquidity[0].TurnoverRatio == nil {
		t.Fatal("unexpected daily liquidity response")
	}
	status, err := c.CSD("300059.SZ", "TRADESTATUS", "2026-11-02", "2026-11-02", "Period=1")
	if err != nil {
		t.Fatal(err)
	}
	statusValue, err := status.At(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := statusValue.Value.(string)
	if !ok {
		t.Fatalf("unexpected status cell type %d", statusValue.Type)
	}
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		t.Fatal("status response did not contain Chinese text")
	}
	statementStore := &testRawStore{ref: "object://test/statement"}
	snapshot, statementRef, err := (Collector{Fetcher: c, Store: statementStore}).CollectCSS(context.Background(), "300059.SZ", "IncomeStatement_9,IncomeStatement_61", "ReportDate=20251231,Type=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Codes) != 1 || len(snapshot.Indicators) != 2 || len(snapshot.Dates) != 1 {
		t.Fatal("unexpected CSS dimensions")
	}
	for i := range snapshot.Indicators {
		cell, err := snapshot.At(0, i, 0)
		if err != nil || (cell.Type != 10 && cell.Type != 11) {
			t.Fatalf("statement field %d has unsupported type %d: %v", i, cell.Type, err)
		}
	}
	if _, _, err := DecodeArchive(statementStore.body, statementRef.BodySHA256); err != nil {
		t.Fatal(err)
	}
	calendarStore := &testRawStore{ref: "object://test/calendar"}
	days, calendarRef, err := (Collector{Fetcher: c, Store: calendarStore}).CollectOpenDates(context.Background(), "CNSESH", "2026-11-02", "2026-11-06")
	if err != nil || len(days) == 0 {
		t.Fatalf("calendar query: %v", err)
	}
	records, err := MapOpenDates(calendarStore.body, calendarRef, "CNSESH", "2026-11-02", "2026-11-06")
	if err != nil || len(records) != len(days) {
		t.Fatalf("calendar mapping: %v", err)
	}
	catalog, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := catalog.Validate(record.Record); err != nil {
			t.Fatal(err)
		}
	}
	store := &testRawStore{ref: "object://test/live"}
	quote, ref, err := (Collector{Fetcher: c, Store: store}).CollectQuote(context.Background(), "300059.SZ", "TIME,NOW,VOLUME", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(quote.Data.Codes) != 1 || len(quote.Data.Indicators) != 3 || len(quote.Data.Dates) != 1 || quote.ObservedAt.IsZero() {
		t.Fatal("unexpected quote response dimensions")
	}
	for i := range quote.Data.Indicators {
		if _, err = quote.Data.At(0, i, 0); err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(quote.Data.Indicators[i], "TIME") {
			v, _ := quote.Data.At(0, i, 0)
			t.Logf("vendor TIME type=%d value=%v; date axis=%s; observed=%s", v.Type, v.Value, quote.Data.Dates[0], quote.ObservedAt.Format(time.RFC3339))
		}
	}
	if _, err := quote.EventTime(0, quote.ObservedAt, 7*24*time.Hour); err != nil {
		t.Fatalf("quote event time: %v", err)
	}
	if _, err := quote.LastPriceAt("300059.SZ", quote.ObservedAt, 7*24*time.Hour); err != nil {
		t.Fatalf("quote last price: %v", err)
	}
	replayed, received, err := DecodeArchive(store.body, ref.BodySHA256)
	if err != nil || !received.Equal(quote.ObservedAt) || len(replayed.Indicators) != 3 {
		t.Fatalf("live archive replay failed: %v", err)
	}
	if _, err = (Quote{Data: replayed, ObservedAt: received}).EventTime(0, received, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
}

// TestLiveArchiveS3 must be opted into explicitly: it writes authorized real
// vendor data into the specified S3 bucket and consumes a vendor request.
func TestLiveArchiveS3(t *testing.T) {
	if os.Getenv("EMQUANT_INTEGRATION") != "1" || os.Getenv("EMQUANT_TEST_S3_ENDPOINT") == "" {
		t.Skip("live SDK and S3 integration not requested")
	}
	c, err := Open(os.Getenv("EMQUANT_LIBRARY"), os.Getenv("EMQUANT_USER"), os.Getenv("EMQUANT_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	store, err := NewS3RawStore(testS3Client(os.Getenv("EMQUANT_TEST_S3_ENDPOINT"), os.Getenv("EMQUANT_TEST_S3_ACCESS"), os.Getenv("EMQUANT_TEST_S3_SECRET")), os.Getenv("EMQUANT_TEST_S3_BUCKET"), "integration-live")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Collector{Fetcher: c, Store: store}).CollectLastPrice(context.Background(), "300059.SZ", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body, err := store.Get(context.Background(), result.Source.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	cube, observed, err := DecodeArchive(body, result.Source.BodySHA256)
	if err != nil || !observed.Equal(result.Price.ObservedAt) {
		t.Fatalf("archive replay: %v", err)
	}
	price, err := (Quote{Data: cube, ObservedAt: observed}).LastPriceAt("300059.SZ", result.Cutoff, 7*24*time.Hour)
	if err != nil || price.Value != result.Price.Value {
		t.Fatal(err)
	}
}
