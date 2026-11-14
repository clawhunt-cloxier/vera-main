package model

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// ReleaseArgs is the periodic job that releases stale UNKNOWN reservations.
type ReleaseArgs struct{}

func (ReleaseArgs) Kind() string { return "model.release_unknown" }

type releaseWorker struct {
	river.WorkerDefaults[ReleaseArgs]
	g *Gateway
}

func (w *releaseWorker) Work(ctx context.Context, _ *river.Job[ReleaseArgs]) error {
	_, err := w.g.ReleaseUnknown(ctx, w.g.now().Add(-UnknownReleaseAfter))
	return err
}

// Register adds the release worker and an hourly schedule to a River client being built.
func (g *Gateway) Register(ws *river.Workers, cfg *river.Config) {
	river.AddWorker(ws, &releaseWorker{g: g})
	cfg.PeriodicJobs = append(cfg.PeriodicJobs, river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return ReleaseArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}))
}
