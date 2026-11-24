package dataservice

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SyncObserved serializes collection and publication per provider. Different
// research subjects can fetch the same indexes, trading calendar and peers.
func (s *Store) SyncObserved(ctx context.Context, provider string, collect func(context.Context) (Provider, error)) (int, error) {
	return s.syncObserved(ctx, provider, "", collect)
}

// PrepareRequestedSubject performs an atomic cold publish, serializing with all other
// TuShare publications. A waiting process rechecks the successful publication after acquiring the lock.
func (s *Store) PrepareRequestedSubject(ctx context.Context, code string, collect func(context.Context) (Provider, error)) (int, error) {
	if !s.onDemandSubjects || code == "" {
		return 0, errors.New("TuShare 按需研究未启用")
	}
	return s.syncObserved(ctx, "tushare", code, collect)
}

func (s *Store) syncObserved(ctx context.Context, provider, requestedCode string, collect func(context.Context) (Provider, error)) (int, error) {
	if !s.excludeSynthetic || provider == "" || (s.providerID != "" && provider != s.providerID) {
		return 0, errors.New("observation requires its configured vendor store")
	}
	conn, err := s.acquireObservationSession(ctx, provider)
	if err != nil {
		return 0, err
	}
	defer closeObservationSession(conn)
	if requestedCode != "" {
		// Do not hold a provider read lock over slow network work. Rights are
		// rechecked in the actual publication transaction below.
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { return s.RequireResearchUseTx(ctx, tx) }); err != nil {
			return 0, err
		}
		prepared, err := s.researchSubjectPrepared(ctx, conn, requestedCode)
		if err != nil || prepared {
			return 0, err
		}
	}
	batch, err := collect(ctx)
	if err != nil {
		return 0, err
	}
	if batch == nil || batch.ID() != provider || batch.Synthetic() {
		return 0, errors.New("observation does not belong to the locked vendor")
	}
	// Use the session that owns the lock for the write. A collector whose
	// session died during a fetch cannot publish after another process takes over.
	return s.sync(ctx, conn, batch, true, requestedCode)
}

func (s *Store) acquireObservationSession(ctx context.Context, provider string) (*pgx.Conn, error) {
	for {
		c, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		err = c.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 508418))`, provider).Scan(&locked)
		if err != nil {
			// The query may have taken the lock before the response was lost.
			closeObservationSession(c.Hijack())
			return nil, err
		}
		if locked {
			// The collector needs the pool for per-request rate limiting. Only
			// the lock owner keeps a dedicated connection, even with MaxConns=1.
			return c.Hijack(), nil
		}
		c.Release()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func closeObservationSession(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = conn.Close(ctx) // Physical close releases all session advisory locks.
}

type observedIdentity struct{ dataset, logical string }
type observedVersion struct {
	key, hash, basis, origin, tier, quality string
	mapping, version                        int
	published, period                       *time.Time
}

func sameInstant(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// The caller owns the provider's publication lock. Source-defined financial
// versions are ordered by disclosure, independently of the order of fetching.
func prepareObserved(ctx context.Context, tx pgx.Tx, provider string, input []Record) ([]Record, error) {
	ordered := slices.Clone(input)
	var keys []string
	for i := range ordered {
		r := &ordered[i]
		if r.LogicalKey == "" {
			r.LogicalKey = r.RecordKey
		}
		if r.Period != nil {
			year, month, day := r.Period.Date()
			date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
			r.Period = &date
		}
		if r.PublishedAt != nil {
			// Conservative rounding to the next representable database instant.
			at := r.PublishedAt.UTC().Add(time.Microsecond - time.Nanosecond).Truncate(time.Microsecond)
			r.PublishedAt = &at
		}
		if r.RecordKey == "" || r.RawRef == "" || len(r.RawSHA256) != 64 || r.RawLocator == "" || r.MappingVersion < 1 {
			return nil, fmt.Errorf("%s/%s: incomplete observed lineage", r.DatasetID, r.RecordKey)
		}
		if _, err := hex.DecodeString(r.RawSHA256); err != nil {
			return nil, errors.New("invalid observation hash")
		}
		keys = append(keys, r.LogicalKey)
	}
	rows, err := tx.Query(ctx, `SELECT dataset_id,logical_key,record_key,content_hash,coalesce(basis,''),origin,source_tier,quality_state,coalesce(mapping_version,0),record_version,published_at,period FROM records WHERE provider_id=$1 AND logical_key=ANY($2) ORDER BY record_version`, provider, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := map[observedIdentity][]observedVersion{}
	for rows.Next() {
		var k observedIdentity
		var v observedVersion
		if err := rows.Scan(&k.dataset, &k.logical, &v.key, &v.hash, &v.basis, &v.origin, &v.tier, &v.quality, &v.mapping, &v.version, &v.published, &v.period); err != nil {
			return nil, err
		}
		history[k] = append(history[k], v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return nil, err
	}
	slices.SortStableFunc(ordered, func(a, b Record) int {
		if n := strings.Compare(a.DatasetID, b.DatasetID); n != 0 {
			return n
		}
		if n := strings.Compare(a.LogicalKey, b.LogicalKey); n != 0 {
			return n
		}
		if a.PublishedAt == nil && b.PublishedAt != nil {
			return -1
		}
		if a.PublishedAt != nil && b.PublishedAt == nil {
			return 1
		}
		if a.PublishedAt != nil && b.PublishedAt != nil {
			if n := a.PublishedAt.Compare(*b.PublishedAt); n != 0 {
				return n
			}
		}
		return strings.Compare(a.RecordKey, b.RecordKey)
	})
	result := make([]Record, 0, len(ordered))
	seen := map[observedIdentity]string{}
	for _, r := range ordered {
		k := observedIdentity{r.DatasetID, r.LogicalKey}
		if r.QualityState == "" {
			r.QualityState = "PASS"
		}
		v := observedVersion{key: r.RecordKey, hash: r.Hash(), basis: r.Basis, origin: r.Origin, tier: r.SourceTier, quality: r.QualityState, mapping: r.MappingVersion, published: r.PublishedAt, period: r.Period}
		unique := observedIdentity{r.DatasetID, r.RecordKey}
		if previous, exists := seen[unique]; exists && previous != v.hash {
			return nil, fmt.Errorf("%s/%s: conflicting vendor rows in one observation", r.DatasetID, r.RecordKey)
		}
		seen[unique] = v.hash
		prior := history[k]
		if len(prior) > 0 {
			latest := prior[len(prior)-1]
			// A peer collection may have an older stock-capital session than the
			// subject's own batch. The v2 join first corrects any v1 period.
			if provider == "tushare" && r.DatasetID == "ref.security" && latest.mapping >= 2 &&
				(v.mapping < latest.mapping || (v.period != nil && latest.period != nil && v.period.Before(*latest.period))) {
				continue
			}
			if r.PublishedAt != nil && latest.published != nil && r.PublishedAt.Before(*latest.published) {
				// Do not let an earlier re-fetched filing replace a known later revision.
				continue
			}
			if v.key == latest.key && v.hash == latest.hash && v.mapping == latest.mapping && v.quality == latest.quality && v.origin == latest.origin && v.tier == latest.tier && v.basis == latest.basis && sameInstant(v.published, latest.published) && sameInstant(v.period, latest.period) {
				continue
			}
			if r.DatasetID == "fin.statement" && v.key == latest.key && v.mapping == latest.mapping {
				return nil, fmt.Errorf("%s/%s: changed financial value without a distinct filing", r.DatasetID, r.RecordKey)
			}
			v.version = latest.version + 1
		} else {
			v.version = 1
		}
		r.Version = v.version
		if at.After(r.AvailableAt) {
			r.AvailableAt = at
		}
		history[k] = append(prior, v)
		result = append(result, r)
	}
	return result, nil
}
