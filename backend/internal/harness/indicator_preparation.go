package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

type prepareIndicatorArgs dataservice.IndicatorPreparation

func (prepareIndicatorArgs) Kind() string { return "tushare_indicator_prepare" }

type indicatorPreparationWorker struct {
	river.WorkerDefaults[prepareIndicatorArgs]
	e *Engine
}

func (*indicatorPreparationWorker) Timeout(*river.Job[prepareIndicatorArgs]) time.Duration {
	return 4 * time.Minute
}

func (w *indicatorPreparationWorker) Work(ctx context.Context, job *river.Job[prepareIndicatorArgs]) error {
	if w.e.PrepareTuShareIndicator == nil || !w.e.RealResearch || !w.e.hasCloseSkills() || KillSwitch(ctx, w.e.Pool) {
		return errors.New(string(dataservice.ErrAuthFailed))
	}
	if err := w.e.PrepareTuShareIndicator(ctx, dataservice.IndicatorPreparation(job.Args)); err != nil {
		return marketPreparationError(ctx, err, "TuShare 指标数据采集或发布失败")
	}
	return nil
}

func indicatorDataset(topic string) string {
	switch topic {
	case DailyTradingTopic:
		return "mkt.daily_bar"
	case FinancingBalanceTopic:
		return "flow.margin"
	default:
		return ""
	}
}

func (e *Engine) UsesIndicatorPreparation(topic string) bool {
	return indicatorDataset(topic) != "" && e.PrepareTuShareIndicator != nil && e.hasCloseSkills()
}

func (e *Engine) PrepareIndicator(ctx context.Context, ws, turn uuid.UUID, revision int64, subject dataservice.EntityRow, topic string, window int) (SubjectPreparation, error) {
	if !e.UsesIndicatorPreparation(topic) || e.River == nil || !e.RealResearch || KillSwitch(ctx, e.Pool) {
		return SubjectPreparation{}, errs.Unavailable("PREPARATION_UNAVAILABLE", "指标数据准备当前不可用")
	}
	dataset := indicatorDataset(topic)
	if !mainlandStockCode.MatchString(subject.Code) || !e.Store.AllowsIndicatorSubject(subject.Code) ||
		!slices.Contains(dataservice.IndicatorWindows(dataset), window) {
		return SubjectPreparation{}, errs.Unprocessable("INVALID_PARAMS", "请选择支持的 A 股标的及交易日范围")
	}
	args := prepareIndicatorArgs{Code: subject.Code, DatasetID: dataset, Window: window,
		CloseCutoff: dataservice.ComparisonCloseDay(e.Now()).Format(time.DateOnly)}
	var output json.RawMessage
	return e.requestWindowPreparation(ctx, ws, turn, revision, args, &output)
}
