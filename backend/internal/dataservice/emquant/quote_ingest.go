package emquant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// QuoteIngestResult identifies a published candidate, not a task snapshot.
// On a publication error StageID can be used with PublishStagedQuote to recover
// a committed or interrupted publication without another vendor request.
type QuoteIngestResult struct {
	StageID     uuid.UUID
	AvailableAt time.Time
}

// CollectAndPublishQuote obtains one vendor snapshot and passes it through
// licensed archival, deterministic mapping and two-phase publication. It
// does not certify real-time status or expose the result to a research task.
func (r *Runtime) CollectAndPublishQuote(ctx context.Context, catalog *dataservice.Catalog, provider, product string, license, policy, entity uuid.UUID, code string) (QuoteIngestResult, error) {
	if r == nil || r.database == nil || policy == uuid.Nil || entity == uuid.Nil || !validSecurityRequest(code) || strings.ToUpper(code) != code {
		return QuoteIngestResult{}, errors.New("emquant: invalid quote ingestion request")
	}
	collector, err := r.RegisteredCollector(catalog, provider, product, license)
	if err != nil {
		return QuoteIngestResult{}, err
	}
	// Reject unrelated or expired policy and entity before consuming quota.
	// Each subsequent stage rechecks these conditions under its own locks.
	var eligible bool
	if err := r.database.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM quote_mapping_policies q JOIN source_licenses l ON l.id=q.source_license_id
		JOIN providers p ON p.id=l.provider_id
		JOIN entities e ON e.id=$4 AND e.kind='stock' AND NOT e.synthetic AND e.code=$5
		WHERE q.id=$1 AND l.id=$2 AND l.provider_id=$3 AND l.product_code=$6
		AND q.dataset_id='mkt.intraday_quote' AND l.dataset_id=q.dataset_id
		AND p.enabled AND p.kind='vendor' AND NOT p.synthetic AND q.revoked_at IS NULL
		AND q.verified_at<=clock_timestamp() AND q.valid_from<=clock_timestamp()
		AND (q.valid_until IS NULL OR q.valid_until>clock_timestamp()))`, policy, license, provider, entity, code, product).Scan(&eligible); err != nil {
		return QuoteIngestResult{}, fmt.Errorf("emquant: quote ingestion preflight: %w", err)
	}
	if !eligible {
		return QuoteIngestResult{}, errors.New("emquant: quote ingestion policy or entity unavailable")
	}
	_, source, err := collector.CollectQuote(ctx, "mkt.intraday_quote", code, "TIME,NOW", "")
	if err != nil {
		return QuoteIngestResult{}, err
	}
	stage, err := r.StageQuote(ctx, catalog, source, policy, entity, code)
	if err != nil {
		return QuoteIngestResult{}, err
	}
	result := QuoteIngestResult{StageID: stage}
	result.AvailableAt, err = r.PublishStagedQuote(ctx, stage)
	if err != nil {
		return result, err
	}
	return result, nil
}
