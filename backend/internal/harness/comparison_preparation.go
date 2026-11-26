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

type prepareComparisonArgs dataservice.ComparisonPreparation

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
	sample, err := w.e.PrepareTuShareComparison(ctx, dataservice.ComparisonPreparation(job.Args))
	if err != nil {
		return marketPreparationError(ctx, err, "TuShare 对比数据采集或发布失败")
	}
	return river.RecordOutput(ctx, sample)
}

func marketPreparationError(ctx context.Context, err error, fallback string) error {
	var providerErr *dataservice.ProviderError
	if errors.As(err, &providerErr) {
		return errors.New(string(providerErr.Class))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New(fallback)
}

// PrepareComparison binds the request to an active turn. It coalesces matching
// requests using registered identifiers. The data service checks coverage on
// every new job. The result is available only after publication.
func (e *Engine) PrepareComparison(ctx context.Context, ws, turn uuid.UUID, revision int64, anchor dataservice.EntityRow, others []string, window int, p ComparisonParams) (SubjectPreparation, dataservice.ComparisonSample, error) {
	var sample dataservice.ComparisonSample
	result := SubjectPreparation{Status: "pending"}
	if e.PrepareTuShareComparison == nil || e.River == nil || !e.comparisonAvailable() || KillSwitch(ctx, e.Pool) {
		return result, sample, errs.Unavailable("PREPARATION_UNAVAILABLE", "股价对比数据准备当前不可用")
	}
	if len(others) >= dataservice.ComparisonSubjectLimit || !mainlandStockCode.MatchString(anchor.Code) || !p.Valid() {
		return result, sample, errs.Unprocessable("INVALID_PARAMS", "请选择支持的 A 股比较对象")
	}
	codes := append([]string{}, others...)
	slices.Sort(codes)
	for i, code := range codes {
		if !mainlandStockCode.MatchString(code) || code == anchor.Code || (i > 0 && code == codes[i-1]) {
			return result, sample, errs.Unprocessable("INVALID_PARAMS", "请选择不同的 A 股比较对象")
		}
	}
	var sampleIndustry string
	if p.IndustryIndex && p.SampleProof != "" {
		samples, err := comparisonSampleRecords(ctx, e.Store, anchor.ID, p, e.Now())
		if err != nil {
			return result, sample, err
		}
		if len(samples) == 1 && dataservice.IsComparisonIndustryCode(str(samples[0], "level2_code")) {
			sampleIndustry = str(samples[0], "level2_code")
		}
	}
	benchmarks := p.ComparisonBenchmarks
	broad := append([]string{}, benchmarks.BroadIndexes...)
	slices.Sort(broad)
	benchmarks.BroadIndexes = broad
	args := prepareComparisonArgs{AnchorCode: anchor.Code, OtherCodes: codes, ComparisonBenchmarks: benchmarks,
		SampleIndustry: sampleIndustry, SingleStock: p.SingleStock, Window: window,
		CloseCutoff: dataservice.ComparisonCloseDay(e.Now()).Format(time.DateOnly)}
	result, err := e.requestWindowPreparation(ctx, ws, turn, revision, args, &sample)
	return result, sample, err
}

func (e *Engine) requestWindowPreparation(ctx context.Context, ws, turn uuid.UUID, revision int64, args river.JobArgs, output any) (SubjectPreparation, error) {
	result := SubjectPreparation{Status: "pending"}
	err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		if _, err := lockPendingTurn(ctx, tx, turn, revision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(600048715, 36)`); err != nil {
			return err
		}
		var state, failure string
		var recent, reusable bool
		err := tx.QueryRow(ctx, `SELECT state, coalesce(finalized_at > now()-interval '30 seconds',false),
			coalesce(finalized_at > now()-interval '2 minutes',false),
			coalesce(errors[array_upper(errors,1)]->>'error',''), coalesce(metadata->'output','{}'::jsonb)
			FROM river_job WHERE kind=$1 AND args = to_jsonb($2::jsonb)
			ORDER BY id DESC LIMIT 1`, args.Kind(), args).Scan(&state, &recent, &reusable, &failure, output)
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
						message = "研究数据准备超过时限，请稍后重试。"
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
	return result, err
}
