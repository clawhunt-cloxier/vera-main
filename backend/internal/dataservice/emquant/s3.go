package emquant

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const maxArchiveSize = 64 << 20

// S3RawStore stores content-addressed SDK results in an S3-compatible bucket.
// Credentials, endpoint and transport are supplied by the application's
// configured AWS SDK client; no provider secrets are held here.
type S3RawStore struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3RawStore(client *s3.Client, bucket, prefix string) (*S3RawStore, error) {
	if client == nil || bucket == "" || strings.TrimSpace(bucket) != bucket || strings.ContainsAny(bucket, "/\\\x00") {
		return nil, errors.New("emquant: invalid S3 configuration")
	}
	prefix = strings.Trim(prefix, "/")
	if strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\\\x00") {
		return nil, errors.New("emquant: invalid S3 prefix")
	}
	return &S3RawStore{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *S3RawStore) key(hash string) (string, error) {
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 || hash != strings.ToLower(hash) {
		return "", errors.New("emquant: invalid content hash")
	}
	key := "sha256/" + hash[:2] + "/" + hash + ".json"
	if s.prefix != "" {
		key = s.prefix + "/" + key
	}
	return key, nil
}

// Put never overwrites an existing object. If another writer already stored
// the key, it verifies the existing body before treating the write as success.
func (s *S3RawStore) Put(ctx context.Context, hash string, body []byte) (string, error) {
	key, err := s.key(hash)
	if err != nil {
		return "", err
	}
	if len(body) > maxArchiveSize || digest(body) != hash {
		return "", errors.New("emquant: invalid archive body or hash")
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentType: aws.String("application/json"), IfNoneMatch: aws.String("*")})
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "PreconditionFailed" {
			return "", fmt.Errorf("emquant: S3 put: %w", err)
		}
		if _, readErr := s.Get(ctx, hash); readErr != nil {
			return "", fmt.Errorf("emquant: existing S3 object failed verification: %w", readErr)
		}
	}
	return "s3://" + s.bucket + "/" + key, nil
}

// Get bounds the response and verifies its content hash before returning it.
func (s *S3RawStore) Get(ctx context.Context, hash string) ([]byte, error) {
	key, err := s.key(hash)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("emquant: S3 get: %w", err)
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("emquant: S3 read: %w", err)
	}
	if len(body) > maxArchiveSize || digest(body) != hash {
		return nil, errors.New("emquant: corrupt or oversized S3 object")
	}
	return body, nil
}
