package emquant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type SnapshotDataset struct {
	DatasetID string `json:"dataset_id"`
	Required  bool   `json:"required"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

type LatestSnapshot struct {
	Hash     string
	Cutoff   time.Time
	Datasets []SnapshotDataset
}

type taskSnapshotDocument struct {
	Version      int               `json:"version"`
	WorkspaceID  uuid.UUID         `json:"workspace_id"`
	TaskID       uuid.UUID         `json:"task_id"`
	Epoch        int64             `json:"epoch"`
	TaskRevision int64             `json:"task_revision"`
	PlanHash     string            `json:"plan_hash"`
	Cutoff       time.Time         `json:"cutoff"`
	QuoteHash    string            `json:"quote_hash"`
	Datasets     []SnapshotDataset `json:"datasets"`
}

func taskSnapshotDigest(body []byte) string {
	h := sha256.Sum256(append([]byte("vera.task-snapshot.v1\x00"), body...))
	return hex.EncodeToString(h[:])
}

type snapshotQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// boundSnapshot validates the exact persisted task/manifest/component link.
// It returns metadata only; display and model access need separate current-use
// authorization, and a missing or corrupted manifest is never reconstructed.
func boundSnapshot(ctx context.Context, q snapshotQuery, workspace, task uuid.UUID, epoch, revision int64) (LatestSnapshot, string, bool, error) {
	var hash, planHash, mode, status string
	var cutoff *time.Time
	var storedHash *string
	var body, quoteBody []byte
	var quoteHash *string
	var manifestCutoff, quoteCutoff *time.Time
	var manifestPlan, quotePlan *string
	var manifestRevision, quoteRevision *int64
	err := q.QueryRow(ctx, `SELECT t.snapshot_mode,t.status,p.plan_hash,t.snapshot_as_of,t.snapshot_manifest_hash,
		m.manifest,m.quote_hash,m.cutoff,c.manifest,m.plan_hash,m.task_revision,c.plan_hash,c.task_revision,c.cutoff
		FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
		LEFT JOIN task_snapshot_manifests m ON m.workspace_id=t.workspace_id AND m.task_id=t.id AND m.epoch=t.epoch AND m.content_hash=t.snapshot_manifest_hash
		LEFT JOIN quote_task_components c ON c.workspace_id=m.workspace_id AND c.task_id=m.task_id AND c.epoch=m.epoch AND c.content_hash=m.quote_hash
		WHERE t.workspace_id=$1 AND t.id=$2 AND t.epoch=$3 AND p.consumed_task_id=t.id AND p.snapshot_mode=t.snapshot_mode`, workspace, task, epoch).
		Scan(&mode, &status, &planHash, &cutoff, &storedHash, &body, &quoteHash, &manifestCutoff, &quoteBody, &manifestPlan, &manifestRevision, &quotePlan, &quoteRevision, &quoteCutoff)
	if err != nil {
		return LatestSnapshot{}, "", false, fmt.Errorf("emquant: snapshot task unavailable: %w", err)
	}
	if mode != "collect_latest" || status == "CANCELLED" || status == "CANCEL_REQUESTED" {
		return LatestSnapshot{}, "", false, errors.New("emquant: task cannot use latest snapshot")
	}
	if cutoff == nil && storedHash == nil {
		return LatestSnapshot{}, "", false, nil
	}
	if cutoff == nil || storedHash == nil || quoteHash == nil || manifestCutoff == nil || quoteCutoff == nil || manifestPlan == nil || quotePlan == nil || manifestRevision == nil || quoteRevision == nil || len(body) == 0 || len(body) > 1<<20 || len(quoteBody) == 0 || len(quoteBody) > 1<<20 {
		return LatestSnapshot{}, "", false, errors.New("emquant: bound snapshot is missing")
	}
	hash = *storedHash
	if taskSnapshotDigest(body) != hash || quoteComponentDigest(quoteBody) != *quoteHash || !cutoff.Equal(*manifestCutoff) || !cutoff.Equal(*quoteCutoff) || *manifestPlan != planHash || *quotePlan != planHash || *manifestRevision != revision || *quoteRevision != revision {
		return LatestSnapshot{}, "", false, errors.New("emquant: bound snapshot hash mismatch")
	}
	var doc taskSnapshotDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return LatestSnapshot{}, "", false, errors.New("emquant: invalid bound snapshot")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return LatestSnapshot{}, "", false, errors.New("emquant: trailing bound snapshot data")
	}
	if doc.Version != 1 || doc.WorkspaceID != workspace || doc.TaskID != task || doc.Epoch != epoch || doc.TaskRevision != revision || doc.PlanHash != planHash || doc.QuoteHash != *quoteHash || !doc.Cutoff.Equal(*cutoff) {
		return LatestSnapshot{}, "", false, errors.New("emquant: bound snapshot scope mismatch")
	}
	return LatestSnapshot{Hash: hash, Cutoff: *cutoff, Datasets: doc.Datasets}, *quoteHash, true, nil
}

// FreezeLatestSnapshot atomically binds one fixed-view manifest to a confirmed
// 0.4 task. Only quote sources currently have an eligible publication path;
// every other requested dataset is explicitly unavailable, never substituted.
// Confirmation must have persisted the exact requirements and quote intent.
// The existing application does not yet create those rows or consume this
// manifest. A caller must abstain when a required dataset is unavailable.
func (r *Runtime) FreezeLatestSnapshot(ctx context.Context, cat *dataservice.Catalog, workspace, task uuid.UUID, epoch, revision int64) (LatestSnapshot, error) {
	if r == nil || r.database == nil || r.store == nil || cat == nil || workspace == uuid.Nil || task == uuid.Nil || epoch < 1 || revision < 1 {
		return LatestSnapshot{}, errors.New("emquant: invalid latest snapshot request")
	}
	result, err := r.freezeLatestSnapshot(ctx, cat, workspace, task, epoch, revision)
	if err == nil {
		return result, nil
	}
	// A concurrent writer or an unknown commit result may have bound the
	// task. Return only that exact verified winner; never refreeze in this call.
	if winner, _, ok, checkErr := boundSnapshot(ctx, r.database, workspace, task, epoch, revision); checkErr == nil && ok {
		return winner, nil
	}
	return LatestSnapshot{}, err
}

func (r *Runtime) freezeLatestSnapshot(ctx context.Context, cat *dataservice.Catalog, workspace, task uuid.UUID, epoch, revision int64) (LatestSnapshot, error) {
	tx, err := r.database.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return LatestSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	existing, _, found, err := boundSnapshot(ctx, tx, workspace, task, epoch, revision)
	if err != nil {
		return LatestSnapshot{}, err
	}
	if found {
		return existing, nil
	}
	boundary, err := BeginQuoteBoundary(ctx, tx)
	if err != nil {
		return LatestSnapshot{}, err
	}
	planHash, err := checkQuoteTask(ctx, tx, workspace, task, epoch, revision, false)
	if err != nil {
		return LatestSnapshot{}, err
	}
	var datasets, required []string
	if err := tx.QueryRow(ctx, `SELECT dataset_ids,required_ids FROM task_snapshot_requirements WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 FOR SHARE`, workspace, task, epoch).Scan(&datasets, &required); err != nil {
		return LatestSnapshot{}, fmt.Errorf("emquant: confirmed snapshot requirements unavailable: %w", err)
	}
	slices.Sort(datasets)
	slices.Sort(required)
	if len(datasets) == 0 || len(datasets) > 128 || len(required) == 0 || len(slices.Compact(slices.Clone(datasets))) != len(datasets) || len(slices.Compact(slices.Clone(required))) != len(required) || !slices.Contains(datasets, "mkt.intraday_quote") {
		return LatestSnapshot{}, errors.New("emquant: invalid confirmed snapshot requirements")
	}
	for _, id := range required {
		if !slices.Contains(datasets, id) {
			return LatestSnapshot{}, errors.New("emquant: required dataset is not requested")
		}
	}
	for _, id := range datasets {
		if _, ok := cat.Dataset(id); !ok {
			return LatestSnapshot{}, errors.New("emquant: unknown confirmed dataset")
		}
	}
	quote, err := r.FreezeQuoteComponent(ctx, tx, boundary, workspace, task, epoch, revision)
	if err != nil {
		return LatestSnapshot{}, err
	}
	var quoteBody []byte
	if err := tx.QueryRow(ctx, `SELECT manifest FROM quote_task_components WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 AND content_hash=$4`, workspace, task, epoch, quote.ContentHash).Scan(&quoteBody); err != nil {
		return LatestSnapshot{}, err
	}
	var quoteDoc quoteComponentDocument
	if quoteComponentDigest(quoteBody) != quote.ContentHash || json.Unmarshal(quoteBody, &quoteDoc) != nil {
		return LatestSnapshot{}, errors.New("emquant: invalid quote component")
	}
	if quoteDoc.View.Requirement == QuoteRealtimeTrade && !slices.Contains(required, "mkt.intraday_quote") {
		return LatestSnapshot{}, errors.New("emquant: realtime quote must be required")
	}
	items := make([]SnapshotDataset, 0, len(datasets))
	for _, id := range datasets {
		item := SnapshotDataset{DatasetID: id, Required: slices.Contains(required, id), State: "unavailable", Reason: "dataset_not_integrated"}
		if id == "mkt.intraday_quote" {
			item.Reason = "quote_unavailable"
			if len(quoteDoc.View.Unavailable) == 0 {
				item.State, item.Reason = "available", ""
			}
		}
		items = append(items, item)
	}
	doc := taskSnapshotDocument{Version: 1, WorkspaceID: workspace, TaskID: task, Epoch: epoch, TaskRevision: revision, PlanHash: planHash, Cutoff: boundary.Cutoff(), QuoteHash: quote.ContentHash, Datasets: items}
	body, err := json.Marshal(doc)
	if err != nil || len(body) > 1<<20 {
		return LatestSnapshot{}, errors.New("emquant: invalid snapshot manifest")
	}
	hash := taskSnapshotDigest(body)
	if _, err := tx.Exec(ctx, `INSERT INTO task_snapshot_manifests(workspace_id,task_id,epoch,task_revision,plan_hash,cutoff,content_hash,quote_hash,manifest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, workspace, task, epoch, revision, planHash, doc.Cutoff, hash, quote.ContentHash, body); err != nil {
		return LatestSnapshot{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE research_tasks SET snapshot_as_of=$5,snapshot_manifest_hash=$6 WHERE workspace_id=$1 AND id=$2 AND epoch=$3 AND revision=$4 AND status='COLLECTING' AND snapshot_mode='collect_latest' AND snapshot_as_of IS NULL AND snapshot_manifest_hash IS NULL AND deadline>clock_timestamp()`, workspace, task, epoch, revision, doc.Cutoff, hash)
	if err != nil || tag.RowsAffected() != 1 {
		return LatestSnapshot{}, errors.New("emquant: snapshot task changed before binding")
	}
	if err := tx.Commit(ctx); err != nil {
		return LatestSnapshot{}, err
	}
	return LatestSnapshot{Hash: hash, Cutoff: doc.Cutoff, Datasets: items}, nil
}

// AuthorizeBoundSnapshotQuote verifies the full task binding before checking
// current rights for the exact frozen quote component in the same transaction.
func (r *Runtime) AuthorizeBoundSnapshotQuote(ctx context.Context, tx pgx.Tx, workspace, task uuid.UUID, epoch, revision int64, hash string, use dataservice.SourceUse) error {
	if r == nil || tx == nil {
		return errors.New("emquant: missing snapshot authorization transaction")
	}
	snapshot, quoteHash, ok, err := boundSnapshot(ctx, tx, workspace, task, epoch, revision)
	if err != nil {
		return err
	}
	if !ok || snapshot.Hash != hash {
		return errors.New("emquant: quote does not belong to the bound snapshot")
	}
	quoteAvailable := false
	for _, dataset := range snapshot.Datasets {
		if dataset.DatasetID == "mkt.intraday_quote" && dataset.State == "available" {
			quoteAvailable = true
		}
	}
	if !quoteAvailable {
		return errors.New("emquant: snapshot quote is unavailable")
	}
	if use == dataservice.SourceModel {
		for _, dataset := range snapshot.Datasets {
			if dataset.Required && dataset.State != "available" {
				return errors.New("emquant: required snapshot data is unavailable")
			}
		}
	}
	return r.ReauthorizeQuoteComponent(ctx, tx, workspace, task, epoch, quoteHash, use)
}
