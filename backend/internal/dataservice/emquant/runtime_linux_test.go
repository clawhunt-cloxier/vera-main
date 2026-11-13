//go:build linux && cgo

package emquant

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestRuntimeRequiresGateAndArchivesSDKResponse(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	lib := filepath.Join(t.TempDir(), "fake.so")
	if output, err := exec.Command("cc", "-shared", "-fPIC", "-o", lib, "testdata/fake.c").CombinedOutput(); err != nil {
		t.Fatalf("compile fake library: %v: %s", err, output)
	}
	archived := make(chan []byte, 1)
	var mu sync.Mutex
	var object []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			defer mu.Unlock()
			_, _ = w.Write(object)
			return
		}
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		object = append([]byte(nil), body...)
		mu.Unlock()
		archived <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := RuntimeConfig{Library: lib, Username: "user", Password: "pass", Database: pool, ObjectClient: testS3Client(server.URL, "test", "test"), Bucket: "test-bucket", AccountScope: "synthetic-account", MinInterval: 90 * time.Millisecond, RateLimitCooldown: 200 * time.Millisecond}
	if strings.Contains(fmt.Sprintf("%v %#v", cfg, cfg), "pass") {
		t.Fatal("credentials appeared in config formatting")
	}
	if runtime, err := OpenRuntime(ctx, cfg); err == nil {
		_ = runtime.Close()
		t.Fatal("runtime opened without persistent rate state")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE emquant_rate_state (scope text PRIMARY KEY, in_flight boolean NOT NULL, next_allowed timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	quote, ref, err := runtime.CollectQuote(ctx, "A,B", "X,Y", "")
	if err != nil || len(quote.Data.Values) != 8 || ref.BodyRef == "" || ref.BodySHA256 == "" {
		t.Fatalf("runtime collection: %v", err)
	}
	body := <-archived
	if replay, _, err := DecodeArchive(body, ref.BodySHA256); err != nil || len(replay.Values) != 8 {
		t.Fatalf("runtime archive replay: %v", err)
	}
	if replay, err := runtime.ReadArchive(ctx, ref); err != nil || !bytes.Equal(replay, body) {
		t.Fatalf("runtime object read: %v", err)
	}
	bad := ref
	bad.BodyRef = "s3://test-bucket/wrong-object"
	if _, err := runtime.ReadArchive(ctx, bad); err == nil {
		t.Fatal("wrong object reference accepted")
	}
	bad = ref
	bad.RequestFingerprint = strings.Repeat("0", 64)
	if _, err := runtime.ReadArchive(ctx, bad); err == nil {
		t.Fatal("wrong request fingerprint accepted")
	}
	mu.Lock()
	object = []byte("corrupt")
	mu.Unlock()
	if _, err := runtime.ReadArchive(ctx, ref); err == nil {
		t.Fatal("corrupt stored object accepted")
	}
	days, calendarRef, err := runtime.CollectOpenDates(ctx, "CNSESH", "2026-11-09", "2026-11-10")
	if err != nil || len(days) != 2 {
		t.Fatalf("calendar collection: %v", err)
	}
	<-archived
	records, err := runtime.MapCalendarFromArchive(ctx, calendarRef, "CNSESH", "2026-11-09", "2026-11-10")
	if err != nil || len(records) != 2 || records[0].RawSHA256 != calendarRef.BodySHA256 || records[0].Record.RecordKey != "2026-11-09" {
		t.Fatalf("calendar archive mapping: %v", err)
	}
	if _, err := runtime.MapCalendarFromArchive(ctx, calendarRef, "CNSESZ", "2026-11-09", "2026-11-10"); err == nil {
		t.Fatal("calendar archive accepted with the wrong market")
	}
	statementFetcher := &testFetcher{cube: Cube{Codes: []string{"A.SH"}, Indicators: []string{"IncomeStatement_9", "IncomeStatement_61"}, Dates: []string{"2026-11-13"}, Values: []Cell{{Type: 11, Value: 1000.0}, {Type: 11, Value: -20.0}}}}
	_, statementRef, err := (Collector{Fetcher: statementFetcher, Store: runtime.store}).CollectCSS(ctx, "A.SH", statementIndicators, "ReportDate=20251231,Type=1")
	if err != nil {
		t.Fatalf("statement archive: %v", err)
	}
	fields, err := runtime.MapStatementFromArchive(ctx, statementRef, "A.SH", "2025-12-31", 1)
	if err != nil || fields.RevenueCNY == nil || *fields.RevenueCNY != "1000" || fields.NetProfitCNY == nil || *fields.NetProfitCNY != "-20" {
		t.Fatalf("statement archive mapping: %v", err)
	}
	if _, err := runtime.MapStatementFromArchive(ctx, statementRef, "A.SH", "2025-09-30", 1); err == nil {
		t.Fatal("statement archive accepted with the wrong report period")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.CollectQuote(ctx, "A,B", "X,Y", ""); err == nil {
		t.Fatal("closed runtime accepted a vendor call")
	}
}

// TestLiveRuntime consumes vendor quota and writes to the specified S3 bucket.
// It requires an isolated account not in use by other applications, since the
// test database is not their shared gate. A passing query does not establish
// price units, realtime latency or product license.
func TestLiveRuntime(t *testing.T) {
	if os.Getenv("EMQUANT_INTEGRATION") != "1" || os.Getenv("EMQUANT_LIVE_RUNTIME") != "1" {
		t.Skip("live vendor runtime test not requested")
	}
	if os.Getenv("EMQUANT_TEST_S3_ENDPOINT") == "" || os.Getenv("EMQUANT_TEST_S3_BUCKET") == "" {
		t.Fatal("an explicit test S3 endpoint and bucket are required")
	}
	interval, err := time.ParseDuration(os.Getenv("EMQUANT_TEST_MIN_INTERVAL"))
	if err != nil {
		t.Fatal("EMQUANT_TEST_MIN_INTERVAL must be explicitly configured")
	}
	cooldown, err := time.ParseDuration(os.Getenv("EMQUANT_TEST_RATE_COOLDOWN"))
	if err != nil {
		t.Fatal("EMQUANT_TEST_RATE_COOLDOWN must be explicitly configured")
	}
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE emquant_rate_state (scope text PRIMARY KEY, in_flight boolean NOT NULL, next_allowed timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	s3Client := testS3Client(os.Getenv("EMQUANT_TEST_S3_ENDPOINT"), os.Getenv("EMQUANT_TEST_S3_ACCESS"), os.Getenv("EMQUANT_TEST_S3_SECRET"))
	cfg := RuntimeConfig{Library: os.Getenv("EMQUANT_LIBRARY"), Username: os.Getenv("EMQUANT_USER"), Password: os.Getenv("EMQUANT_PASSWORD"), Database: pool, ObjectClient: s3Client, Bucket: os.Getenv("EMQUANT_TEST_S3_BUCKET"), Prefix: "runtime-integration", AccountScope: "isolated-integration-account", MinInterval: interval, RateLimitCooldown: cooldown}
	runtime, err := OpenRuntime(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	_, ref, err := runtime.CollectQuote(ctx, "300059.SZ", "TIME,NOW", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewS3RawStore(s3Client, cfg.Bucket, cfg.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	body, err := store.Get(ctx, ref.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, observed, err := DecodeArchive(body, ref.BodySHA256); err != nil || !observed.Equal(ref.ReceivedAt) {
		t.Fatalf("live runtime archive replay: %v", err)
	}
}
