package harness

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

// One in-flight collection per symbol across workspaces. The first request
// is charged against admission limits; subsequent requests only read state.
type prepareSubjectArgs struct {
	Code      string    `json:"code"`
	Workspace uuid.UUID `json:"workspace"`
}

func (prepareSubjectArgs) Kind() string { return "tushare_subject_prepare" }

const prepareQueue = "subject_prepare"

type SubjectPreparation struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type prepareSubjectWorker struct {
	river.WorkerDefaults[prepareSubjectArgs]
	e *Engine
}

func (*prepareSubjectWorker) Timeout(*river.Job[prepareSubjectArgs]) time.Duration {
	return 30 * time.Minute
}

func (w *prepareSubjectWorker) Work(ctx context.Context, j *river.Job[prepareSubjectArgs]) error {
	if !w.e.RealResearch || w.e.PrepareTuShareSubject == nil || KillSwitch(ctx, w.e.Pool) {
		return errors.New("研究数据准备已暂停")
	}
	if err := w.e.WithMixedResearchUse(ctx, func() error { return nil }); err != nil {
		return errors.New("研究来源当前未授权")
	}
	if err := w.e.PrepareTuShareSubject(ctx, j.Args.Code); err != nil {
		var pe *dataservice.ProviderError
		if errors.As(err, &pe) {
			// Persist only the classification, never upstream responses or credentials.
			return errors.New(string(pe.Class))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("TuShare 数据采集或发布失败")
	}
	return nil
}

func (e *Engine) RequestSubjectPreparation(ctx context.Context, ws uuid.UUID, code string) (SubjectPreparation, error) {
	if e.PrepareTuShareSubject == nil || !e.RealResearch || e.Store == nil || e.Pool == nil || KillSwitch(ctx, e.Pool) {
		return SubjectPreparation{}, errs.Unavailable("PREPARATION_UNAVAILABLE", "真实研究数据准备当前不可用")
	}
	ready, err := e.Store.ResearchSubjectPrepared(ctx, code)
	if err != nil {
		return SubjectPreparation{}, err
	}
	if ready {
		return SubjectPreparation{Status: "ready"}, nil
	}
	if e.River == nil || e.Policy == nil {
		return SubjectPreparation{}, errs.Unavailable("PREPARATION_UNAVAILABLE", "真实研究数据准备队列尚未就绪")
	}
	return e.enqueueSubjectPreparation(ctx, ws, code)
}

func preparationFailure(code string) string {
	switch dataservice.ErrClass(code) {
	case dataservice.ErrAuthFailed:
		return "TuShare 账户或数据用途授权不可用，请联系管理员核对权限。"
	case dataservice.ErrNotCovered, dataservice.ErrEmptyValid:
		return "TuShare 未提供该股票所需的研究数据，请核对接口权限和覆盖范围。"
	case dataservice.ErrRateLimited:
		return "TuShare 请求达到频率限制，请稍后重试。"
	default:
		return "TuShare 数据准备失败，请稍后重试或联系管理员查看同步记录。"
	}
}

func (e *Engine) enqueueSubjectPreparation(ctx context.Context, ws uuid.UUID, code string) (SubjectPreparation, error) {
	result := SubjectPreparation{Status: "pending"}
	args := prepareSubjectArgs{Code: code, Workspace: ws}
	err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		// Serialize only the short check-and-insert, not vendor calls. This
		// bounds outstanding work even across concurrent API replicas.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(600048715, 36)`); err != nil {
			return err
		}
		var state, failure string
		var recent bool
		err := tx.QueryRow(ctx, `SELECT state, coalesce(finalized_at > now()-interval '2 minutes',false),
			coalesce(errors[array_upper(errors,1)]->>'error','') FROM river_job
			WHERE kind=$1 AND args->>'code'=$2 ORDER BY id DESC LIMIT 1`, args.Kind(), code).Scan(&state, &recent, &failure)
		if err == nil {
			switch state {
			case "cancelled", "discarded":
				if recent {
					result = SubjectPreparation{Status: "failed", Message: preparationFailure(failure)}
					return nil
				}
			case "completed":
				// Another caller may have passed its readiness check immediately
				// before the worker committed. Poll again against published data.
				if recent {
					return nil
				}
			default:
				return nil
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var outstanding, daily int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE finalized_at IS NULL),
			count(*) FILTER(WHERE args->>'workspace'=$2 AND created_at > now()-interval '24 hours')
			FROM river_job WHERE kind=$1`, args.Kind(), ws.String()).Scan(&outstanding, &daily); err != nil {
			return err
		}
		if daily >= e.Policy.Quota.UserDailyTasks {
			return errs.New(429, "PREPARATION_DAILY_QUOTA", "过去 24 小时的数据准备配额已用完，请稍后重新发起。")
		}
		if outstanding >= e.Policy.Quota.PlatformInFlightTasks {
			return errs.TooMany("PREPARATION_QUOTA", "数据准备队列已满，请稍后重试")
		}
		_, err = e.River.InsertTx(ctx, tx, args, &river.InsertOpts{Queue: prepareQueue, MaxAttempts: 1})
		return err
	})
	return result, err
}
