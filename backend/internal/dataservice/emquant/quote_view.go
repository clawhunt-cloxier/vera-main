package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type QuoteRequirement string

const (
	QuoteLatestAvailable QuoteRequirement = "latest_available"
	QuoteRealtimeTrade   QuoteRequirement = "realtime_trade"
)

// QuoteView is a candidate for inclusion in a task snapshot. Its IDs and
// cutoff must be bound atomically with all other datasets and the task epoch;
// returning this value alone does not freeze a task or authorize later use.
type QuoteView struct {
	Cutoff      time.Time
	Requirement QuoteRequirement
	Use         dataservice.SourceUse
	Quotes      []PublishedQuote
	Unavailable []uuid.UUID
}

type PublishedQuote struct {
	StageID     uuid.UUID
	EntityID    uuid.UUID
	RawObjectID uuid.UUID
	PolicyID    uuid.UUID
	LicenseID   uuid.UUID
	ProviderID  string
	Product     string
	Code        string
	Price       string
	EventAt     time.Time
	ReceivedAt  time.Time
	AvailableAt time.Time
	LatencyMode string
	MarketState string
	RawSHA256   string
	RawRef      string
	Locators    map[string]string
	Fields      []string `json:",omitempty"`
}

// Historical quote components predate explicit field scope and contain only
// TIME,NOW. New components persist their exact licensed field list.
func publishedQuoteFields(q PublishedQuote) []string {
	if len(q.Fields) == 0 {
		return []string{"TIME", "NOW"}
	}
	return q.Fields
}

// QuoteBoundary binds one database cutoff and snapshot to one transaction.
// The caller can share its cutoff with the other data-service selectors when
// atomically constructing a multi-dataset task manifest.
type QuoteBoundary struct {
	cutoff   time.Time
	snapshot string
	xid      string
}

func (b QuoteBoundary) Cutoff() time.Time { return b.cutoff }

func (b QuoteBoundary) verify(ctx context.Context, tx pgx.Tx) error {
	if tx == nil {
		return errors.New("emquant: missing quote view transaction")
	}
	var snapshot, xid string
	if err := tx.QueryRow(ctx, `SELECT pg_current_snapshot()::text,pg_current_xact_id()::text`).Scan(&snapshot, &xid); err != nil {
		return fmt.Errorf("emquant: check quote view identity: %w", err)
	}
	if b.cutoff.IsZero() || b.snapshot == "" || b.xid == "" || snapshot != b.snapshot || xid != b.xid {
		return errors.New("emquant: quote boundary belongs to another transaction")
	}
	return nil
}

// BeginQuoteBoundary acquires a database-clock cutoff in a fixed, read-write
// MVCC transaction. It does not freeze a task or approve a plan.
func BeginQuoteBoundary(ctx context.Context, tx pgx.Tx) (QuoteBoundary, error) {
	if tx == nil {
		return QuoteBoundary{}, errors.New("emquant: missing quote view transaction")
	}
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil || (isolation != "repeatable read" && isolation != "serializable") {
		return QuoteBoundary{}, errors.New("emquant: quote view requires a fixed MVCC transaction")
	}
	var b QuoteBoundary
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),pg_current_snapshot()::text,pg_current_xact_id()::text`).Scan(&b.cutoff, &b.snapshot, &b.xid); err != nil {
		return QuoteBoundary{}, fmt.Errorf("emquant: acquire quote view: %w", err)
	}
	if b.snapshot == "" || b.xid == "" {
		return QuoteBoundary{}, errors.New("emquant: empty database view identity")
	}
	b.cutoff = b.cutoff.UTC().Truncate(time.Microsecond)
	return b, nil
}

// SelectPublishedQuotes reads only publications visible in the caller's fixed
// PostgreSQL MVCC view. The caller must use a read-write REPEATABLE READ or SERIALIZABLE transaction,
// keep this transaction open for atomic task/manifest binding, and reauthorize
// current uses when a frozen report is read or sent to a model. This method
// never falls back to a stale quote or claims an unknown market state is live.
func (r *Runtime) SelectPublishedQuotes(ctx context.Context, tx pgx.Tx, entities []uuid.UUID, requirement QuoteRequirement, use dataservice.SourceUse) (QuoteView, error) {
	boundary, err := BeginQuoteBoundary(ctx, tx)
	if err != nil {
		return QuoteView{}, err
	}
	return r.SelectPublishedQuotesAt(ctx, tx, boundary, entities, requirement, use)
}

// SelectPublishedQuotesAt reuses a boundary created in this same transaction.
// A different transaction, even one using the same cutoff, cannot reuse it.
func (r *Runtime) SelectPublishedQuotesAt(ctx context.Context, tx pgx.Tx, boundary QuoteBoundary, entities []uuid.UUID, requirement QuoteRequirement, use dataservice.SourceUse) (QuoteView, error) {
	if r == nil || r.store == nil || tx == nil || len(entities) == 0 || len(entities) > 64 || (requirement != QuoteLatestAvailable && requirement != QuoteRealtimeTrade) || (use != dataservice.SourceDisplay && use != dataservice.SourceModel) {
		return QuoteView{}, errors.New("emquant: invalid quote view request")
	}
	ids := slices.Clone(entities)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if ids[0] == uuid.Nil || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		return QuoteView{}, errors.New("emquant: invalid or duplicate quote entity")
	}
	if err := boundary.verify(ctx, tx); err != nil {
		return QuoteView{}, err
	}
	cutoff := boundary.cutoff
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (s.entity_id)
		s.id,s.entity_id,s.raw_object_id,s.policy_id,o.source_license_id,o.provider_id,l.product_code,s.security_code,
		s.event_at,s.received_at,pub.available_at,q.latency_mode,s.payload->>'market_status',
		o.content_hash,o.stored_path,o.request_fingerprint,o.fetched_at,o.archived_at,o.vendor_fields,o.vendor_codes,
		q.time_convention,q.time_evidence_ref,q.price_unit,q.price_evidence_ref,coalesce(q.latency_evidence_ref,''),q.max_age_ms,
		coalesce(q.status_indicator,''),coalesce(q.status_evidence_ref,''),coalesce(q.status_mapping,'{}'::jsonb),
		s.payload::text,s.field_locators::text
		FROM quote_publications pub JOIN staged_quotes s ON s.id=pub.staged_quote_id
		JOIN raw_objects o ON o.id=s.raw_object_id AND o.source_license_id IS NOT NULL
		JOIN source_licenses l ON l.id=o.source_license_id AND l.provider_id=o.provider_id AND l.dataset_id=o.dataset_id
		JOIN providers p ON p.id=o.provider_id AND p.kind='vendor' AND p.enabled AND NOT p.synthetic
		JOIN entities e ON e.id=s.entity_id AND e.kind='stock' AND NOT e.synthetic AND e.code=s.security_code
		JOIN quote_mapping_policies q ON q.id=s.policy_id AND q.source_license_id=l.id AND q.dataset_id=o.dataset_id
		WHERE s.entity_id=ANY($1) AND o.dataset_id='mkt.intraday_quote' AND o.vendor_method='CSQSNAPSHOT'
		AND o.vendor_fields=CASE WHEN q.status_indicator IS NULL THEN ARRAY['TIME','NOW']::text[] ELSE ARRAY['TIME','NOW',q.status_indicator]::text[] END AND o.vendor_codes=ARRAY[s.security_code]::text[]
		AND l.vendor_method=o.vendor_method AND o.vendor_fields <@ l.field_names
		AND (l.instrument_scope='all' OR o.vendor_codes <@ l.instrument_codes)
		AND l.revoked_at IS NULL AND l.verified_at<=$2 AND l.valid_from<=$2 AND (l.valid_until IS NULL OR l.valid_until>$2)
		AND q.revoked_at IS NULL AND q.verified_at<=$2 AND q.valid_from<=$2 AND (q.valid_until IS NULL OR q.valid_until>$2)
		AND CASE $3 WHEN 'display' THEN l.allow_display WHEN 'model' THEN l.allow_model_processing AND p.allow_model_processing ELSE false END
		AND pub.available_at<=$2 AND s.event_at<=$2 AND s.received_at<=$2 AND s.event_at<=s.received_at
		AND o.fetched_at<=pub.available_at AND o.archived_at<=pub.available_at AND s.staged_at<=pub.available_at
		AND s.event_at >= $2 - q.max_age_ms * interval '1 millisecond'
		AND ($4::text='latest_available' OR (q.latency_mode='real_time' AND s.payload->>'market_status'='continuous'))
		ORDER BY s.entity_id,s.event_at DESC,pub.available_at DESC,s.id DESC`, ids, cutoff, string(use), string(requirement))
	if err != nil {
		return QuoteView{}, err
	}
	type candidate struct {
		quote                PublishedQuote
		fingerprint          string
		fetched, archived    time.Time
		fields, codes        []string
		policy               QuoteMappingPolicy
		maxAgeMS             int64
		payload, locatorsRaw string
		statusMapping        []byte
	}
	candidates := make([]candidate, 0, len(ids))
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.quote.StageID, &c.quote.EntityID, &c.quote.RawObjectID, &c.quote.PolicyID, &c.quote.LicenseID, &c.quote.ProviderID, &c.quote.Product, &c.quote.Code,
			&c.quote.EventAt, &c.quote.ReceivedAt, &c.quote.AvailableAt, &c.quote.LatencyMode, &c.quote.MarketState,
			&c.quote.RawSHA256, &c.quote.RawRef, &c.fingerprint, &c.fetched, &c.archived, &c.fields, &c.codes,
			&c.policy.TimeConvention, &c.policy.TimeEvidence, &c.policy.PriceUnit, &c.policy.PriceEvidence, &c.policy.LatencyEvidence, &c.maxAgeMS,
			&c.policy.StatusIndicator, &c.policy.StatusEvidence, &c.statusMapping,
			&c.payload, &c.locatorsRaw); err != nil {
			rows.Close()
			return QuoteView{}, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return QuoteView{}, err
	}
	view := QuoteView{Cutoff: cutoff, Requirement: requirement, Use: use, Quotes: make([]PublishedQuote, 0, len(candidates)), Unavailable: make([]uuid.UUID, 0)}
	found := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		if err := json.Unmarshal(c.statusMapping, &c.policy.StatusMapping); err != nil {
			return QuoteView{}, errors.New("emquant: invalid quote status mapping")
		}
		c.quote.Fields = slices.Clone(c.fields)
		var locked uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
			AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp()) FOR SHARE`, c.quote.PolicyID).Scan(&locked); err != nil {
			return QuoteView{}, fmt.Errorf("emquant: quote policy no longer current: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM entities WHERE id=$1 AND code=$2 AND kind='stock' AND NOT synthetic FOR SHARE`, c.quote.EntityID, c.quote.Code).Scan(&locked); err != nil {
			return QuoteView{}, fmt.Errorf("emquant: quote entity no longer eligible: %w", err)
		}
		grant := dataservice.SourceUseRequest{LicenseID: c.quote.LicenseID, ProviderID: c.quote.ProviderID, Product: c.quote.Product, DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: c.fields, Codes: c.codes, Use: use}
		if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
			return QuoteView{}, err
		}
		if err := r.verifyPublishedQuote(ctx, &c.quote, c.fingerprint, c.fetched, c.archived, c.policy, c.maxAgeMS, c.payload, c.locatorsRaw, cutoff); err != nil {
			return QuoteView{}, err
		}
		// Network reads and row-lock waits can outlast a grant or policy. Check
		// current use again after them, while the acquired locks are still held.
		if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
			return QuoteView{}, err
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
			AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, c.quote.PolicyID).Scan(&locked); err != nil {
			return QuoteView{}, fmt.Errorf("emquant: quote policy expired during verification: %w", err)
		}
		found[c.quote.EntityID] = true
		view.Quotes = append(view.Quotes, c.quote)
	}
	for _, id := range ids {
		if !found[id] {
			view.Unavailable = append(view.Unavailable, id)
		}
	}
	return view, nil
}

func (r *Runtime) verifyPublishedQuote(ctx context.Context, q *PublishedQuote, fingerprint string, fetched, archived time.Time, policy QuoteMappingPolicy, maxAgeMS int64, payloadJSON, locatorJSON string, cutoff time.Time) error {
	key, err := r.store.key(q.RawSHA256)
	if err != nil || q.RawRef != "s3://"+r.store.bucket+"/"+key {
		return errors.New("emquant: quote view source location mismatch")
	}
	body, err := r.store.Get(ctx, q.RawSHA256)
	if err != nil {
		return err
	}
	cube, observed, err := DecodeArchive(body, q.RawSHA256)
	if err != nil || !observed.UTC().Truncate(time.Microsecond).Equal(fetched) {
		return errors.New("emquant: quote view source observation mismatch")
	}
	// PostgreSQL metadata stores microseconds; using the later of the decoded
	// observation and archived lower bound here only avoids a precision-induced
	// false ordering failure. It is not exposed as the true archive time.
	archiveBound := archived
	if observed.After(archiveBound) {
		archiveBound = observed
	}
	policy.LatencyMode = q.LatencyMode
	policy.LicenseRef = q.LicenseID.String()
	policy.MaxAge = time.Duration(maxAgeMS) * time.Millisecond
	ref := ArchiveRef{RequestFingerprint: fingerprint, BodySHA256: q.RawSHA256, BodyRef: q.RawRef, ReceivedAt: observed, ArchivedAt: archiveBound}
	mapped, err := MapArchivedQuote(body, ref, q.Code, cutoff, policy)
	if err != nil {
		return err
	}
	var payload struct {
		EventAt     time.Time `json:"event_at"`
		ReceivedAt  time.Time `json:"received_at"`
		Price       string    `json:"last_price"`
		MarketState string    `json:"market_status"`
		LatencyMode string    `json:"latency_mode"`
	}
	var locators map[string]string
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(locatorJSON), &locators); err != nil {
		return err
	}
	if !payload.EventAt.Equal(mapped.Price.EventAt) || !payload.ReceivedAt.Equal(observed) || !q.EventAt.Equal(mapped.Price.EventAt) || !q.ReceivedAt.Equal(observed.UTC().Truncate(time.Microsecond)) || payload.Price != mapped.Price.Value || payload.MarketState != mapped.MarketStatus || payload.LatencyMode != q.LatencyMode || !maps.Equal(locators, mapped.Locators) || len(cube.Codes) != 1 {
		return errors.New("emquant: staged quote does not match its archived source")
	}
	q.Price = payload.Price
	q.ReceivedAt = observed.UTC()
	q.Locators = locators
	return nil
}
