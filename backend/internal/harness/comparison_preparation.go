package harness

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

type prepareComparisonArgs struct {
	Anchor string   `json:"anchor"`
	Others []string `json:"others"`
}

func (prepareComparisonArgs) Kind() string { return "tushare_comparison_prepare" }

type comparisonPreparationWorker struct {
	river.WorkerDefaults[prepareComparisonArgs]
	e *Engine
}

func (*comparisonPreparationWorker) Timeout(*river.Job[prepareComparisonArgs]) time.Duration {
	return 4 * time.Minute
}

func (w *comparisonPreparationWorker) Work(ctx context.Context, job *river.Job[prepareComparisonArgs]) error {
	if !w.e.comparisonAvailable() || w.e.PrepareTuShareComparison == nil || KillSwitch(ctx, w.e.Pool) {
		return errors.New(string(dataservice.ErrAuthFailed))
	}
	sample, err := w.e.PrepareTuShareComparison(ctx, job.Args.Anchor, job.Args.Others)
	if err != nil {
		var providerErr *dataservice.ProviderError
		if errors.As(err, &providerErr) {
			return errors.New(string(providerErr.Class))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("TuShare 对比数据采集或发布失败")
	}
	return river.RecordOutput(ctx, sample)
}

// PrepareComparison binds the request to an active turn. It coalesces matching
// requests using only public stock codes, and briefly reuses completed batches
// when users change the window. The result is available only after publication.
func (e *Engine) PrepareComparison(ctx context.Context, ws, turn uuid.UUID, revision int64, anchor string, others []string) (SubjectPreparation, dataservice.ComparisonSample, error) {
	var sample dataservice.ComparisonSample
	result := SubjectPreparation{Status: "pending"}
	if e.PrepareTuShareComparison == nil || e.River == nil || !e.comparisonAvailable() || KillSwitch(ctx, e.Pool) {
		return result, sample, errs.Unavailable("PREPARATION_UNAVAILABLE", "股价对比数据准备当前不可用")
	}
	if len(others) >= dataservice.ComparisonSubjectLimit || !mainlandStockCode.MatchString(anchor) {
		return result, sample, errs.Unprocessable("INVALID_PARAMS", "请选择支持的 A 股比较对象")
	}
	codes := append([]string{}, others...)
	slices.Sort(codes)
	for i, code := range codes {
		if !mainlandStockCode.MatchString(code) || code == anchor || (i > 0 && code == codes[i-1]) {
			return result, sample, errs.Unprocessable("INVALID_PARAMS", "请选择不同的 A 股比较对象")
		}
	}
	args := prepareComparisonArgs{Anchor: anchor, Others: codes}
	err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		if _, err := lockPendingTurn(ctx, tx, turn, revision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(600048715, 36)`); err != nil {
			return err
		}
		var state, failure string
		var recent, reusable bool
		err := tx.QueryRow(ctx, `SELECT state, coalesce(finalized_at > now()-interval '2 minutes',false),
			coalesce(finalized_at > now()-interval '15 minutes',false),
			coalesce(errors[array_upper(errors,1)]->>'error',''), coalesce(metadata->'output','{}'::jsonb)
			FROM river_job WHERE kind=$1 AND args->>'anchor'=$2 AND (
				(args->'others'=to_jsonb($3::text[]) AND (cardinality($3::text[]) > 0 OR state<>'completed'
					OR metadata->'output'->>'proof_sha256' IS NOT NULL)) OR (
					cardinality($3::text[]) > 0 AND args->'others'='[]'::jsonb AND state='completed'
					AND finalized_at > now()-interval '15 minutes'
					AND metadata->'output'->'codes' @> to_jsonb($3::text[])
					AND metadata->'output'->'codes' <@ to_jsonb($3::text[])
				)
			)
			ORDER BY id DESC LIMIT 1`, args.Kind(), anchor, codes).Scan(&state, &recent, &reusable, &failure, &sample)
		if err == nil {
			switch state {
			case "completed":
				if reusable {
					result.Status = "ready"
					return nil
				}
			case "cancelled", "discarded":
				if recent {
					message := preparationFailure(failure)
					if failure == context.DeadlineExceeded.Error() {
						message = "对比行情准备超过时限，请稍后重试。"
					}
					result = SubjectPreparation{Status: "failed", Message: message}
					return nil
				}
			default:
				return nil
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var outstanding int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE queue=$1 AND finalized_at IS NULL`, prepareQueue).Scan(&outstanding); err != nil {
			return err
		}
		if outstanding >= e.Policy.Quota.PlatformInFlightTasks {
			return errs.TooMany("PREPARATION_QUOTA", "数据准备队列已满，请稍后重试")
		}
		_, err = e.River.InsertTx(ctx, tx, args, &river.InsertOpts{Queue: prepareQueue, MaxAttempts: 1})
		return err
	})
	return result, sample, err
}
