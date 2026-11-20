package dataservice

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// WithResearchUse rechecks the configured vendor's research permission and
// holds the grant through the protected action. Other providers use their own
// existing permission paths (including the Choice per-snapshot policies).
func (s *Store) WithResearchUse(ctx context.Context, fn func() error) error {
	if s.providerID == "" {
		return fn()
	}
	if !s.allowResearch {
		return &ProviderError{Class: ErrAuthFailed, Err: errors.New("部署未启用 TuShare 研究用途")}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT enabled AND allow_model_processing AND kind='vendor' AND NOT synthetic FROM providers WHERE id=$1 FOR SHARE`, s.providerID).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !allowed {
			return &ProviderError{Class: ErrAuthFailed, Err: errors.New("研究数据源当前未启用或未授权")}
		}
		if err != nil {
			return err
		}
		return fn()
	})
}

// PublicationCutoff serializes a new plan's data clock with observation commits.
// Publications completed before the barrier are visible, and later ones receive
// a greater availability timestamp under the exclusive lock.
func (s *Store) PublicationCutoff(ctx context.Context) (time.Time, error) {
	var at time.Time
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,508417))`, s.providerID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at)
	})
	return at, err
}
