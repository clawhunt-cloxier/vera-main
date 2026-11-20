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
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.RequireResearchUseTx(ctx, tx); err != nil {
			return err
		}
		return fn()
	})
}

// RequireResearchUseTx lets multi-source tasks hold all rights in the same
// transaction rather than nesting acquisitions from the connection pool.
func (s *Store) RequireResearchUseTx(ctx context.Context, tx pgx.Tx) error {
	if s.providerID == "" || !s.allowResearch {
		return &ProviderError{Class: ErrAuthFailed, Err: errors.New("部署未启用收盘研究用途")}
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT enabled AND allow_model_processing AND kind='vendor' AND NOT synthetic FROM providers WHERE id=$1 FOR SHARE`, s.providerID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !allowed {
		return &ProviderError{Class: ErrAuthFailed, Err: errors.New("研究数据源当前未启用或未授权")}
	}
	return err
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
