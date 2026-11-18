package emquant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type TaskQuotePublication struct {
	EntityID uuid.UUID
	Result   QuoteIngestResult
}

// CollectTaskQuotes derives its securities from the immutable intent created
// by a trusted 0.4 confirmation, never from a model or an unconfirmed request.
// Partial results may already be published when an error occurs; they are not
// a task snapshot. The caller must roll back any subsequent task binding and
// use the task step ledger for retry/recovery. No task row is held during SDK
// or object-store calls. This method does not create or confirm a plan.
func (r *Runtime) CollectTaskQuotes(ctx context.Context, catalog *dataservice.Catalog, provider, product string, license, policy, workspace, task uuid.UUID, epoch, revision int64) ([]TaskQuotePublication, error) {
	if r == nil || r.database == nil || workspace == uuid.Nil || task == uuid.Nil || epoch < 1 || revision < 1 {
		return nil, errors.New("emquant: invalid task quote collection request")
	}
	type subject struct {
		id   uuid.UUID
		code string
	}
	var subjects []subject
	var planHash, requirement string
	err := pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
		var err error
		planHash, err = checkQuoteTask(ctx, tx, workspace, task, epoch, revision, false)
		if err != nil {
			return err
		}
		var intentHash string
		var ids []uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT plan_hash,requirement,entity_ids FROM quote_task_intents
			WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 AND protocol='0.4'`, workspace, task, epoch).Scan(&intentHash, &requirement, &ids); err != nil {
			return fmt.Errorf("emquant: confirmed quote intent unavailable: %w", err)
		}
		if intentHash != planHash || (requirement != string(QuoteLatestAvailable) && requirement != string(QuoteRealtimeTrade)) || len(ids) == 0 || len(ids) > 64 {
			return errors.New("emquant: quote intent does not match confirmed plan")
		}
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		if ids[0] == uuid.Nil || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return errors.New("emquant: invalid quote intent subjects")
		}
		rows, err := tx.Query(ctx, `SELECT id,code FROM entities WHERE id=ANY($1) AND kind='stock' AND NOT synthetic`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		codes := make(map[uuid.UUID]string, len(ids))
		for rows.Next() {
			var id uuid.UUID
			var code string
			if err := rows.Scan(&id, &code); err != nil {
				return err
			}
			codes[id] = code
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			code, ok := codes[id]
			if !ok || !validSecurityRequest(code) || strings.ToUpper(code) != code {
				return errors.New("emquant: confirmed quote subject unavailable")
			}
			subjects = append(subjects, subject{id, code})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	check := func() error {
		return pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
			current, err := checkQuoteTask(ctx, tx, workspace, task, epoch, revision, false)
			if err != nil {
				return err
			}
			if current != planHash {
				return errors.New("emquant: confirmed plan changed during quote collection")
			}
			return nil
		})
	}
	results := make([]TaskQuotePublication, 0, len(subjects))
	for _, subject := range subjects {
		if err := check(); err != nil {
			return results, err
		}
		result, err := r.CollectAndPublishQuote(ctx, catalog, provider, product, license, policy, subject.id, subject.code)
		if result.StageID != uuid.Nil {
			results = append(results, TaskQuotePublication{EntityID: subject.id, Result: result})
		}
		if err != nil {
			return results, err
		}
		if err := check(); err != nil {
			return results, err
		}
	}
	return results, nil
}

// CollectTaskQuotesDirect derives the requested security code from the trusted
// task intent and resolves the Choice grant independently for each code.
// Static per-security license and policy configuration is deliberately not
// accepted by this path.
func (r *Runtime) CollectTaskQuotesDirect(ctx context.Context, catalog *dataservice.Catalog, provider, product string, workspace, task uuid.UUID, epoch, revision int64) ([]TaskQuotePublication, error) {
	if r == nil || r.database == nil || workspace == uuid.Nil || task == uuid.Nil || epoch < 1 || revision < 1 {
		return nil, errors.New("emquant: invalid task quote collection request")
	}
	type subject struct {
		id   uuid.UUID
		code string
	}
	var subjects []subject
	var planHash, requirement string
	err := pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
		var err error
		planHash, err = checkQuoteTask(ctx, tx, workspace, task, epoch, revision, false)
		if err != nil {
			return err
		}
		var intentHash string
		var ids []uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT plan_hash,requirement,entity_ids FROM quote_task_intents
			WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 AND protocol='0.4'`, workspace, task, epoch).Scan(&intentHash, &requirement, &ids); err != nil {
			return fmt.Errorf("emquant: confirmed quote intent unavailable: %w", err)
		}
		if intentHash != planHash || (requirement != string(QuoteLatestAvailable) && requirement != string(QuoteRealtimeTrade)) || len(ids) == 0 || len(ids) > 64 {
			return errors.New("emquant: quote intent does not match confirmed plan")
		}
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		if ids[0] == uuid.Nil || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return errors.New("emquant: invalid quote intent subjects")
		}
		rows, err := tx.Query(ctx, `SELECT id,code FROM entities WHERE id=ANY($1) AND kind='stock' AND NOT synthetic`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		codes := make(map[uuid.UUID]string, len(ids))
		for rows.Next() {
			var id uuid.UUID
			var code string
			if err := rows.Scan(&id, &code); err != nil {
				return err
			}
			codes[id] = code
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			code, ok := codes[id]
			if !ok || !validSecurityRequest(code) || strings.ToUpper(code) != code {
				return errors.New("emquant: confirmed quote subject unavailable")
			}
			subjects = append(subjects, subject{id, code})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	check := func() error {
		return pgx.BeginFunc(ctx, r.database, func(tx pgx.Tx) error {
			current, err := checkQuoteTask(ctx, tx, workspace, task, epoch, revision, false)
			if err != nil {
				return err
			}
			if current != planHash {
				return errors.New("emquant: confirmed plan changed during quote collection")
			}
			return nil
		})
	}
	results := make([]TaskQuotePublication, 0, len(subjects))
	for _, subject := range subjects {
		if err := check(); err != nil {
			return results, err
		}
		result, err := r.CollectAndPublishQuoteForCode(ctx, catalog, provider, product, requirement, subject.id, subject.code)
		if result.StageID != uuid.Nil {
			results = append(results, TaskQuotePublication{EntityID: subject.id, Result: result})
		}
		if err != nil {
			return results, err
		}
		if err := check(); err != nil {
			return results, err
		}
	}
	return results, nil
}
