package emquant

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestPreviouslyArchivedQuote is read-only and requires an explicitly supplied
// S3 test bucket containing an already captured SDK observation. It does not
// contact the vendor or print values. A hash check proves archive integrity,
// not the vendor's identity, field meaning or real-time entitlement.
func TestPreviouslyArchivedQuote(t *testing.T) {
	if os.Getenv("EMQUANT_ARCHIVE_REPLAY") != "1" {
		t.Skip("archived vendor replay not requested")
	}
	endpoint, bucket := os.Getenv("EMQUANT_TEST_S3_ENDPOINT"), os.Getenv("EMQUANT_TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Fatal("explicit S3 endpoint and bucket are required")
	}
	client := testS3Client(endpoint, os.Getenv("EMQUANT_TEST_S3_ACCESS"), os.Getenv("EMQUANT_TEST_S3_SECRET"))
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			t.Fatalf("archive listing: %v", err)
		}
		for _, item := range page.Contents {
			key := aws.ToString(item.Key)
			prefix, _, ok := strings.Cut(key, "/sha256/")
			if !ok || !strings.HasSuffix(key, ".json") {
				continue
			}
			hash := strings.TrimSuffix(path.Base(key), ".json")
			store, err := NewS3RawStore(client, bucket, prefix)
			if err != nil {
				t.Fatal(err)
			}
			body, err := store.Get(ctx, hash)
			if err != nil {
				t.Fatalf("archive retrieval: %v", err)
			}
			cube, observed, err := DecodeArchive(body, hash)
			if err != nil {
				continue // Other test objects need not be SDK archives.
			}
			quote := Quote{Data: cube, ObservedAt: observed}
			if _, err := quote.LastPriceAt("300059.SZ", observed, 7*24*time.Hour); err != nil {
				continue // Other SDK responses need not be this quote.
			}
			if time.Since(observed) > time.Minute {
				if _, err := quote.LastPriceAt("300059.SZ", time.Now().UTC(), time.Minute); !errors.Is(err, ErrStaleQuote) {
					t.Fatal("historical quote was accepted as current")
				}
			}
			return
		}
	}
	t.Fatal("no compatible previously archived quote found")
}
