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
	DatasetID string
	EntityIDs []uuid.UUID // empty = no entity filter
	From, To  *time.Time  // period range, inclusive
	AsOf      time.Time
}

func (s *Store) Query(ctx context.Context, q Query) ([]StoredRecord, error) {
	sql := `SELECT DISTINCT ON (r.logical_key) r.id, r.dataset_id, r.entity_id, r.record_key, r.record_version, r.period, r.published_at,
		r.available_at, r.provider_id, r.origin, r.source_tier, coalesce(r.basis,''), r.payload, r.content_hash
		FROM records r JOIN providers p ON p.id = r.provider_id AND p.enabled
		WHERE r.dataset_id = $1 AND r.available_at <= $2
		AND ($3::uuid[] IS NULL OR r.entity_id = ANY($3)) AND ($4::date IS NULL OR r.period >= $4) AND ($5::date IS NULL OR r.period <= $5)
		ORDER BY r.logical_key, r.record_version DESC`
	var ents []uuid.UUID
	if len(q.EntityIDs) > 0 {
		ents = q.EntityIDs
	}
	rows, err := s.pool.Query(ctx, sql, q.DatasetID, q.AsOf, ents, q.From, q.To)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StoredRecord, error) {
		var r StoredRecord
		err := row.Scan(&r.ID, &r.DatasetID, &r.EntityID, &r.RecordKey, &r.Version, &r.Period, &r.PublishedAt, &r.AvailableAt,
			&r.ProviderID, &r.Origin, &r.SourceTier, &r.Basis, &r.Payload, &r.ContentHash)
		return r, err
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b StoredRecord) int {
		if a.Period != nil && b.Period != nil && !a.Period.Equal(*b.Period) {
			return a.Period.Compare(*b.Period)
		}
		return strings.Compare(a.RecordKey, b.RecordKey)
	})
	return out, nil
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
	rows, err := s.pool.Query(ctx, `SELECT id, dataset_id, entity_id, record_key, record_version, period, published_at, available_at,
		provider_id, origin, source_tier, coalesce(basis,''), payload, content_hash FROM records WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StoredRecord, error) {
		var r StoredRecord
		err := row.Scan(&r.ID, &r.DatasetID, &r.EntityID, &r.RecordKey, &r.Version, &r.Period, &r.PublishedAt, &r.AvailableAt,
			&r.ProviderID, &r.Origin, &r.SourceTier, &r.Basis, &r.Payload, &r.ContentHash)
		return r, err
	})
	out := map[uuid.UUID]StoredRecord{}
	for _, r := range list {
		out[r.ID] = r
	}
	return out, err
}
