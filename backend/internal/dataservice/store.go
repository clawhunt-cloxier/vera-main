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
	pool *pgxpool.Pool
	cat  *Catalog
}

func NewStore(pool *pgxpool.Pool, cat *Catalog) *Store { return &Store{pool: pool, cat: cat} }

func (s *Store) Catalog() *Catalog { return s.cat }

// Sync upserts a provider's entities, records and capabilities. Records are immutable: existing
// (dataset, record_key, version) rows are left untouched.
func (s *Store) Sync(ctx context.Context, p Provider) (int, error) {
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
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing,synthetic,basis) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET name=excluded.name, allow_model_processing=excluded.allow_model_processing, basis=excluded.basis`,
			p.ID(), p.Name(), kind, p.AllowModelProcessing(), p.Synthetic(), basis); err != nil {
			return err
		}
		for _, e := range ents {
			attrs, _ := json.Marshal(e.Attrs)
			var id uuid.UUID
			err := tx.QueryRow(ctx, `INSERT INTO entities(kind,code,name,exchange,status,is_financial,classification,synthetic,attrs)
				VALUES($1,$2,$3,NULLIF($4,''),$5,$6,NULLIF($7,''),$8,$9)
				ON CONFLICT (code) DO UPDATE SET name=excluded.name, status=excluded.status, classification=excluded.classification, attrs=excluded.attrs
				RETURNING id`, e.Kind, e.Code, e.Name, e.Exchange, e.Status, e.IsFinancial, e.Classification, p.Synthetic(), attrs).Scan(&id)
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
		return nil
	})
	if err != nil {
		return 0, err
	}
	rows := make([][]any, 0, len(recs))
	for _, r := range recs {
		var eid *uuid.UUID
		if r.EntityCode != "" {
			id, ok := ids[r.EntityCode]
			if !ok {
				return 0, fmt.Errorf("record %s references unknown entity %s", r.RecordKey, r.EntityCode)
			}
			eid = &id
		}
		lk := r.LogicalKey
		if lk == "" {
			lk = r.RecordKey
		}
		payload, _ := json.Marshal(r.Payload)
		rows = append(rows, []any{r.DatasetID, eid, r.RecordKey, lk, max(r.Version, 1), r.Period, r.PublishedAt, r.AvailableAt,
			p.ID(), r.Origin, r.SourceTier, nilIfEmpty(r.Basis), payload, r.Hash()})
	}
	n := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE records_in (LIKE records INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
			return err
		}
		cols := []string{"dataset_id", "entity_id", "record_key", "logical_key", "record_version", "period", "published_at",
			"available_at", "provider_id", "origin", "source_tier", "basis", "payload", "content_hash"}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"records_in"}, cols, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO records (`+strings.Join(cols, ",")+`) SELECT `+strings.Join(cols, ",")+
			` FROM records_in ON CONFLICT (dataset_id, record_key, record_version) DO NOTHING`)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
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
		return nil
	})
	return n, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// StoredRecord is a record as read back for research, with its evidence identity.
type StoredRecord struct {
	ID          uuid.UUID
	DatasetID   string
	EntityID    *uuid.UUID
	RecordKey   string
	Version     int
	Period      *time.Time
	PublishedAt *time.Time
	AvailableAt time.Time
	ProviderID  string
	Origin      string
	SourceTier  string
	Basis       string
	Payload     map[string]any
	ContentHash string
}

// Query selects the latest revision of each logical record available at asOf.
type Query struct {
	DatasetIDs []string
	EntityIDs  []uuid.UUID // empty = no entity filter
	From, To   *time.Time  // period range, inclusive
	AsOf       time.Time
}

const recordCols = `r.id, r.dataset_id, r.entity_id, r.record_key, r.record_version, r.period, r.published_at,
		r.available_at, r.provider_id, r.origin, r.source_tier, coalesce(r.basis,''), r.payload, r.content_hash`

func scanRecord(row pgx.CollectableRow) (StoredRecord, error) {
	var r StoredRecord
	err := row.Scan(&r.ID, &r.DatasetID, &r.EntityID, &r.RecordKey, &r.Version, &r.Period, &r.PublishedAt, &r.AvailableAt,
		&r.ProviderID, &r.Origin, &r.SourceTier, &r.Basis, &r.Payload, &r.ContentHash)
	return r, err
}

func (s *Store) Query(ctx context.Context, q Query) ([]StoredRecord, error) {
	sql := `SELECT DISTINCT ON (r.dataset_id, r.logical_key) ` + recordCols + `
		FROM records r JOIN providers p ON p.id = r.provider_id AND p.enabled
		WHERE r.dataset_id = ANY($1) AND r.available_at <= $2
		AND ($3::uuid[] IS NULL OR r.entity_id = ANY($3)) AND ($4::date IS NULL OR r.period >= $4) AND ($5::date IS NULL OR r.period <= $5)
		ORDER BY r.dataset_id, r.logical_key, r.record_version DESC`
	var ents []uuid.UUID
	if len(q.EntityIDs) > 0 {
		ents = q.EntityIDs
	}
	rows, err := s.pool.Query(ctx, sql, q.DatasetIDs, q.AsOf, ents, q.From, q.To)
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
func (s *Store) Coverage(ctx context.Context, entities []uuid.UUID, asOf time.Time) (map[uuid.UUID]map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT r.entity_id, r.dataset_id FROM records r JOIN providers p ON p.id=r.provider_id AND p.enabled
		WHERE r.entity_id = ANY($1) AND r.available_at <= $2`, entities, asOf)
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

// HasGlobal reports whether an entity-less dataset (calendar, index bars, connect turnover) has data as of asOf.
func (s *Store) HasGlobal(ctx context.Context, dataset string, asOf time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records r JOIN providers p ON p.id=r.provider_id AND p.enabled
		WHERE r.dataset_id=$1 AND r.entity_id IS NULL AND r.available_at <= $2)`, dataset, asOf).Scan(&ok)
	return ok, err
}

// Snapshot loads the frozen record set.
func (s *Store) Snapshot(ctx context.Context, hash string) (time.Time, []StoredRecord, error) {
	var asOf time.Time
	var ids []uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT as_of, record_ids FROM snapshots WHERE hash=$1`, hash).Scan(&asOf, &ids); err != nil {
		return asOf, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+recordCols+` FROM records r WHERE r.id = ANY($1)`, ids)
	if err != nil {
		return asOf, nil, err
	}
	out, err := pgx.CollectRows(rows, scanRecord)
	if err != nil {
		return asOf, nil, err
	}
	if len(out) != len(ids) {
		return asOf, nil, fmt.Errorf("snapshot %s: %d of %d records present", hash, len(out), len(ids))
	}
	sortRecords(out)
	return asOf, out, nil
}

// Freeze stores the exact record set used by a task and returns its hash.
func (s *Store) Freeze(ctx context.Context, asOf time.Time, ids []uuid.UUID) (string, error) {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	sorted = slices.Compact(sorted)
	h := sha256.New()
	h.Write([]byte(asOf.UTC().Format(time.RFC3339)))
	for _, id := range sorted {
		h.Write(id[:])
	}
	hash := hex.EncodeToString(h.Sum(nil))
	_, err := s.pool.Exec(ctx, `INSERT INTO snapshots(hash,as_of,record_ids) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, hash, asOf, sorted)
	return hash, err
}

// Get loads specific records (evidence lookup).
func (s *Store) Get(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]StoredRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recordCols+` FROM records r WHERE r.id = ANY($1)`, ids)
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
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE id = ANY($1)`, ids)
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
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE code=$1`, code)
	if err != nil {
		return EntityRow{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanEntity)
}

func (s *Store) EntitiesByKind(ctx context.Context, kind string) ([]EntityRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities WHERE kind=$1 ORDER BY code`, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntity)
}

// Search matches names, codes and aliases (trigram similarity plus substring), best first.
func (s *Store) Search(ctx context.Context, q, kind string, limit int) ([]EntityRow, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities e WHERE ($2 = '' OR kind = $2) AND EXISTS (
		SELECT 1 FROM entity_aliases a WHERE a.entity_id = e.id AND (a.alias LIKE '%' || $1 || '%' OR similarity(a.alias, $1) > 0.3))
		ORDER BY (SELECT max(CASE WHEN a.alias = $1 THEN 2 ELSE similarity(a.alias, $1) END) FROM entity_aliases a WHERE a.entity_id = e.id) DESC, code
		LIMIT $3`, q, kind, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntity)
}

// Mentioned returns entities whose alias occurs verbatim in free text (rule-based plan parsing). Longest alias first.
func (s *Store) Mentioned(ctx context.Context, text string) ([]EntityRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entityCols+` FROM entities e JOIN LATERAL (
		SELECT max(length(alias)) AS l FROM entity_aliases a WHERE a.entity_id = e.id AND length(a.alias) >= 2 AND strpos(lower($1), a.alias) > 0) m ON m.l IS NOT NULL
		ORDER BY m.l DESC, code LIMIT 10`, text)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntity)
}
