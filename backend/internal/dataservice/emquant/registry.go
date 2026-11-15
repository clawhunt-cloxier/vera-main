package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// ArchiveRegistry records a verified decoded archive. Registration alone does
// not prove vendor origin, authorize any use, publish records, or establish a
// system availability time. Only a trusted collection service should call it.
type ArchiveRegistry struct {
	Pool       *pgxpool.Pool
	Store      *S3RawStore
	Catalog    *dataservice.Catalog
	ProviderID string
}

// RegisterCube rereads an immutable S3 object and binds its fingerprint, hash,
// size and observation metadata to an enabled non-synthetic vendor. It is
// idempotent only for identical metadata. The caller's dataset label classifies
// the raw response; it does not assert canonical coverage or permissions.
func (r ArchiveRegistry) RegisterCube(ctx context.Context, dataset string, ref ArchiveRef) (uuid.UUID, error) {
	if r.Pool == nil || r.Store == nil || r.Catalog == nil || r.ProviderID == "" || len(r.ProviderID) > 128 || strings.TrimSpace(r.ProviderID) != r.ProviderID || dataset == "" || len(dataset) > 128 || strings.TrimSpace(dataset) != dataset || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return uuid.Nil, errors.New("emquant: invalid raw registration")
	}
	if _, ok := r.Catalog.Dataset(dataset); !ok {
		return uuid.Nil, errors.New("emquant: raw registration dataset is not in catalog")
	}
	key, err := r.Store.key(ref.BodySHA256)
	if err != nil || ref.BodyRef != "s3://"+r.Store.bucket+"/"+key {
		return uuid.Nil, errors.New("emquant: raw registration location mismatch")
	}
	body, err := r.Store.Get(ctx, ref.BodySHA256)
	if err != nil {
		return uuid.Nil, err
	}
	_, received, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return uuid.Nil, err
	}
	var header struct {
		Format             string `json:"format"`
		RequestFingerprint string `json:"request_fingerprint"`
	}
	if err := json.Unmarshal(body, &header); err != nil || header.Format != "emquant-sdk-decoded-v2" || header.RequestFingerprint != ref.RequestFingerprint || !received.Equal(ref.ReceivedAt) {
		return uuid.Nil, errors.New("emquant: raw registration provenance mismatch")
	}
	var id uuid.UUID
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		var provider string
		if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 AND kind='vendor' AND enabled AND NOT synthetic FOR SHARE`, r.ProviderID).Scan(&provider); err != nil {
			return fmt.Errorf("emquant: raw registration provider unavailable: %w", err)
		}
		// PostgreSQL stores microseconds. Hash and archive retain the original
		// nanosecond timestamp; normalize only the indexed database metadata.
		err := tx.QueryRow(ctx, `INSERT INTO raw_objects
			(provider_id,dataset_id,object_key,content_hash,size_bytes,stored_path,fetched_at,archived_at,request_fingerprint,archive_format,registered_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,clock_timestamp())
			ON CONFLICT (provider_id,dataset_id,object_key,content_hash) DO UPDATE SET object_key=raw_objects.object_key
			WHERE raw_objects.size_bytes=excluded.size_bytes AND raw_objects.stored_path=excluded.stored_path
			  AND raw_objects.fetched_at=excluded.fetched_at AND raw_objects.archived_at=excluded.archived_at
			  AND raw_objects.request_fingerprint=excluded.request_fingerprint AND raw_objects.archive_format=excluded.archive_format
			RETURNING id`, provider, dataset, key, ref.BodySHA256, int64(len(body)), ref.BodyRef,
			ref.ReceivedAt.UTC().Truncate(time.Microsecond), ref.ArchivedAt.UTC().Truncate(time.Microsecond), ref.RequestFingerprint, header.Format).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("emquant: conflicting raw registration metadata")
		}
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
