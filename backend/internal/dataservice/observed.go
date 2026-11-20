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

type observedIdentity struct{ dataset, logical string }
type observedVersion struct {
	key, hash, basis, origin, tier, quality string
	mapping, version                        int
	published                               *time.Time
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
	rows, err := tx.Query(ctx, `SELECT dataset_id,logical_key,record_key,content_hash,coalesce(basis,''),origin,source_tier,quality_state,coalesce(mapping_version,0),record_version,published_at FROM records WHERE provider_id=$1 AND logical_key=ANY($2) ORDER BY record_version`, provider, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := map[observedIdentity][]observedVersion{}
	for rows.Next() {
		var k observedIdentity
		var v observedVersion
		if err := rows.Scan(&k.dataset, &k.logical, &v.key, &v.hash, &v.basis, &v.origin, &v.tier, &v.quality, &v.mapping, &v.version, &v.published); err != nil {
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
		v := observedVersion{key: r.RecordKey, hash: r.Hash(), basis: r.Basis, origin: r.Origin, tier: r.SourceTier, quality: r.QualityState, mapping: r.MappingVersion, published: r.PublishedAt}
		unique := observedIdentity{r.DatasetID, r.RecordKey}
		if previous, exists := seen[unique]; exists && previous != v.hash {
			return nil, fmt.Errorf("%s/%s: conflicting vendor rows in one observation", r.DatasetID, r.RecordKey)
		}
		seen[unique] = v.hash
		prior := history[k]
		if len(prior) > 0 {
			latest := prior[len(prior)-1]
			if r.PublishedAt != nil && latest.published != nil && r.PublishedAt.Before(*latest.published) {
				// Do not let an earlier re-fetched filing replace a known later revision.
				continue
			}
			if v.key == latest.key && v.hash == latest.hash && v.mapping == latest.mapping && v.quality == latest.quality && v.origin == latest.origin && v.tier == latest.tier && v.basis == latest.basis && sameInstant(v.published, latest.published) {
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
