package dataservice

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type subjectQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ResearchSubjectRegistered checks both the configured prewarm list and, on combined
// deployments, committed on-demand publications. Read the shared database so callers
// also see subjects prepared by another process.
func (s *Store) ResearchSubjectRegistered(ctx context.Context, code string) (bool, error) {
	return s.researchSubject(ctx, s.pool, code, false)
}

// ResearchSubjectPrepared admits prewarmed subjects or a dynamic batch collected in
// the last 24 hours. Normal plan freshness and dataset checks still apply.
func (s *Store) ResearchSubjectPrepared(ctx context.Context, code string) (bool, error) {
	return s.researchSubject(ctx, s.pool, code, true)
}

func (s *Store) researchSubject(ctx context.Context, q subjectQuery, code string, recent bool) (bool, error) {
	if s.RegisteredResearchSubject(code) {
		return true, nil
	}
	if !s.onDemandSubjects || s.providerID != "tushare" {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tushare_requested_subjects WHERE code=$1
		AND (NOT $2::bool OR published_at > clock_timestamp() - interval '24 hours'))`, code, recent).Scan(&ok)
	return ok, err
}
