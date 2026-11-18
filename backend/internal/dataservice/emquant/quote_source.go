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

// QuoteSource is the current Choice grant and mapping selected for one
// requested security. License and policy identities are resolved at request
// time from the provider/product and security code; callers do not configure
// a per-security UUID.
type QuoteSource struct {
	ProviderID string
	Product    string
	LicenseID  uuid.UUID
	PolicyID   uuid.UUID
	Fields     []string
	Policy     QuoteMappingPolicy
}

// Grant builds the exact-use authorization request for the selected source.
func (s QuoteSource) Grant(use dataservice.SourceUse, code string) dataservice.SourceUseRequest {
	return dataservice.SourceUseRequest{LicenseID: s.LicenseID, ProviderID: s.ProviderID, Product: s.Product,
		DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: s.Fields, Codes: []string{code}, Use: use}
}

// ResolveQuoteSourceTx chooses an active all-instrument or code-scoped Choice
// grant and its active mapping policy. The selection is deterministic and the
// returned identity is only a candidate; callers must recheck each intended
// use in the transaction that archives, publishes, displays, or models data.
func ResolveQuoteSourceTx(ctx context.Context, tx pgx.Tx, provider, product, code, requirement string) (QuoteSource, error) {
	if tx == nil || strings.TrimSpace(provider) != provider || provider == "" || strings.TrimSpace(product) != product || product == "" || !validBriefCode(code) || (requirement != string(QuoteRealtimeTrade) && requirement != string(QuoteLatestAvailable)) {
		return QuoteSource{}, errors.New("emquant: invalid Choice quote source request")
	}
	return resolveQuoteSource(ctx, tx, provider, product, code, requirement, true)
}

var errQuoteSourceUnavailable = errors.New("emquant: no active Choice quote grant or mapping")

type QuoteCapabilities struct {
	Latest, Realtime bool
}

// AllStockQuoteCapabilities checks which report options can be offered.
// An empty code selects only all-stock grants; this never calls the SDK.
func AllStockQuoteCapabilities(ctx context.Context, pool *pgxpool.Pool, provider, product string) (QuoteCapabilities, error) {
	source, err := resolveQuoteSource(ctx, pool, provider, product, "", string(QuoteLatestAvailable), true)
	if errors.Is(err, errQuoteSourceUnavailable) {
		return QuoteCapabilities{}, nil
	}
	if err != nil {
		return QuoteCapabilities{}, err
	}
	return QuoteCapabilities{Latest: true, Realtime: supportsRealtimeTrade(source.Policy)}, nil
}

type quoteSourceQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func resolveQuoteSource(ctx context.Context, db quoteSourceQueryer, provider, product, code, requirement string, reportUses bool) (QuoteSource, error) {
	rows, err := db.Query(ctx, `SELECT l.id,q.id,l.field_names,
		q.time_convention,q.time_evidence_ref,q.price_unit,q.price_evidence_ref,q.latency_mode,coalesce(q.latency_evidence_ref,''),q.max_age_ms,
		coalesce(q.status_indicator,''),coalesce(q.status_evidence_ref,''),coalesce(q.status_mapping,'{}'::jsonb)
		FROM quote_mapping_policies q
		JOIN source_licenses l ON l.id=q.source_license_id
		JOIN providers p ON p.id=l.provider_id
		WHERE l.provider_id=$1 AND l.product_code=$2
		  AND l.dataset_id='mkt.intraday_quote' AND l.vendor_method='CSQSNAPSHOT'
		  AND (l.instrument_scope='all' OR ($3 <> '' AND $3 = ANY(l.instrument_codes)))
		  AND l.revoked_at IS NULL AND l.verified_at<=clock_timestamp() AND l.valid_from<=clock_timestamp()
		  AND (l.valid_until IS NULL OR l.valid_until>clock_timestamp())
		  AND q.dataset_id=l.dataset_id AND q.revoked_at IS NULL AND q.verified_at<=clock_timestamp() AND q.valid_from<=clock_timestamp()
		  AND (q.valid_until IS NULL OR q.valid_until>clock_timestamp())
		  AND p.kind='vendor' AND p.enabled AND NOT p.synthetic AND l.allow_storage
		  AND (NOT $4 OR (l.allow_display AND l.allow_model_processing AND p.allow_model_processing))
		ORDER BY (l.instrument_scope='all') DESC, q.verified_at DESC, q.id DESC`, provider, product, code, reportUses)
	if err != nil {
		return QuoteSource{}, fmt.Errorf("emquant: resolve Choice quote source: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var out QuoteSource
		var maxAgeMS int64
		var statusMapping []byte
		if err := rows.Scan(&out.LicenseID, &out.PolicyID, &out.Fields, &out.Policy.TimeConvention, &out.Policy.TimeEvidence,
			&out.Policy.PriceUnit, &out.Policy.PriceEvidence, &out.Policy.LatencyMode, &out.Policy.LatencyEvidence, &maxAgeMS,
			&out.Policy.StatusIndicator, &out.Policy.StatusEvidence, &statusMapping); err != nil {
			return QuoteSource{}, fmt.Errorf("emquant: read Choice quote source: %w", err)
		}
		if err := json.Unmarshal(statusMapping, &out.Policy.StatusMapping); err != nil {
			continue
		}
		out.Policy.MaxAge = durationMillis(maxAgeMS)
		out.Policy.LicenseRef = out.LicenseID.String()
		fields, err := QuotePolicyFields(out.Policy)
		if err != nil || !containsFields(out.Fields, fields) {
			continue
		}
		// The current policy can downgrade a product. Do not revive an older
		// realtime declaration when the current policy lacks that evidence.
		if requirement == string(QuoteRealtimeTrade) && !supportsRealtimeTrade(out.Policy) {
			return QuoteSource{}, errors.New("emquant: realtime quote requires verified latency and continuous trading status")
		}
		out.ProviderID, out.Product, out.Fields = provider, product, fields
		return out, nil
	}
	if err := rows.Err(); err != nil {
		return QuoteSource{}, fmt.Errorf("emquant: resolve Choice quote source: %w", err)
	}
	return QuoteSource{}, errQuoteSourceUnavailable
}

func supportsRealtimeTrade(policy QuoteMappingPolicy) bool {
	if policy.LatencyMode != "real_time" || policy.StatusIndicator == "" {
		return false
	}
	for _, status := range policy.StatusMapping {
		if status == "continuous" {
			return true
		}
	}
	return false
}

func durationMillis(ms int64) (d time.Duration) {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func containsFields(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, field := range have {
		set[strings.ToUpper(field)] = struct{}{}
	}
	for _, field := range want {
		if _, ok := set[strings.ToUpper(field)]; !ok {
			return false
		}
	}
	return true
}

func (r *Runtime) resolveQuoteSource(ctx context.Context, provider, product, code, requirement string) (QuoteSource, error) {
	if r == nil || r.database == nil {
		return QuoteSource{}, errors.New("emquant: Choice runtime unavailable")
	}
	if provider == "" || product == "" || strings.TrimSpace(provider) != provider || strings.TrimSpace(product) != product || !validBriefCode(code) {
		return QuoteSource{}, errors.New("emquant: invalid Choice quote source request")
	}
	if requirement == "" {
		return resolveQuoteSource(ctx, r.database, provider, product, code, string(QuoteLatestAvailable), false)
	}
	if requirement != string(QuoteRealtimeTrade) && requirement != string(QuoteLatestAvailable) {
		return QuoteSource{}, errors.New("emquant: invalid Choice quote requirement")
	}
	return resolveQuoteSource(ctx, r.database, provider, product, code, requirement, true)
}
