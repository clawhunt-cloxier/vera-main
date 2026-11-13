//go:build linux && cgo

package emquant

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
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
