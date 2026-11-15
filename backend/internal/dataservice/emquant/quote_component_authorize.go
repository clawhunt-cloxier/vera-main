package emquant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// ReauthorizeQuoteComponent checks an exact stored component against its
// immutable source versions and current rights. It returns no quote data and
// does not prove that the component belongs to a fully bound task snapshot;
// the eventual report path must first verify that binding. The caller must
// keep this fixed transaction open through the protected operation, and repeat
// the check separately for display and each model dispatch.
func (r *Runtime) ReauthorizeQuoteComponent(ctx context.Context, tx pgx.Tx, workspace, task uuid.UUID, epoch int64, hash string, use dataservice.SourceUse) error {
	if r == nil || r.store == nil || tx == nil || workspace == uuid.Nil || task == uuid.Nil || epoch < 1 || len(hash) != 64 || (use != dataservice.SourceDisplay && use != dataservice.SourceModel) {
		return errors.New("emquant: invalid quote component authorization request")
	}
	if _, err := BeginQuoteBoundary(ctx, tx); err != nil {
		return err
	}
	var body []byte
	var cutoff time.Time
	var planHash, requirement string
	var revision int64
	var entities []uuid.UUID
	err := tx.QueryRow(ctx, `SELECT c.manifest,c.cutoff,c.plan_hash,c.task_revision,i.requirement,i.entity_ids
		FROM quote_task_components c JOIN quote_task_intents i USING(workspace_id,task_id,epoch)
		JOIN research_tasks t ON t.id=c.task_id AND t.workspace_id=c.workspace_id AND t.epoch=c.epoch
		JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id AND p.plan_hash=c.plan_hash AND p.consumed_task_id=t.id
		WHERE c.workspace_id=$1 AND c.task_id=$2 AND c.epoch=$3 AND c.content_hash=$4 AND i.plan_hash=c.plan_hash AND i.protocol='0.4'
		AND t.snapshot_mode='collect_latest' AND p.snapshot_mode=t.snapshot_mode
		AND t.status NOT IN ('CANCEL_REQUESTED','CANCELLED') FOR SHARE OF c,i`, workspace, task, epoch, hash).
		Scan(&body, &cutoff, &planHash, &revision, &requirement, &entities)
	if err != nil {
		return fmt.Errorf("emquant: quote component unavailable: %w", err)
	}
	if len(body) == 0 || len(body) > 1<<20 || quoteComponentDigest(body) != hash {
		return errors.New("emquant: quote component hash mismatch")
	}
	var doc quoteComponentDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return errors.New("emquant: invalid quote component manifest")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("emquant: trailing quote component content")
	}
	slices.SortFunc(entities, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if doc.Version != 1 || doc.WorkspaceID != workspace || doc.TaskID != task || doc.Epoch != epoch || doc.TaskRevision != revision || doc.PlanHash != planHash || !doc.View.Cutoff.Equal(cutoff) || doc.View.Requirement != QuoteRequirement(requirement) || doc.View.Use != dataservice.SourceModel || !slices.Equal(doc.EntityIDs, entities) || len(entities) == 0 || len(entities) > 64 || entities[0] == uuid.Nil || len(slices.Compact(slices.Clone(entities))) != len(entities) {
		return errors.New("emquant: quote component scope mismatch")
	}
	want := make(map[uuid.UUID]bool, len(entities))
	for _, id := range entities {
		want[id] = true
	}
	for _, id := range doc.View.Unavailable {
		if !want[id] {
			return errors.New("emquant: invalid unavailable quote subject")
		}
		delete(want, id)
	}
	for _, original := range doc.View.Quotes {
		if !want[original.EntityID] {
			return errors.New("emquant: duplicate or unplanned quote subject")
		}
		delete(want, original.EntityID)
		if doc.View.Requirement == QuoteRealtimeTrade && (original.LatencyMode != "real_time" || original.MarketState != "continuous") {
			return errors.New("emquant: frozen quote does not meet realtime requirement")
		}
		if err := r.reauthorizeQuote(ctx, tx, original, cutoff, use); err != nil {
			return err
		}
	}
	if len(want) != 0 {
		return errors.New("emquant: incomplete quote component coverage")
	}
	// Do not hold the task row during object reads; lock and recheck it last so
	// a cancellation can proceed while S3 is slow and stale epochs fail closed.
	var currentEpoch int64
	var currentPlan, status string
	if err := tx.QueryRow(ctx, `SELECT t.epoch,p.plan_hash,t.status FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
		WHERE t.id=$1 AND t.workspace_id=$2 AND p.consumed_task_id=t.id FOR SHARE OF t,p`, task, workspace).
		Scan(&currentEpoch, &currentPlan, &status); err != nil {
		return fmt.Errorf("emquant: task changed during quote authorization: %w", err)
	}
	if currentEpoch != epoch || currentPlan != planHash || status == "CANCEL_REQUESTED" || status == "CANCELLED" {
		return errors.New("emquant: task changed during quote authorization")
	}
	// Waiting for the task lock can outlast a previously checked grant. The
	// source and policy rows remain locked; recheck their time windows before
	// returning without performing any further remote reads under this lock.
	for _, quote := range doc.View.Quotes {
		grant := dataservice.SourceUseRequest{LicenseID: quote.LicenseID, ProviderID: quote.ProviderID, Product: quote.Product,
			DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: []string{"TIME", "NOW"}, Codes: []string{quote.Code}, Use: use}
		if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
			return err
		}
		var current uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
			AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, quote.PolicyID).Scan(&current); err != nil {
			return fmt.Errorf("emquant: quote policy expired before authorization: %w", err)
		}
	}
	return nil
}

func (r *Runtime) reauthorizeQuote(ctx context.Context, tx pgx.Tx, original PublishedQuote, cutoff time.Time, use dataservice.SourceUse) error {
	var q PublishedQuote
	var fingerprint string
	var fetched, archived time.Time
	var fields, codes []string
	var policy QuoteMappingPolicy
	var maxAgeMS int64
	var payload, locators string
	err := tx.QueryRow(ctx, `SELECT s.id,s.entity_id,s.raw_object_id,s.policy_id,o.source_license_id,o.provider_id,l.product_code,s.security_code,
		s.event_at,s.received_at,pub.available_at,q.latency_mode,s.payload->>'market_status',o.content_hash,o.stored_path,o.request_fingerprint,
		o.fetched_at,o.archived_at,o.vendor_fields,o.vendor_codes,q.time_convention,q.time_evidence_ref,q.price_unit,q.price_evidence_ref,coalesce(q.latency_evidence_ref,''),q.max_age_ms,s.payload::text,s.field_locators::text
		FROM staged_quotes s JOIN quote_publications pub ON pub.staged_quote_id=s.id
		JOIN raw_objects o ON o.id=s.raw_object_id AND o.source_license_id IS NOT NULL
		JOIN source_licenses l ON l.id=o.source_license_id AND l.provider_id=o.provider_id AND l.dataset_id=o.dataset_id
		JOIN quote_mapping_policies q ON q.id=s.policy_id AND q.source_license_id=l.id AND q.dataset_id=o.dataset_id
		JOIN entities e ON e.id=s.entity_id AND e.kind='stock' AND NOT e.synthetic AND e.code=s.security_code
		WHERE s.id=$1 AND s.entity_id=$2 AND s.raw_object_id=$3 AND s.policy_id=$4 AND o.source_license_id=$5
		AND o.dataset_id='mkt.intraday_quote' AND o.vendor_method='CSQSNAPSHOT' AND o.vendor_fields=ARRAY['TIME','NOW']::text[] AND o.vendor_codes=ARRAY[s.security_code]::text[]
		AND pub.available_at<=$6 AND s.event_at<=$6 AND s.received_at<=$6 AND o.fetched_at<=pub.available_at AND o.archived_at<=pub.available_at AND s.staged_at<=pub.available_at
		AND q.revoked_at IS NULL AND q.verified_at<=$6 AND q.valid_from<=$6 AND (q.valid_until IS NULL OR q.valid_until>$6)
		FOR SHARE OF s,o,q,pub,e`, original.StageID, original.EntityID, original.RawObjectID, original.PolicyID, original.LicenseID, cutoff).
		Scan(&q.StageID, &q.EntityID, &q.RawObjectID, &q.PolicyID, &q.LicenseID, &q.ProviderID, &q.Product, &q.Code,
			&q.EventAt, &q.ReceivedAt, &q.AvailableAt, &q.LatencyMode, &q.MarketState, &q.RawSHA256, &q.RawRef, &fingerprint,
			&fetched, &archived, &fields, &codes, &policy.TimeConvention, &policy.TimeEvidence, &policy.PriceUnit, &policy.PriceEvidence, &policy.LatencyEvidence, &maxAgeMS, &payload, &locators)
	if err != nil {
		return fmt.Errorf("emquant: frozen quote source unavailable: %w", err)
	}
	grant := dataservice.SourceUseRequest{LicenseID: q.LicenseID, ProviderID: q.ProviderID, Product: q.Product, DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: fields, Codes: codes, Use: use}
	if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
		return err
	}
	// The original cutoff proves that the mapping was valid when frozen. A
	// historical cutoff does not make an expired mapping valid for use now.
	var current uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
		AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, q.PolicyID).Scan(&current); err != nil {
		return fmt.Errorf("emquant: quote policy no longer current: %w", err)
	}
	if err := r.verifyPublishedQuote(ctx, &q, fingerprint, fetched, archived, policy, maxAgeMS, payload, locators, cutoff); err != nil {
		return err
	}
	if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
		AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, q.PolicyID).Scan(&current); err != nil {
		return fmt.Errorf("emquant: quote policy expired during verification: %w", err)
	}
	if q.StageID != original.StageID || q.EntityID != original.EntityID || q.RawObjectID != original.RawObjectID || q.PolicyID != original.PolicyID || q.LicenseID != original.LicenseID || q.ProviderID != original.ProviderID || q.Product != original.Product || q.Code != original.Code || q.Price != original.Price || !q.EventAt.Equal(original.EventAt) || !q.ReceivedAt.Equal(original.ReceivedAt) || !q.AvailableAt.Equal(original.AvailableAt) || q.LatencyMode != original.LatencyMode || q.MarketState != original.MarketState || q.RawSHA256 != original.RawSHA256 || q.RawRef != original.RawRef || !maps.Equal(q.Locators, original.Locators) {
		return errors.New("emquant: frozen quote differs from its immutable source")
	}
	return nil
}
