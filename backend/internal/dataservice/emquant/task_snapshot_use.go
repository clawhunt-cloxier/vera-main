package emquant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// WithTaskSnapshotQuote reauthorizes the immutable snapshot for a current use.
// Snapshot revision is read from the bound manifest, never the task's mutable
// progress revision. API callers must establish workspace visibility first.
// The grant locks remain held until fn returns; callers must not retain the
// returned view as authorization for a later model send or display.
func (r *Runtime) WithTaskSnapshotQuote(ctx context.Context, workspace, task uuid.UUID, use dataservice.SourceUse, fn func(QuoteView) error) error {
	if r == nil || r.database == nil || workspace == uuid.Nil || task == uuid.Nil || fn == nil {
		return errors.New("emquant: invalid snapshot use")
	}
	return pgx.BeginTxFunc(ctx, r.database, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		view, err := r.ReadTaskSnapshotQuote(ctx, tx, workspace, task, use)
		if err != nil {
			return err
		}
		return fn(view)
	})
}

// ReadTaskSnapshotQuote allows a caller to check additional source grants in
// the same repeatable-read transaction as the task's frozen quote.
func (r *Runtime) ReadTaskSnapshotQuote(ctx context.Context, tx pgx.Tx, workspace, task uuid.UUID, use dataservice.SourceUse) (QuoteView, error) {
	var epoch, revision int64
	var hash string
	if err := tx.QueryRow(ctx, `SELECT t.epoch,m.task_revision,t.snapshot_manifest_hash
			FROM research_tasks t
			JOIN task_snapshot_manifests m ON m.workspace_id=t.workspace_id AND m.task_id=t.id AND m.epoch=t.epoch AND m.content_hash=t.snapshot_manifest_hash
			WHERE t.workspace_id=$1 AND t.id=$2`, workspace, task).Scan(&epoch, &revision, &hash); err != nil {
		return QuoteView{}, fmt.Errorf("emquant: bound snapshot unavailable: %w", err)
	}
	return r.ReadBoundSnapshotQuote(ctx, tx, workspace, task, epoch, revision, hash, use)
}
