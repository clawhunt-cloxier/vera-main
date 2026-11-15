package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// PublishStagedQuote commits a separate publication marker for an already
// committed stage. It does not alter legacy records or create a task snapshot.
// A future reader must observe the marker in its fixed MVCC view and recheck
// the current display/model license before using or exposing the data.
func (r *Runtime) PublishStagedQuote(ctx context.Context, stageID uuid.UUID) (time.Time, error) {
	if r == nil || r.database == nil || r.store == nil || stageID == uuid.Nil {
		return time.Time{}, errors.New("emquant: invalid quote publication request")
	}
	var hash, path, fingerprint string
	var fetched time.Time
	if err := r.database.QueryRow(ctx, `SELECT o.content_hash,o.stored_path,o.request_fingerprint,o.fetched_at
		FROM staged_quotes s JOIN raw_objects o ON o.id=s.raw_object_id WHERE s.id=$1`, stageID).Scan(&hash, &path, &fingerprint, &fetched); err != nil {
		return time.Time{}, fmt.Errorf("emquant: staged quote source unavailable: %w", err)
	}
	key, err := r.store.key(hash)
	if err != nil || path != "s3://"+r.store.bucket+"/"+key {
		return time.Time{}, errors.New("emquant: staged quote source location mismatch")
	}
	body, err := r.store.Get(ctx, hash)
	if err != nil {
		return time.Time{}, err
	}
	_, observed, err := DecodeArchive(body, hash)
	if err != nil {
		return time.Time{}, err
	}
	var header struct {
		Format             string `json:"format"`
		RequestFingerprint string `json:"request_fingerprint"`
	}
	if err := json.Unmarshal(body, &header); err != nil || header.Format != "emquant-sdk-decoded-v2" || header.RequestFingerprint != fingerprint || !observed.UTC().Truncate(time.Microsecond).Equal(fetched) {
		return time.Time{}, errors.New("emquant: staged quote source integrity mismatch")
	}
	var available time.Time
	err = pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
		var policyID, licenseID, entityID uuid.UUID
		var provider, product, dataset, code string
		var fields, codes []string
		var eventAt, receivedAt, stagedAt, fetchedAt, archivedAt time.Time
		var maxAgeMS int64
		err := tx.QueryRow(ctx, `SELECT s.policy_id,o.source_license_id,s.entity_id,o.provider_id,l.product_code,o.dataset_id,s.security_code,o.vendor_fields,o.vendor_codes,
			s.event_at,s.received_at,s.staged_at,o.fetched_at,o.archived_at,q.max_age_ms
			FROM staged_quotes s JOIN raw_objects o ON o.id=s.raw_object_id
			JOIN quote_mapping_policies q ON q.id=s.policy_id AND q.source_license_id=o.source_license_id AND q.dataset_id=o.dataset_id
			JOIN source_licenses l ON l.id=o.source_license_id
			WHERE s.id=$1 AND o.vendor_method='CSQSNAPSHOT' AND o.dataset_id='mkt.intraday_quote'
			AND o.content_hash=$2 AND o.stored_path=$3 AND o.request_fingerprint=$4 AND o.fetched_at=$5
			FOR SHARE OF s,o,q`, stageID, hash, path, fingerprint, fetched).Scan(&policyID, &licenseID, &entityID, &provider, &product, &dataset, &code, &fields, &codes,
			&eventAt, &receivedAt, &stagedAt, &fetchedAt, &archivedAt, &maxAgeMS)
		if err != nil {
			return fmt.Errorf("emquant: staged quote unavailable: %w", err)
		}
		if len(fields) != 2 || fields[0] != "TIME" || fields[1] != "NOW" || len(codes) != 1 || codes[0] != code {
			return errors.New("emquant: staged quote source scope mismatch")
		}
		var entity uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM entities WHERE id=$1 AND code=$2 AND kind='stock' AND NOT synthetic FOR SHARE`, entityID, code).Scan(&entity); err != nil {
			return fmt.Errorf("emquant: quote entity unavailable: %w", err)
		}
		grant := dataservice.SourceUseRequest{LicenseID: licenseID, ProviderID: provider, Product: product, DatasetID: dataset, Method: "CSQSNAPSHOT", Fields: fields, Codes: codes, Use: dataservice.SourceStorage}
		if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp() FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
			AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, policyID).Scan(&now); err != nil {
			return fmt.Errorf("emquant: quote policy is not current: %w", err)
		}
		// Recover a committed publication even if the quote has since become
		// stale. A later snapshot must independently enforce freshness; a lost
		// commit response must not create a new publication time.
		err = tx.QueryRow(ctx, `SELECT available_at FROM quote_publications WHERE staged_quote_id=$1`, stageID).Scan(&available)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now = now.UTC().Truncate(time.Microsecond)
		if now.Before(eventAt) || now.Before(receivedAt) || now.Before(stagedAt) || now.Before(fetchedAt) || now.Before(archivedAt) || now.Sub(eventAt) > time.Duration(maxAgeMS)*time.Millisecond {
			return ErrStaleQuote
		}
		err = tx.QueryRow(ctx, `INSERT INTO quote_publications(staged_quote_id,available_at) VALUES($1,$2)
			ON CONFLICT (staged_quote_id) DO NOTHING RETURNING available_at`, stageID, now).Scan(&available)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `SELECT available_at FROM quote_publications WHERE staged_quote_id=$1`, stageID).Scan(&available)
		}
		return err
	})
	if err != nil {
		return time.Time{}, err
	}
	return available, nil
}
