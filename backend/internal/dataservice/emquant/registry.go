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
	return r.registerCube(ctx, dataset, ref, nil, nil)
}

// registerCube may additionally bind a current storage grant and the exact
// request to the raw row. Unlicensed registrations cannot be promoted by
// silently rewriting their metadata on a later retry.
func (r ArchiveRegistry) registerCube(ctx context.Context, dataset string, ref ArchiveRef, grant *dataservice.SourceUseRequest, expected *request) (uuid.UUID, error) {
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
	cube, received, err := DecodeArchive(body, ref.BodySHA256)
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
	if (grant == nil) != (expected == nil) {
		return uuid.Nil, errors.New("emquant: incomplete raw registration grant")
	}
	if grant != nil {
		encoded, err := json.Marshal(expected)
		requested := strings.Split(expected.Indicators, ",")
		requestedCodes := strings.Split(expected.Codes, ",")
		if err != nil || digest(encoded) != ref.RequestFingerprint || grant.ProviderID != r.ProviderID || grant.DatasetID != dataset || grant.Use != dataservice.SourceStorage || !strings.EqualFold(expected.Method, grant.Method) || len(cube.Indicators) != len(grant.Fields) || len(requested) != len(grant.Fields) || len(requestedCodes) != len(grant.Codes) {
			return uuid.Nil, errors.New("emquant: raw registration request scope mismatch")
		}
		fields := make(map[string]bool, len(grant.Fields))
		for _, name := range grant.Fields {
			fields[strings.ToUpper(strings.TrimSpace(name))] = true
		}
		for _, name := range requested {
			if !fields[strings.ToUpper(strings.TrimSpace(name))] {
				return uuid.Nil, errors.New("emquant: raw registration request field mismatch")
			}
		}
		for _, name := range cube.Indicators {
			key := strings.ToUpper(strings.TrimSpace(name))
			if !fields[key] {
				return uuid.Nil, errors.New("emquant: raw registration returned unpermitted indicator")
			}
			delete(fields, key)
		}
		if len(fields) != 0 {
			return uuid.Nil, errors.New("emquant: raw registration missing requested indicator")
		}
		codes := make(map[string]bool, len(grant.Codes))
		for _, code := range grant.Codes {
			codes[strings.ToUpper(strings.TrimSpace(code))] = true
		}
		for _, code := range requestedCodes {
			if !codes[strings.ToUpper(strings.TrimSpace(code))] {
				return uuid.Nil, errors.New("emquant: raw registration request instrument mismatch")
			}
		}
		for _, code := range cube.Codes {
			key := strings.ToUpper(strings.TrimSpace(code))
			if !codes[key] {
				return uuid.Nil, errors.New("emquant: raw registration returned unpermitted instrument")
			}
			delete(codes, key)
		}
	}
	var id uuid.UUID
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		var licenseID *uuid.UUID
		var method *string
		var fields []string
		var codes []string
		if grant != nil {
			if err := dataservice.RequireSourceUse(ctx, tx, *grant); err != nil {
				return err
			}
			licenseID = &grant.LicenseID
			method = &grant.Method
			fields = grant.Fields
			codes = grant.Codes
		} else {
			var provider string
			if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 AND kind='vendor' AND enabled AND NOT synthetic FOR SHARE`, r.ProviderID).Scan(&provider); err != nil {
				return fmt.Errorf("emquant: raw registration provider unavailable: %w", err)
			}
		}
		// PostgreSQL stores microseconds. Hash and archive retain the original
		// nanosecond timestamp; normalize only the indexed database metadata.
		err := tx.QueryRow(ctx, `INSERT INTO raw_objects
			(provider_id,dataset_id,object_key,content_hash,size_bytes,stored_path,fetched_at,archived_at,request_fingerprint,archive_format,registered_at,source_license_id,vendor_method,vendor_fields,vendor_codes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,clock_timestamp(),$11,$12,$13,$14)
			ON CONFLICT (provider_id,dataset_id,object_key,content_hash) DO UPDATE SET object_key=raw_objects.object_key
			WHERE raw_objects.size_bytes=excluded.size_bytes AND raw_objects.stored_path=excluded.stored_path
			  AND raw_objects.fetched_at=excluded.fetched_at AND raw_objects.archived_at=excluded.archived_at
			  AND raw_objects.request_fingerprint=excluded.request_fingerprint AND raw_objects.archive_format=excluded.archive_format
			  AND raw_objects.source_license_id IS NOT DISTINCT FROM excluded.source_license_id
			  AND raw_objects.vendor_method IS NOT DISTINCT FROM excluded.vendor_method AND raw_objects.vendor_fields IS NOT DISTINCT FROM excluded.vendor_fields
			  AND raw_objects.vendor_codes IS NOT DISTINCT FROM excluded.vendor_codes
			RETURNING id`, r.ProviderID, dataset, key, ref.BodySHA256, int64(len(body)), ref.BodyRef,
			ref.ReceivedAt.UTC().Truncate(time.Microsecond), ref.ArchivedAt.UTC().Truncate(time.Microsecond), ref.RequestFingerprint, header.Format, licenseID, method, fields, codes).Scan(&id)
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
