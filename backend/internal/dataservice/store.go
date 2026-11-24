package dataservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists provider output and answers point-in-time queries.
type Store struct {
	pool             *pgxpool.Pool
	cat              *Catalog
	excludeSynthetic bool
	allowedCodes     []string
	providerID       string
	researchSubjects []string
	allowResearch    bool
	onDemandSubjects bool
}

// NewResearchStore restricts the full research path to its configured source.
// Identities used only for peer calculations remain available to the loader.
func NewResearchStore(pool *pgxpool.Pool, cat *Catalog, provider string, subjects []string, allowResearch bool) *Store {
	return &Store{pool: pool, cat: cat, excludeSynthetic: true, providerID: provider, researchSubjects: slices.Clone(subjects), allowResearch: allowResearch}
}

// NewOnDemandResearchStore also admits TuShare batches requested on a dual-source deployment.
func NewOnDemandResearchStore(pool *pgxpool.Pool, cat *Catalog, subjects []string, allowResearch bool) *Store {
	s := NewResearchStore(pool, cat, "tushare", subjects, allowResearch)
	s.onDemandSubjects = true
	return s
}

func NewStore(pool *pgxpool.Pool, cat *Catalog) *Store { return &Store{pool: pool, cat: cat} }

// NewVendorStore excludes synthetic providers and entities, including rows
// left over from a previous development deployment in the same database.
func NewVendorStore(pool *pgxpool.Pool, cat *Catalog) *Store {
	return &Store{pool: pool, cat: cat, excludeSynthetic: true}
}

func (s *Store) Catalog() *Catalog { return s.cat }

// RegisteredResearchSubject checks the current deployment's research list.
// A persisted entity may outlive a configuration change.
func (s *Store) RegisteredResearchSubject(code string) bool {
	return s.providerID == "tushare" && slices.Contains(s.researchSubjects, code)
}

// Sync atomically upserts a provider's entities, records and capabilities.
// Existing record keys are idempotent only when their content and provenance
// agree; a conflicting key fails the entire sync without replacing either.
func (s *Store) Sync(ctx context.Context, p Provider) (int, error) {
	return s.sync(ctx, s.pool, p, false, "")
}

type txBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func (s *Store) sync(ctx context.Context, db txBeginner, p Provider, observed bool, requestedCode string) (int, error) {
	if s.excludeSynthetic && p.Synthetic() {
		return 0, fmt.Errorf("synthetic provider not permitted in vendor store")
	}
	ents, err := p.Entities(ctx)
	if err != nil {
		return 0, err
	}
	recs, err := p.Records(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range recs {
		if err := s.cat.Validate(r); err != nil {
			return 0, fmt.Errorf("provider %s: %w", p.ID(), err)
		}
	}
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return 0, err
	}
	basis, _ := json.Marshal(p.Basis())
	kind := "vendor"
	if p.Synthetic() {
		kind = "fixture"
	}
	ids := map[string]uuid.UUID{}
	n := 0
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var directoryUpdates map[string]bool
		if requestedCode != "" {
			if err := s.RequireResearchUseTx(ctx, tx); err != nil {
				return err
			}
			validTarget := slices.ContainsFunc(ents, func(e Entity) bool {
				return e.Kind == "stock" && e.Code == requestedCode && e.Attrs["tushare_research_subject"] == true
			})
			validBar := slices.ContainsFunc(recs, func(r Record) bool {
				return r.DatasetID == "mkt.daily_bar" && r.EntityCode == requestedCode && r.Period != nil &&
					(r.QualityState == "" || r.QualityState == "PASS" || r.QualityState == "WARN")
			})
			if !validTarget || !validBar {
				return &ProviderError{Class: ErrNotCovered, Err: fmt.Errorf("TuShare %s 缺少已验证证券信息或可用日线", requestedCode)}
			}
		}
		if observed {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 508417))`, p.ID()); err != nil {
				return err
			}
			var err error
			recs, directoryUpdates, err = prepareObserved(ctx, tx, p.ID(), recs)
			if err != nil {
				return err
			}
		}
		var providerID string
		if err := tx.QueryRow(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing,synthetic,basis) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET name=excluded.name,
                allow_model_processing=providers.allow_model_processing AND excluded.allow_model_processing, basis=excluded.basis
            WHERE providers.kind=excluded.kind AND providers.synthetic=excluded.synthetic
            RETURNING id`, p.ID(), p.Name(), kind, p.AllowModelProcessing(), p.Synthetic(), basis).Scan(&providerID); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("provider %s: identity conflicts with existing provider kind", p.ID())
			}
			return err
		}
		for _, e := range ents {
			protectDirectory := observed && p.ID() == "tushare" && e.Kind == "stock" && !directoryUpdates[e.Code]
			id, err := syncEntity(ctx, tx, p, e, protectDirectory)
			if err != nil {
				return fmt.Errorf("entity %s: %w", e.Code, err)
			}
			ids[e.Code] = id
			for _, a := range append([]string{e.Code}, e.Aliases...) {
				if _, err := tx.Exec(ctx, `INSERT INTO entity_aliases VALUES($1,$2) ON CONFLICT DO NOTHING`, id, strings.ToLower(a)); err != nil {
					return err
				}
			}
		}
		rows := make([][]any, 0, len(recs))
		for _, r := range recs {
			var eid *uuid.UUID
			if r.EntityCode != "" {
				id, ok := ids[r.EntityCode]
				if !ok {
					return fmt.Errorf("record %s references unknown entity %s", r.RecordKey, r.EntityCode)
				}
				eid = &id
			}
			lk := r.LogicalKey
			if lk == "" {
				lk = r.RecordKey
			}
			payload, _ := json.Marshal(r.Payload)
			qs := r.QualityState
			if qs == "" {
				qs = "PASS"
			}
			rows = append(rows, []any{r.DatasetID, eid, r.RecordKey, lk, max(r.Version, 1), r.Period, r.PublishedAt, r.AvailableAt,
				p.ID(), r.Origin, r.SourceTier, nilIfEmpty(r.Basis), payload, r.Hash(),
				nilIfEmpty(r.RawRef), nilIfEmpty(r.RawSHA256), nilIfEmpty(r.RawLocator), nilIfZero(r.MappingVersion), qs})
		}
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE records_in (LIKE records INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
			return err
		}
		cols := []string{"dataset_id", "entity_id", "record_key", "logical_key", "record_version", "period", "published_at",
			"available_at", "provider_id", "origin", "source_tier", "basis", "payload", "content_hash",
			"raw_ref", "raw_sha256", "raw_locator", "mapping_version", "quality_state"}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"records_in"}, cols, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO records (`+strings.Join(cols, ",")+`) SELECT `+strings.Join(cols, ",")+
			` FROM records_in ON CONFLICT (dataset_id, record_key, record_version) DO NOTHING`)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		var conflict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
            SELECT 1 FROM records_in i JOIN records r USING (dataset_id, record_key, record_version)
            WHERE ROW(r.entity_id, r.logical_key, r.period, r.published_at, r.available_at,
                      r.provider_id, r.origin, r.source_tier, r.basis, r.payload, r.content_hash,
                      r.raw_ref, r.raw_sha256, r.raw_locator, r.mapping_version)
              IS DISTINCT FROM ROW(i.entity_id, i.logical_key, i.period, i.published_at, i.available_at,
                      i.provider_id, i.origin, i.source_tier, i.basis, i.payload, i.content_hash,
                      i.raw_ref, i.raw_sha256, i.raw_locator, i.mapping_version)
        )`).Scan(&conflict); err != nil {
			return err
		}
		if conflict {
			return fmt.Errorf("provider %s: immutable record key conflicts with existing content or provenance", p.ID())
		}
		for _, c := range caps {
			var eid *uuid.UUID
			if c.EntityCode != "" {
				id := ids[c.EntityCode]
				eid = &id
			}
			if _, err := tx.Exec(ctx, `INSERT INTO capabilities(provider_id,dataset_id,entity_id,coverage_from,coverage_to) VALUES($1,$2,$3,$4,$5)
				ON CONFLICT (provider_id,dataset_id,entity_id) DO UPDATE SET coverage_from=excluded.coverage_from, coverage_to=excluded.coverage_to, updated_at=now()`,
				p.ID(), c.DatasetID, eid, c.From, c.To); err != nil {
				return err
			}
		}
		if requestedCode != "" {
			_, err := tx.Exec(ctx, `INSERT INTO tushare_requested_subjects(code,published_at) VALUES($1,clock_timestamp())
				ON CONFLICT (code) DO UPDATE SET published_at=excluded.published_at`, requestedCode)
			return err
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func syncEntity(ctx context.Context, tx pgx.Tx, p Provider, e Entity, protectDirectory bool) (uuid.UUID, error) {
	var id uuid.UUID
	if protectDirectory {
		// Preserve the fields from the dated snapshot while allowing the same
		// security to be registered as a research subject in this batch.
		err := tx.QueryRow(ctx, `UPDATE entities e
			SET attrs=CASE WHEN $2 OR EXISTS (SELECT 1 FROM tushare_requested_subjects WHERE code=e.code)
				THEN e.attrs || '{"tushare_research_subject":true}'::jsonb ELSE e.attrs END
			WHERE e.code=$1 AND e.kind='stock' AND NOT e.synthetic AND EXISTS (
				SELECT 1 FROM records r WHERE r.entity_id=e.id AND r.provider_id='tushare'
					AND r.dataset_id='ref.security' AND r.mapping_version >= 2 AND r.period IS NOT NULL)
			RETURNING e.id`, e.Code, e.Attrs["tushare_research_subject"] == true).Scan(&id)
		if err != pgx.ErrNoRows {
			return id, err
		}
	}
	attrs, _ := json.Marshal(e.Attrs)
	err := tx.QueryRow(ctx, `INSERT INTO entities(kind,code,name,exchange,status,is_financial,classification,synthetic,attrs)
		VALUES($1,$2,$3,NULLIF($4,''),$5,$6,NULLIF($7,''),$8,$9)
		ON CONFLICT (code) DO UPDATE SET name=excluded.name, exchange=excluded.exchange, status=excluded.status, is_financial=excluded.is_financial, classification=excluded.classification,
		attrs=CASE WHEN $10 AND EXISTS (SELECT 1 FROM tushare_requested_subjects WHERE code=excluded.code)
			THEN excluded.attrs || '{"tushare_research_subject":true}'::jsonb ELSE excluded.attrs END
		WHERE entities.synthetic=excluded.synthetic AND entities.kind=excluded.kind
		RETURNING id`, e.Kind, e.Code, e.Name, e.Exchange, e.Status, e.IsFinancial, e.Classification, p.Synthetic(), attrs, p.ID() == "tushare").Scan(&id)
	return id, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nilIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// StoredRecord is a record as read back for research, with its evidence identity.
type StoredRecord struct {
	ID             uuid.UUID
	DatasetID      string
	EntityID       *uuid.UUID
	RecordKey      string
	Version        int
	Period         *time.Time
	PublishedAt    *time.Time
	AvailableAt    time.Time
	ProviderID     string
	Origin         string
	SourceTier     string
	Basis          string
	Payload        map[string]any
	ContentHash    string
	QualityState   string
	RawRef         *string
	RawSHA256      *string
	RawLocator     *string
	MappingVersion *int
}

// Query selects the latest revision of each logical record available at asOf.
type Query struct {
	DatasetIDs []string
	EntityIDs  []uuid.UUID // empty = no entity filter
	From, To   *time.Time  // period range, inclusive
	AsOf       time.Time
	ModelUse   bool // records will reach a model: only providers licensed for model processing
}

const recordCols = `r.id, r.dataset_id, r.entity_id, r.record_key, r.record_version, r.period, r.published_at,
		r.available_at, r.provider_id, r.origin, r.source_tier, coalesce(r.basis,''), r.payload, r.content_hash,
		r.quality_state, r.raw_ref, r.raw_sha256, r.raw_locator, r.mapping_version`

// Financial facts require a known disclosure time. Other sources may not have
// one, but when supplied it must never be later than the snapshot cutoff.
func visibleAt(alias, cutoff string) string {
	return fmt.Sprintf(`%[1]s.available_at <= %[2]s
		AND (%[1]s.published_at IS NULL OR %[1]s.published_at <= %[2]s)
		AND (%[1]s.dataset_id NOT IN ('fin.statement','fin.segment') OR %[1]s.published_at IS NOT NULL)`, alias, cutoff)
}

var visibleAtCutoff = visibleAt("r", "$2")

// A visible correction supersedes its predecessor even when the correction is
// quarantined. A later disclosure or publication cannot affect an earlier cutoff.
func latestRevisionAt(cutoff string) string {
	return `NOT EXISTS (SELECT 1 FROM records newer
		WHERE newer.dataset_id=r.dataset_id AND newer.logical_key=r.logical_key
		AND newer.provider_id=r.provider_id AND newer.record_version>r.record_version
		AND ` + visibleAt("newer", cutoff) + `)`
}

// A real-data store must exclude a vendor record attached to a synthetic
// entity. Intraday records are also withheld from this legacy query path until
// it is replaced by publication-aware MVCC selection and current-use checks.
const realSourceFilter = ` AND (NOT %s::bool OR (NOT p.synthetic AND p.kind='vendor' AND
 r.dataset_id NOT IN ('mkt.intraday_quote','mkt.intraday_index_quote') AND
 (r.entity_id IS NULL OR EXISTS (SELECT 1 FROM entities real_entity WHERE real_entity.id=r.entity_id AND NOT real_entity.synthetic))))`

func scanRecord(row pgx.CollectableRow) (StoredRecord, error) {
	var r StoredRecord
	err := row.Scan(&r.ID, &r.DatasetID, &r.EntityID, &r.RecordKey, &r.Version, &r.Period, &r.PublishedAt, &r.AvailableAt,
		&r.ProviderID, &r.Origin, &r.SourceTier, &r.Basis, &r.Payload, &r.ContentHash,
		&r.QualityState, &r.RawRef, &r.RawSHA256, &r.RawLocator, &r.MappingVersion)
	return r, err
}

func (s *Store) Query(ctx context.Context, q Query) ([]StoredRecord, error) {
	sql := `SELECT ` + recordCols + `
		FROM records r JOIN providers p ON p.id = r.provider_id AND p.enabled
		WHERE r.dataset_id = ANY($1) AND ` + visibleAtCutoff + `
		AND ($3::uuid[] IS NULL OR r.entity_id = ANY($3)) AND ($4::date IS NULL OR r.period >= $4) AND ($5::date IS NULL OR r.period <= $5)
		AND (NOT $6::bool OR p.allow_model_processing) AND ($8='' OR p.id=$8)` + fmt.Sprintf(realSourceFilter, "$7") + `
		AND r.quality_state IN ('PASS','WARN') AND ` + latestRevisionAt("$2")
	var ents []uuid.UUID
	if len(q.EntityIDs) > 0 {
		ents = q.EntityIDs
	}
	rows, err := s.pool.Query(ctx, sql, q.DatasetIDs, q.AsOf, ents, q.From, q.To, q.ModelUse, s.excludeSynthetic, s.providerID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, scanRecord)
	if err != nil {
		return nil, err
	}
	sortRecords(out)
	return out, nil
}

func sortRecords(out []StoredRecord) {
	slices.SortFunc(out, func(a, b StoredRecord) int {
		if c := strings.Compare(a.DatasetID, b.DatasetID); c != 0 {
			return c
		}
		if a.Period != nil && b.Period != nil && !a.Period.Equal(*b.Period) {
			return a.Period.Compare(*b.Period)
		}
		return strings.Compare(a.RecordKey, b.RecordKey)
	})
}

// Coverage reports which datasets have at least one record per entity as of asOf (capability check for plans).
// Only PASS/WARN records count; QUARANTINED/REJECTED are excluded.
func (s *Store) Coverage(ctx context.Context, entities []uuid.UUID, asOf time.Time) (map[uuid.UUID]map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT r.entity_id, r.dataset_id FROM records r JOIN providers p ON p.id=r.provider_id AND p.enabled
		WHERE r.entity_id = ANY($1) AND `+visibleAtCutoff+` AND p.allow_model_processing`+fmt.Sprintf(realSourceFilter, "$3")+`
		AND ($4='' OR p.id=$4) AND r.quality_state IN ('PASS','WARN') AND `+latestRevisionAt("$2"), entities, asOf, s.excludeSynthetic, s.providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]map[string]bool{}
	for rows.Next() {
		var e uuid.UUID
		var d string
		if err := rows.Scan(&e, &d); err != nil {
			return nil, err
		}
		if out[e] == nil {
			out[e] = map[string]bool{}
		}
		out[e][d] = true
	}
	return out, rows.Err()
}

// HasGlobal reports whether an entity-less dataset has data as of asOf.
// Only PASS/WARN records count.
func (s *Store) HasGlobal(ctx context.Context, dataset string, asOf time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records r JOIN providers p ON p.id=r.provider_id AND p.enabled
		WHERE r.dataset_id=$1 AND r.entity_id IS NULL AND `+visibleAtCutoff+` AND p.allow_model_processing`+fmt.Sprintf(realSourceFilter, "$3")+`
		AND ($4='' OR p.id=$4) AND r.quality_state IN ('PASS','WARN') AND `+latestRevisionAt("$2")+`)`, dataset, asOf, s.excludeSynthetic, s.providerID).Scan(&ok)
	return ok, err
}

// Snapshot loads a frozen research record set, rechecking its cutoff and the
// provider's current permission for model processing.
func (s *Store) Snapshot(ctx context.Context, hash string) (time.Time, []StoredRecord, error) {
	var asOf time.Time
	var ids []uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT as_of, record_ids FROM snapshots WHERE hash=$1`, hash).Scan(&asOf, &ids); err != nil {
		return asOf, nil, err
	}
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if len(slices.Compact(slices.Clone(sorted))) != len(sorted) {
		return asOf, nil, fmt.Errorf("snapshot %s contains duplicate records", hash)
	}
	newHash := snapshotDigest(asOf, sorted, time.RFC3339Nano, "vera.snapshot.v2\x00")
	legacyHash := snapshotDigest(asOf, sorted, time.RFC3339, "")
	if hash != newHash && hash != legacyHash {
		return asOf, nil, fmt.Errorf("snapshot %s: manifest hash mismatch", hash)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+recordCols+` FROM records r JOIN providers p ON p.id=r.provider_id
		WHERE r.id = ANY($1) AND `+visibleAtCutoff+` AND p.enabled AND p.allow_model_processing`+fmt.Sprintf(realSourceFilter, "$3")+`
		AND ($4='' OR p.id=$4) AND r.quality_state IN ('PASS','WARN')`, ids, asOf, s.excludeSynthetic, s.providerID)
	if err != nil {
		return asOf, nil, err
	}
	out, err := pgx.CollectRows(rows, scanRecord)
	if err != nil {
		return asOf, nil, err
	}
	if len(out) != len(ids) {
		return asOf, nil, fmt.Errorf("snapshot %s: %d of %d records eligible", hash, len(out), len(ids))
	}
	sortRecords(out)
	return asOf, out, nil
}

// Freeze stores an eligible record set for model research and returns its hash.
// It rejects any record whose quality_state is QUARANTINED or REJECTED before
// performing the temporal eligibility check, so bad data cannot enter a snapshot.
func (s *Store) Freeze(ctx context.Context, asOf time.Time, ids []uuid.UUID) (string, error) {
	// PostgreSQL timestamptz stores microseconds; hash the same instant that
	// will be persisted rather than discarding subsecond precision or hashing
	// nanoseconds the database cannot retain.
	asOf = asOf.UTC().Truncate(time.Microsecond)
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	sorted = slices.Compact(sorted)
	hash := snapshotDigest(asOf, sorted, time.RFC3339Nano, "vera.snapshot.v2\x00")
	legacyHash := snapshotDigest(asOf, sorted, time.RFC3339, "")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock and qualify records in a single query so that a concurrent
		// quality_state change between check and lock cannot slip a bad
		// record into the snapshot. FOR SHARE holds an implicit row lock that
		// blocks quality_state updates until the transaction commits.
		rows, err := tx.Query(ctx, `SELECT r.id FROM records r JOIN providers p ON p.id=r.provider_id
			WHERE r.id = ANY($1) AND `+visibleAtCutoff+` AND p.enabled AND p.allow_model_processing`+fmt.Sprintf(realSourceFilter, "$3")+`
			AND ($4='' OR p.id=$4) AND r.quality_state IN ('PASS','WARN')
			AND (`+latestRevisionAt("$2")+` OR EXISTS (
				SELECT 1 FROM snapshots WHERE hash IN ($5,$6) AND as_of=$2 AND record_ids=$1
			)) FOR SHARE OF r,p`, sorted, asOf, s.excludeSynthetic, s.providerID, hash, legacyHash)
		if err != nil {
			return err
		}
		var count int
		for rows.Next() {
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != len(sorted) {
			// Distinguish quality rejection from temporal/permission rejection
			// so the caller gets an actionable message.
			var badCount int
			if qerr := tx.QueryRow(ctx,
				`SELECT count(*) FROM records WHERE id = ANY($1) AND quality_state NOT IN ('PASS','WARN')`,
				sorted).Scan(&badCount); qerr != nil {
				return fmt.Errorf("freeze: eligibility check failed: %w", qerr)
			}
			if badCount > 0 {
				return fmt.Errorf("freeze: %d record(s) have QUARANTINED or REJECTED quality state", badCount)
			}
			return fmt.Errorf("snapshot contains missing, temporally ineligible, or currently unpermitted records")
		}
		// Preserve a previously frozen legacy snapshot only when its stored
		// cutoff and exact IDs match; the old hash omitted fractional seconds.
		var matches bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM snapshots WHERE hash=$1 AND as_of=$2 AND record_ids=$3)`, legacyHash, asOf, sorted).Scan(&matches); err != nil {
			return err
		}
		if matches {
			hash = legacyHash
			return nil
		}
		tag, err := tx.Exec(ctx, `INSERT INTO snapshots(hash,as_of,record_ids) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, hash, asOf, sorted)
		if err != nil || tag.RowsAffected() == 1 {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM snapshots WHERE hash=$1 AND as_of=$2 AND record_ids=$3)`, hash, asOf, sorted).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf("snapshot hash conflicts with stored content")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hash, nil
}

func snapshotDigest(asOf time.Time, sorted []uuid.UUID, layout, domain string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(domain))
	_, _ = h.Write([]byte(asOf.UTC().Format(layout)))
	for _, id := range sorted {
		_, _ = h.Write(id[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get loads specific records (evidence lookup).
func (s *Store) Get(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]StoredRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recordCols+` FROM records r JOIN providers p ON p.id=r.provider_id WHERE r.id = ANY($1) AND ($3='' OR p.id=$3 AND p.enabled)`+fmt.Sprintf(realSourceFilter, "$2"), ids, s.excludeSynthetic, s.providerID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanRecord)
	out := map[uuid.UUID]StoredRecord{}
	for _, r := range list {
		out[r.ID] = r
	}
	return out, err
}

// Entity is a catalog row as used by research and the API.
type EntityRow struct {
	ID             uuid.UUID      `json:"id"`
	Kind           string         `json:"kind"`
	Code           string         `json:"code"`
	Name           string         `json:"name"`
	Exchange       *string        `json:"exchange"`
	Status         string         `json:"status"`
	IsFinancial    bool           `json:"is_financial"`
	Classification *string        `json:"classification"`
	Synthetic      bool           `json:"synthetic"`
	Attrs          map[string]any `json:"attrs"`
}

const entityCols = `id, kind, code, name, exchange, status, is_financial, classification, synthetic, attrs`

func scanEntity(row pgx.CollectableRow) (EntityRow, error) {
	var e EntityRow
	err := row.Scan(&e.ID, &e.Kind, &e.Code, &e.Name, &e.Exchange, &e.Status, &e.IsFinancial, &e.Classification, &e.Synthetic, &e.Attrs)
	return e, err
}

func (s *Store) Entities(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]EntityRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE id = ANY($1) AND (NOT $2::bool OR NOT synthetic) AND ($3::text[] IS NULL OR code=ANY($3))`, ids, s.excludeSynthetic, s.allowedCodes)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanEntity)
	out := map[uuid.UUID]EntityRow{}
	for _, e := range list {
		out[e.ID] = e
	}
	return out, err
}

func (s *Store) EntityByCode(ctx context.Context, code string) (EntityRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE code=$1 AND (NOT $2::bool OR NOT synthetic) AND ($3::text[] IS NULL OR code=ANY($3))`, code, s.excludeSynthetic, s.allowedCodes)
	if err != nil {
		return EntityRow{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanEntity)
}

func (s *Store) EntitiesByKind(ctx context.Context, kind string) ([]EntityRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE kind=$1 AND (NOT $2::bool OR NOT synthetic) AND ($3::text[] IS NULL OR code=ANY($3)) ORDER BY code`, kind, s.excludeSynthetic, s.allowedCodes)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntity)
}

// Search matches names, codes and aliases (trigram similarity plus substring), best first.
func (s *Store) Search(ctx context.Context, q, kind string, limit int) ([]EntityRow, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities e WHERE ($2 = '' OR kind = $2) AND (NOT $4::bool OR NOT e.synthetic) AND
		($5::text[] IS NULL OR code=ANY($5) OR ($6 AND EXISTS (SELECT 1 FROM tushare_requested_subjects t WHERE t.code=e.code))) AND EXISTS (
		SELECT 1 FROM entity_aliases a WHERE a.entity_id = e.id AND (a.alias LIKE '%' || $1 || '%' OR similarity(a.alias, $1) > 0.3))
		ORDER BY (SELECT max(CASE WHEN a.alias = $1 THEN 2 ELSE similarity(a.alias, $1) END) FROM entity_aliases a WHERE a.entity_id = e.id) DESC, code
		LIMIT $3`, q, kind, limit, s.excludeSynthetic, s.searchableCodes(), s.onDemandSubjects)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntity)
}

// Mentioned returns entities whose alias occurs verbatim in free text (rule-based plan parsing). Longest alias first.
func (s *Store) Mentioned(ctx context.Context, text string) ([]EntityRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities e JOIN LATERAL (
		SELECT max(length(alias)) AS l FROM entity_aliases a WHERE a.entity_id = e.id AND length(a.alias) >= 2 AND strpos(lower($1), a.alias) > 0) m ON m.l IS NOT NULL
        WHERE (NOT $2::bool OR NOT e.synthetic) AND
		($3::text[] IS NULL OR code=ANY($3) OR ($4 AND EXISTS (SELECT 1 FROM tushare_requested_subjects t WHERE t.code=e.code)))
		ORDER BY m.l DESC, code LIMIT 10`, text, s.excludeSynthetic, s.searchableCodes(), s.onDemandSubjects)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntity)
}

func (s *Store) RestrictCodes(codes []string) { s.allowedCodes = append([]string(nil), codes...) }

func (s *Store) searchableCodes() []string {
	if s.researchSubjects != nil {
		return s.researchSubjects
	}
	return s.allowedCodes
}

// UpsertSearchEntity inserts or updates a non-synthetic entity discovered via
// a real-time vendor search. It is a search-index-only write: it does not
// grant data permissions, establish data lineage, or add research records.
// Bypasses allowedCodes because this is a write path creating the row.
func (s *Store) UpsertSearchEntity(ctx context.Context, kind, code, name string, aliases []string) (EntityRow, error) {
	if kind == "" || code == "" || name == "" {
		return EntityRow{}, fmt.Errorf("dataservice: upsert search entity: kind, code and name required")
	}
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic,status)
			VALUES($1,$2,$3,false,'unknown')
			ON CONFLICT (code) DO UPDATE SET name=CASE WHEN entities.attrs->>'tushare_research_subject'='true' THEN entities.name ELSE excluded.name END
			WHERE NOT entities.synthetic AND entities.kind=excluded.kind
			RETURNING id`, kind, code, name).Scan(&id); err != nil {
			return err
		}
		for _, a := range append([]string{strings.ToLower(code), strings.ToLower(name)}, aliases...) {
			a = strings.TrimSpace(strings.ToLower(a))
			if a == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO entity_aliases VALUES($1,$2) ON CONFLICT DO NOTHING`, id, a); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return EntityRow{}, fmt.Errorf("dataservice: upsert search entity %s: %w", code, err)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE id=$1`, id)
	if err != nil {
		return EntityRow{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanEntity)
}
