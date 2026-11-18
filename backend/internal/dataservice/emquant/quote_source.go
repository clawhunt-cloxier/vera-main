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

func (s QuoteSource) Grant(use dataservice.SourceUse, code string) dataservice.SourceUseRequest {
	return dataservice.SourceUseRequest{LicenseID: s.LicenseID, ProviderID: s.ProviderID, Product: s.Product,
		DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: s.Fields, Codes: []string{code}, Use: use}
}

// ResolveQuoteSourceTx chooses an active all-instrument or code-scoped Choice
// grant and its active mapping policy. The selection is deterministic and the
// returned identity is only a candidate; callers must recheck each intended
// use in the transaction that archives, publishes, displays, or models data.
func ResolveQuoteSourceTx(ctx context.Context, tx pgx.Tx, provider, product, code, requirement string) (QuoteSource, error) {
	if tx == nil || strings.TrimSpace(provider) != provider || provider == "" || strings.TrimSpace(product) != product || product == "" || !validSecurityRequest(code) || strings.ToUpper(code) != code || (requirement != string(QuoteRealtimeTrade) && requirement != string(QuoteLatestAvailable)) {
		return QuoteSource{}, errors.New("emquant: invalid Choice quote source request")
	}
	rows, err := tx.Query(ctx, `SELECT l.id,q.id,l.field_names,
		q.time_convention,q.time_evidence_ref,q.price_unit,q.price_evidence_ref,q.latency_mode,coalesce(q.latency_evidence_ref,''),q.max_age_ms,
		coalesce(q.status_indicator,''),coalesce(q.status_evidence_ref,''),coalesce(q.status_mapping,'{}'::jsonb)
		FROM quote_mapping_policies q
		JOIN source_licenses l ON l.id=q.source_license_id
		JOIN providers p ON p.id=l.provider_id
		WHERE l.provider_id=$1 AND l.product_code=$2
		  AND l.dataset_id='mkt.intraday_quote' AND l.vendor_method='CSQSNAPSHOT'
		  AND (l.instrument_scope='all' OR $3 = ANY(l.instrument_codes))
		  AND l.revoked_at IS NULL AND l.verified_at<=clock_timestamp() AND l.valid_from<=clock_timestamp()
		  AND (l.valid_until IS NULL OR l.valid_until>clock_timestamp())
		  AND q.dataset_id=l.dataset_id AND q.revoked_at IS NULL AND q.verified_at<=clock_timestamp() AND q.valid_from<=clock_timestamp()
		  AND (q.valid_until IS NULL OR q.valid_until>clock_timestamp())
		  AND p.kind='vendor' AND p.enabled AND NOT p.synthetic
		ORDER BY (l.instrument_scope='listed') DESC, q.verified_at DESC, q.id DESC`, provider, product, code)
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
		out.ProviderID, out.Product, out.Fields = provider, product, fields
		return out, nil
	}
	if err := rows.Err(); err != nil {
		return QuoteSource{}, fmt.Errorf("emquant: resolve Choice quote source: %w", err)
	}
	return QuoteSource{}, errors.New("emquant: no active Choice quote grant or mapping for requested security")
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

// ResolveQuoteSource resolves a current source using a short read transaction.
func (r *Runtime) ResolveQuoteSource(ctx context.Context, provider, product, code string) (QuoteSource, error) {
	return r.resolveQuoteSource(ctx, provider, product, code, string(QuoteLatestAvailable))
}

func (r *Runtime) resolveQuoteSource(ctx context.Context, provider, product, code, requirement string) (QuoteSource, error) {
	if r == nil || r.database == nil {
		return QuoteSource{}, errors.New("emquant: Choice runtime unavailable")
	}
	var source QuoteSource
	err := pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
		var err error
		source, err = ResolveQuoteSourceTx(ctx, tx, provider, product, code, requirement)
		return err
	})
	return source, err
}
