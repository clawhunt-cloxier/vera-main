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
	return s.researchSubjectRegistered(ctx, s.pool, code)
}

func (s *Store) researchSubjectRegistered(ctx context.Context, q subjectQuery, code string) (bool, error) {
	if s.RegisteredResearchSubject(code) {
		return true, nil
	}
	if !s.onDemandSubjects || s.providerID != "tushare" {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tushare_requested_subjects WHERE code=$1)`, code).Scan(&ok)
	return ok, err
}

// ResearchSubjectPrepared checks for a committed batch in the last 24 hours.
// Each plan independently checks the trading dates and datasets it needs.
func (s *Store) ResearchSubjectPrepared(ctx context.Context, code string) (bool, error) {
	return s.researchSubjectPrepared(ctx, s.pool, code)
}

func (s *Store) researchSubjectPrepared(ctx context.Context, q subjectQuery, code string) (bool, error) {
	configured := s.RegisteredResearchSubject(code)
	if s.providerID != "tushare" || (!configured && !s.onDemandSubjects) {
		return false, nil
	}
	// Ordinary prewarm publishes daily bars only for its primary stock. Its per-stock
	// capability timestamp advances on every commit, even if the market was closed
	// and all record versions were reused. A peer or Choice directory entry can
	// carry the configured stock's identity before its own batch has committed.
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM tushare_requested_subjects WHERE $2 AND code=$1
			AND published_at > statement_timestamp() - interval '24 hours'
	) OR EXISTS (
		SELECT 1 FROM entities e JOIN capabilities c ON c.entity_id=e.id
		JOIN records r ON r.entity_id=c.entity_id AND r.dataset_id=c.dataset_id AND r.provider_id=c.provider_id
		WHERE $3 AND e.code=$1 AND e.kind='stock' AND NOT e.synthetic AND e.attrs->>'tushare_research_subject'='true'
			AND c.provider_id='tushare' AND c.dataset_id='mkt.daily_bar'
			AND c.updated_at > statement_timestamp() - interval '24 hours'
			AND r.quality_state IN ('PASS','WARN') AND r.available_at <= statement_timestamp()
			AND (r.published_at IS NULL OR r.published_at <= statement_timestamp())
			AND r.period <= (statement_timestamp() AT TIME ZONE 'Asia/Shanghai')::date
			AND r.raw_ref IS NOT NULL AND r.raw_sha256 IS NOT NULL AND r.raw_locator IS NOT NULL AND r.mapping_version > 0
	)`, code, s.onDemandSubjects, configured).Scan(&ok)
	return ok, err
}
