package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

type demoTuShareSync struct {
	pool     *pgxpool.Pool
	subjects []string
	cancel   context.CancelFunc
	done     chan struct{}
}

func startDemoTuShareSync(ctx context.Context, pool *pgxpool.Pool, subjects []string, collect func(context.Context) error) *demoTuShareSync {
	ctx, cancel := context.WithCancel(ctx)
	s := &demoTuShareSync{pool: pool, subjects: subjects, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		if err := collect(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "TuShare demo sync failed", "error", err)
		}
	}()
	return s
}

func (s *demoTuShareSync) stop() {
	s.cancel()
	<-s.done
}

// Each subject's archived closing bars are published in the same transaction
// as its collected observation. A peer has valuation and filing rows only.
func (s *demoTuShareSync) ready(ctx context.Context) (bool, error) {
	var ready bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM unnest($1::text[]) AS target(code)
		WHERE NOT EXISTS (
			SELECT 1 FROM entities e JOIN records r ON r.entity_id=e.id
			JOIN providers p ON p.id=r.provider_id
			WHERE e.code=target.code AND e.kind='stock' AND NOT e.synthetic
			AND p.id='tushare' AND p.enabled AND p.kind='vendor' AND NOT p.synthetic
			AND r.dataset_id='mkt.daily_bar' AND r.available_at<=now()
			AND r.quality_state IN ('PASS','WARN') AND r.raw_ref IS NOT NULL
			AND r.raw_sha256 IS NOT NULL AND r.raw_locator IS NOT NULL
		))`, s.subjects).Scan(&ready)
	return ready, err
}

func (s *demoTuShareSync) readiness(next http.Handler) http.Handler {
	type reply struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.Path != "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		reason := "tushare_initial_sync"
		// Check completion before reading published rows so a just-committed
		// final subject cannot turn the previous state into a spurious failure.
		select {
		case <-s.done:
			reason = "tushare_sync_failed"
		default:
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		ready, err := s.ready(ctx)
		if err != nil {
			reason = "database_unavailable"
		} else if ready {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, r, http.StatusServiceUnavailable, reply{"not_ready", reason})
	})
}
