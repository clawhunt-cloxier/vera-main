package emquant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// QuoteComponentRef identifies only the quote part of a future task snapshot.
// The caller must atomically bind it to the full multi-dataset manifest in the
// same transaction; this component alone is not a deliverable task snapshot.
type QuoteComponentRef struct {
	ContentHash string
	Cutoff      time.Time
}

type quoteComponentDocument struct {
	Version      int         `json:"version"`
	WorkspaceID  uuid.UUID   `json:"workspace_id"`
	TaskID       uuid.UUID   `json:"task_id"`
	Epoch        int64       `json:"epoch"`
	TaskRevision int64       `json:"task_revision"`
	PlanHash     string      `json:"plan_hash"`
	EntityIDs    []uuid.UUID `json:"entity_ids"`
	View         QuoteView   `json:"view"`
}

func quoteComponentDigest(b []byte) string {
	h := sha256.Sum256(append([]byte("vera.quote-component.v1\x00"), b...))
	return hex.EncodeToString(h[:])
}

func checkQuoteTask(ctx context.Context, tx pgx.Tx, workspace, task uuid.UUID, epoch, revision int64, lock bool) (string, error) {
	query := `SELECT t.epoch,t.revision,t.status,p.plan_hash,t.snapshot_hash,t.deadline>clock_timestamp()
		FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
		WHERE t.id=$1 AND t.workspace_id=$2 AND p.consumed_task_id=t.id AND p.executable
		AND t.snapshot_mode='collect_latest' AND p.snapshot_mode=t.snapshot_mode AND t.snapshot_as_of IS NULL`
	if lock {
		query += ` FOR UPDATE OF t FOR SHARE OF p`
	}
	var actualEpoch, actualRevision int64
	var status, planHash string
	var taskSnapshot *string
	var beforeDeadline bool
	if err := tx.QueryRow(ctx, query, task, workspace).Scan(&actualEpoch, &actualRevision, &status, &planHash, &taskSnapshot, &beforeDeadline); err != nil {
		return "", fmt.Errorf("emquant: confirmed task unavailable: %w", err)
	}
	if lock {
		// The first query may have waited for a task lock; evaluate the deadline
		// after acquiring it, while still retaining the fixed MVCC view.
		if err := tx.QueryRow(ctx, `SELECT deadline>clock_timestamp() FROM research_tasks WHERE id=$1 AND workspace_id=$2`, task, workspace).Scan(&beforeDeadline); err != nil {
			return "", err
		}
	}
	if actualEpoch != epoch || actualRevision != revision || status != "COLLECTING" || taskSnapshot != nil || !beforeDeadline {
		return "", errors.New("emquant: task epoch, revision, status or deadline changed")
	}
	return planHash, nil
}

// FreezeQuoteComponent selects quotes and inserts an immutable component using
// the caller's fixed MVCC transaction. Only a trusted 0.4 confirmation path
// may have created the matching intent; the current application does not yet
// create intents or call this function. Repeated calls in the same boundary are
// idempotent. Unbound components are only candidates; recovery must read the
// fully bound task snapshot, never combine candidates from different views.
// The caller must roll back the whole transaction on any error and bind all
// data components before committing. Current authorization must be rechecked
// before display or model processing.
func (r *Runtime) FreezeQuoteComponent(ctx context.Context, tx pgx.Tx, boundary QuoteBoundary, workspace, task uuid.UUID, epoch, revision int64) (QuoteComponentRef, error) {
	if r == nil || tx == nil || workspace == uuid.Nil || task == uuid.Nil || epoch < 1 || revision < 1 {
		return QuoteComponentRef{}, errors.New("emquant: invalid task quote component request")
	}
	if err := boundary.verify(ctx, tx); err != nil {
		return QuoteComponentRef{}, err
	}
	// Do not hold the task row lock while reading remote objects. Cancellation
	// can proceed; the fixed view plus the final locked check fences stale work.
	planHash, err := checkQuoteTask(ctx, tx, workspace, task, epoch, revision, false)
	if err != nil {
		return QuoteComponentRef{}, err
	}
	var intentHash, requirement string
	var entities []uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT plan_hash,requirement,entity_ids FROM quote_task_intents
		WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 AND protocol='0.4' FOR SHARE`, workspace, task, epoch).
		Scan(&intentHash, &requirement, &entities); err != nil {
		return QuoteComponentRef{}, fmt.Errorf("emquant: confirmed 0.4 quote intent unavailable: %w", err)
	}
	if planHash == "" || planHash != intentHash || len(entities) == 0 || len(entities) > 64 {
		return QuoteComponentRef{}, errors.New("emquant: task quote intent does not match the confirmed plan")
	}
	slices.SortFunc(entities, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if entities[0] == uuid.Nil || len(slices.Compact(slices.Clone(entities))) != len(entities) {
		return QuoteComponentRef{}, errors.New("emquant: invalid confirmed quote subjects")
	}
	view, err := r.SelectPublishedQuotesAt(ctx, tx, boundary, entities, QuoteRequirement(requirement), dataservice.SourceModel)
	if err != nil {
		return QuoteComponentRef{}, err
	}
	lockedHash, err := checkQuoteTask(ctx, tx, workspace, task, epoch, revision, true)
	if err != nil {
		return QuoteComponentRef{}, err
	}
	if lockedHash != planHash {
		return QuoteComponentRef{}, errors.New("emquant: confirmed plan changed during quote verification")
	}
	for _, quote := range view.Quotes {
		grant := dataservice.SourceUseRequest{LicenseID: quote.LicenseID, ProviderID: quote.ProviderID, Product: quote.Product,
			DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: []string{"TIME", "NOW"}, Codes: []string{quote.Code}, Use: dataservice.SourceModel}
		if err := dataservice.RequireSourceUse(ctx, tx, grant); err != nil {
			return QuoteComponentRef{}, err
		}
		var current uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND revoked_at IS NULL
			AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp() AND (valid_until IS NULL OR valid_until>clock_timestamp())`, quote.PolicyID).Scan(&current); err != nil {
			return QuoteComponentRef{}, fmt.Errorf("emquant: quote policy expired before task binding: %w", err)
		}
	}
	var deadlineOK bool
	if err := tx.QueryRow(ctx, `SELECT deadline>clock_timestamp() FROM research_tasks WHERE id=$1 AND workspace_id=$2`, task, workspace).Scan(&deadlineOK); err != nil {
		return QuoteComponentRef{}, err
	}
	if !deadlineOK {
		return QuoteComponentRef{}, errors.New("emquant: task deadline passed before quote component write")
	}
	doc := quoteComponentDocument{Version: 1, WorkspaceID: workspace, TaskID: task, Epoch: epoch, TaskRevision: revision, PlanHash: planHash, EntityIDs: entities, View: view}
	body, err := json.Marshal(doc)
	if err != nil || len(body) > 1<<20 {
		return QuoteComponentRef{}, errors.New("emquant: invalid or oversized quote component")
	}
	hash := quoteComponentDigest(body)
	tag, err := tx.Exec(ctx, `INSERT INTO quote_task_components(workspace_id,task_id,epoch,task_revision,plan_hash,cutoff,content_hash,manifest)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8 FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
		WHERE t.id=$2 AND t.workspace_id=$1 AND t.epoch=$3 AND t.revision=$4 AND t.status='COLLECTING' AND t.snapshot_hash IS NULL
		AND t.snapshot_mode='collect_latest' AND p.snapshot_mode=t.snapshot_mode AND t.snapshot_as_of IS NULL
		AND t.deadline>clock_timestamp() AND p.plan_hash=$5 AND p.consumed_task_id=t.id AND p.executable
		ON CONFLICT (workspace_id,task_id,epoch,content_hash) DO NOTHING`, workspace, task, epoch, revision, planHash, view.Cutoff, hash, body)
	if err != nil {
		return QuoteComponentRef{}, fmt.Errorf("emquant: write quote component: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var stored []byte
		var storedCutoff time.Time
		if err := tx.QueryRow(ctx, `SELECT manifest,cutoff FROM quote_task_components WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 AND content_hash=$4`, workspace, task, epoch, hash).Scan(&stored, &storedCutoff); err != nil {
			return QuoteComponentRef{}, err
		}
		if !bytes.Equal(stored, body) || !storedCutoff.Equal(view.Cutoff) || quoteComponentDigest(stored) != hash {
			return QuoteComponentRef{}, errors.New("emquant: quote component hash conflicts with stored content")
		}
	}
	if err := tx.QueryRow(ctx, `SELECT deadline>clock_timestamp() FROM research_tasks WHERE id=$1 AND workspace_id=$2`, task, workspace).Scan(&deadlineOK); err != nil {
		return QuoteComponentRef{}, err
	}
	if !deadlineOK {
		return QuoteComponentRef{}, errors.New("emquant: task deadline passed during quote component write")
	}
	return QuoteComponentRef{ContentHash: hash, Cutoff: view.Cutoff}, nil
}
