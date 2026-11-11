package watchlist

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type snapshotWorker struct {
	river.WorkerDefaults[SnapshotArgs]
	s *Service
}

func (w *snapshotWorker) Work(ctx context.Context, _ *river.Job[SnapshotArgs]) error {
	n, err := w.s.RefreshAll(ctx)
	if err == nil {
		slog.InfoContext(ctx, "watchlist snapshots refreshed", "items", n)
	}
	return err
}

// Register adds the worker and an hourly periodic job (snapshots are idempotent per trade date).
func (s *Service) Register(ws *river.Workers, cfg *river.Config) {
	river.AddWorker(ws, &snapshotWorker{s: s})
	cfg.PeriodicJobs = append(cfg.PeriodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return SnapshotArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}

var _ = pgx.ErrNoRows
