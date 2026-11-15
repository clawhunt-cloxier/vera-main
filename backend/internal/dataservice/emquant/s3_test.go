package emquant

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func testS3Client(endpoint, access, secret string) *s3.Client {
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: access, SecretAccessKey: secret, Source: "test"}, nil
	})
	return s3.New(s3.Options{Region: "garage", Credentials: provider, BaseEndpoint: aws.String(endpoint), UsePathStyle: true})
}

func TestS3RawStoreConditionalWriteAndIntegrity(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing conditional write")
				http.Error(w, "bad condition", 400)
				return
			}
			if _, exists := objects[r.URL.Path]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			objects[r.URL.Path] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, exists := objects[r.URL.Path]
			if !exists {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	store, err := NewS3RawStore(testS3Client(server.URL, "test", "test"), "test-bucket", "vendor")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"sample":"synthetic"}`)
	hash := digest(body)
	ref, err := store.Put(context.Background(), hash, body)
	if err != nil || !strings.HasPrefix(ref, "s3://test-bucket/vendor/sha256/") {
		t.Fatalf("put: %s %v", ref, err)
	}
	if _, err = store.Put(context.Background(), hash, body); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	got, err := store.Get(context.Background(), hash)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("get: %v", err)
	}
	mu.Lock()
	for key := range objects {
		objects[key] = []byte("corrupt")
	}
	mu.Unlock()
	if _, err = store.Get(context.Background(), hash); err == nil {
		t.Fatal("corruption accepted")
	}
	if _, err = store.Put(context.Background(), hash, body); err == nil {
		t.Fatal("corrupted existing object accepted")
	}
	if _, err = store.Put(context.Background(), hash, []byte("different")); err == nil {
		t.Fatal("incorrect hash accepted")
	}
	if _, err = store.Get(context.Background(), "../../bad"); err == nil {
		t.Fatal("invalid key accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.Put(ctx, hash, body); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

// Opt-in integration test runs against an independently started S3 service.
func TestS3Integration(t *testing.T) {
	endpoint := os.Getenv("EMQUANT_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3 integration not requested")
	}
	store, err := NewS3RawStore(testS3Client(endpoint, os.Getenv("EMQUANT_TEST_S3_ACCESS"), os.Getenv("EMQUANT_TEST_S3_SECRET")), os.Getenv("EMQUANT_TEST_S3_BUCKET"), "integration")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic S3 integration")
	hash := digest(body)
	if _, err = store.Put(context.Background(), hash, body); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Put(context.Background(), hash, body); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), hash)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("round-trip: %v", err)
	}
}
