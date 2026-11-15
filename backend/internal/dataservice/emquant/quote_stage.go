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

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// StageQuote verifies an already registered source and a controlled mapping
// policy, then commits an immutable normalized candidate. It does not publish
// the candidate, mark it available, or expose it to a research snapshot.
// Policy and license evidence must be checked independently by the operator;
// their presence in the database is not proof that vendor terms are correct.
func (r *Runtime) StageQuote(ctx context.Context, catalog *dataservice.Catalog, source RegisteredArchive, policyID, entityID uuid.UUID, code string) (uuid.UUID, error) {
	if r == nil || r.database == nil || catalog == nil || source.ID == uuid.Nil || source.LicenseID == uuid.Nil || policyID == uuid.Nil || entityID == uuid.Nil || source.DatasetID != "mkt.intraday_quote" || code == "" || strings.TrimSpace(code) != code || strings.ToUpper(code) != code {
		return uuid.Nil, errors.New("emquant: invalid quote staging request")
	}
	body, err := r.ReadArchive(ctx, source.Source)
	if err != nil {
		return uuid.Nil, err
	}
	var stagedID uuid.UUID
	err = pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
		var locked uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 FOR SHARE`, policyID).Scan(&locked); err != nil {
			return fmt.Errorf("emquant: quote policy unavailable: %w", err)
		}
		var policy QuoteMappingPolicy
		var maxAgeMS int64
		if err := tx.QueryRow(ctx, `SELECT time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,coalesce(latency_evidence_ref,''),max_age_ms
			FROM quote_mapping_policies WHERE id=$1 AND source_license_id=$2 AND dataset_id=$3
			AND revoked_at IS NULL AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp()
			AND (valid_until IS NULL OR valid_until>clock_timestamp())`, policyID, source.LicenseID, source.DatasetID).
			Scan(&policy.TimeConvention, &policy.TimeEvidence, &policy.PriceUnit, &policy.PriceEvidence, &policy.LatencyMode, &policy.LatencyEvidence, &maxAgeMS); err != nil {
			return fmt.Errorf("emquant: quote policy is not current: %w", err)
		}
		policy.MaxAge = time.Duration(maxAgeMS) * time.Millisecond
		policy.LicenseRef = source.LicenseID.String()
		var provider string
		var fields, codes []string
		ref := source.Source
		if err := tx.QueryRow(ctx, `SELECT provider_id,vendor_fields,vendor_codes FROM raw_objects
			WHERE id=$1 AND dataset_id=$2 AND source_license_id=$3 AND vendor_method='CSQSNAPSHOT'
			AND content_hash=$4 AND stored_path=$5 AND request_fingerprint=$6 AND archive_format='emquant-sdk-decoded-v2'
			AND fetched_at=$7 AND archived_at=$8 FOR SHARE`, source.ID, source.DatasetID, source.LicenseID, ref.BodySHA256, ref.BodyRef, ref.RequestFingerprint,
			ref.ReceivedAt.UTC().Truncate(time.Microsecond), ref.ArchivedAt.UTC().Truncate(time.Microsecond)).Scan(&provider, &fields, &codes); err != nil {
			return fmt.Errorf("emquant: registered quote provenance mismatch: %w", err)
		}
		if len(fields) != 2 || fields[0] != "TIME" || fields[1] != "NOW" || len(codes) != 1 || codes[0] != code {
			return errors.New("emquant: registered quote request scope mismatch")
		}
		var found uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM entities WHERE id=$1 AND code=$2 AND kind='stock' AND NOT synthetic FOR SHARE`, entityID, code).Scan(&found); err != nil {
			return fmt.Errorf("emquant: quote entity unavailable: %w", err)
		}
		grant := dataservice.SourceUseRequest{LicenseID: source.LicenseID, ProviderID: provider, Product: source.Product, DatasetID: source.DatasetID, Method: "CSQSNAPSHOT", Fields: fields, Codes: codes, Use: dataservice.SourceStorage}
		if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp() FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
			AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, policyID).Scan(&now); err != nil {
			return fmt.Errorf("emquant: quote policy expired while waiting: %w", err)
		}
		mapped, err := MapArchivedQuote(body, ref, code, now, policy)
		if err != nil {
			return err
		}
		payload := map[string]any{"event_at": mapped.Price.EventAt.Format(time.RFC3339Nano), "received_at": mapped.Price.ObservedAt.Format(time.RFC3339Nano), "last_price": mapped.Price.Value, "market_status": "unknown", "latency_mode": mapped.DeclaredLatencyMode}
		if err := catalog.Validate(dataservice.Record{DatasetID: source.DatasetID, Payload: payload}); err != nil {
			return fmt.Errorf("emquant: mapped quote does not match catalog: %w", err)
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		locatorsJSON, err := json.Marshal(mapped.Locators)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO staged_quotes(raw_object_id,policy_id,entity_id,security_code,event_at,received_at,payload,field_locators)
			VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb) ON CONFLICT (raw_object_id,policy_id,entity_id) DO NOTHING RETURNING id`,
			source.ID, policyID, entityID, code, mapped.Price.EventAt, mapped.Price.ObservedAt.UTC().Truncate(time.Microsecond), string(payloadJSON), string(locatorsJSON)).Scan(&stagedID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `SELECT id FROM staged_quotes WHERE raw_object_id=$1 AND policy_id=$2 AND entity_id=$3 AND security_code=$4
				AND event_at=$5 AND received_at=$6 AND payload=$7::jsonb AND field_locators=$8::jsonb`, source.ID, policyID, entityID, code,
				mapped.Price.EventAt, mapped.Price.ObservedAt.UTC().Truncate(time.Microsecond), string(payloadJSON), string(locatorsJSON)).Scan(&stagedID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("emquant: staged quote conflicts with existing version")
			}
		}
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	return stagedID, nil
}
