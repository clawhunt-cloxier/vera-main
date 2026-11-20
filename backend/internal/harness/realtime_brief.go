package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

func RealtimeBriefSkill() *Skill {
	return &Skill{ID: "stock_analysis", Version: RealtimeBriefVersion, Title: "行情数据简报", Subjects: []string{"stock"},
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		Validate: func(subject dataservice.EntityRow, raw map[string]any) (Params, error) {
			if subject.Synthetic || subject.Kind != "stock" || !mainlandStockCode.MatchString(subject.Code) {
				return Params{}, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "行情简报仅支持非模拟 A 股")
			}
			return Params{}, onlyKeys(raw)
		},
		Name: func(s dataservice.EntityRow, _ Params) string { return s.Name + " · 行情数据简报" },
		Sections: func(Params) []Section {
			return []Section{
				{ID: "S2", Title: "行情与市场事实", Required: true, Groups: []string{"mkt"}, Kinds: []string{KFact}, MaxClaims: 3, MaxChars: 200, Writer: true,
					Guide: "仅陈述冻结行情提供的证券、最新价和事件时点。不得推断收益率、价格趋势、资金流向或未来走势，不自行判断实时性或交易状态；仅用提供的事实，缺失项不补全。"},
				{ID: "S3", Title: "公司与财务事实", Required: false, Groups: []string{"unavailable"}, Kinds: []string{KFact}, MaxClaims: 1, MaxChars: 100, Writer: false},
			}
		},
	}
}

type realtimeCollect struct {
	Snapshot emquant.LatestSnapshot
	Revision int64
}

// withRealtimeSnapshot reauthorizes the exact bound manifest in a fixed view,
// keeping source locks through the model send or display operation.
func (e *Engine) withRealtimeSnapshot(ctx context.Context, t *taskRow, use dataservice.SourceUse, fn func(pgx.Tx, emquant.QuoteView) error) error {
	if e.Brief == nil || e.Brief.Runtime == nil || t.Snapshot == nil || t.SnapshotRevision < 1 {
		return errors.New("harness: bound realtime snapshot unavailable")
	}
	tx, err := e.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	view, err := e.Brief.Runtime.ReadBoundSnapshotQuote(ctx, tx, t.Workspace, t.ID, t.Epoch, t.SnapshotRevision, *t.Snapshot, use)
	if err != nil {
		return err
	}
	if err := fn(tx, view); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *Engine) completeRealtime(ctx context.Context, t *taskRow, req model.Request) (json.RawMessage, model.Meta, error) {
	req.WithReuse = func(callCtx context.Context, cached func(pgx.Tx) error) error {
		return e.withRealtimeSnapshot(callCtx, t, dataservice.SourceModel, func(tx pgx.Tx, _ emquant.QuoteView) error { return cached(tx) })
	}
	req.WithSend = func(callCtx context.Context, send func() (model.Raw, error)) (model.Raw, error) {
		if err := e.stillCurrent(callCtx, t); err != nil {
			return model.Raw{}, err
		}
		var raw model.Raw
		err := e.withRealtimeSnapshot(callCtx, t, dataservice.SourceModel, func(pgx.Tx, emquant.QuoteView) error {
			var err error
			raw, err = send()
			return err
		})
		return raw, err
	}
	return e.Gateway.Complete(ctx, req)
}

func (e *Engine) realtimePipeline(ctx, parent context.Context, t *taskRow, sk *Skill) error {
	if !e.RealtimeEnabled() {
		return abstain("DATA_SOURCE_UNAVAILABLE", "行情数据源未配置")
	}
	if t.Status == "QUEUED" {
		if err := e.transition(ctx, t, []string{"QUEUED"}, "COLLECTING", "", ""); err != nil {
			return err
		}
	}
	entities, err := e.Store.Entities(ctx, []uuid.UUID{t.Subject})
	if err != nil {
		return err
	}
	subject, ok := entities[t.Subject]
	if !ok || subject.Synthetic {
		return abstain("SUBJECT_UNAVAILABLE", "真实证券身份不可用")
	}
	collected, err := step(ctx, e, t, "collect", func() (realtimeCollect, error) {
		var revision int64
		var frozenRevision *int64
		var hash *string
		// A committed binding may outlive a crash before the step ledger write.
		// Read its original revision rather than the task's later progress version.
		if err := e.Pool.QueryRow(ctx, `SELECT t.revision,t.snapshot_manifest_hash,m.task_revision FROM research_tasks t
			LEFT JOIN task_snapshot_manifests m ON m.workspace_id=t.workspace_id AND m.task_id=t.id AND m.epoch=t.epoch AND m.content_hash=t.snapshot_manifest_hash
			WHERE t.id=$1 AND t.workspace_id=$2 AND t.epoch=$3`, t.ID, t.Workspace, t.Epoch).Scan(&revision, &hash, &frozenRevision); err != nil {
			return realtimeCollect{}, err
		}
		if hash != nil {
			if frozenRevision == nil {
				return realtimeCollect{}, errors.New("harness: frozen manifest missing")
			}
			revision = *frozenRevision
		} else {
			source := e.RealtimeSource
			if _, err := e.Brief.Runtime.CollectTaskQuotesDirect(ctx, e.Brief.Catalog, source.ProviderID, source.Product, t.Workspace, t.ID, t.Epoch, revision); err != nil {
				if current := e.stillCurrent(ctx, t); current != nil {
					return realtimeCollect{}, current
				}
				if errors.Is(err, emquant.ErrIPRestricted) {
					return realtimeCollect{}, abstain("DATA_SOURCE_UNAVAILABLE", "Choice 限制海外 IP 访问，当前服务器无法获取行情快照")
				}
				return realtimeCollect{}, abstain("DATA_SOURCE_UNAVAILABLE", "行情采集、归档或用途许可校验失败")
			}
		}
		snapshot, err := e.Brief.Runtime.FreezeLatestSnapshot(ctx, e.Brief.Catalog, t.Workspace, t.ID, t.Epoch, revision)
		return realtimeCollect{Snapshot: snapshot, Revision: revision}, err
	})
	if err != nil {
		return err
	}
	t.AsOf, t.Snapshot, t.SnapshotRevision = collected.Snapshot.Cutoff, &collected.Snapshot.Hash, collected.Revision
	for _, dataset := range collected.Snapshot.Datasets {
		if dataset.Required && dataset.State != "available" {
			return abstain("REQUIRED_QUOTE_UNAVAILABLE", "冻结行情不满足已确认的时点、延迟或交易状态要求")
		}
	}
	var sheet *facts.Sheet
	err = pgx.BeginTxFunc(ctx, e.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		var err error
		sheet, _, err = BoundQuoteFacts(ctx, tx, e.Brief.Runtime, t.Workspace, t.ID, t.Subject, t.Epoch, t.SnapshotRevision, *t.Snapshot)
		return err
	})
	if err != nil {
		return abstain("SOURCE_AUTHORIZATION_UNAVAILABLE", "冻结行情无法核验或当前用途许可不可用")
	}
	sheet.Limitations = append(sheet.Limitations, "本简报仅包含已核验的行情快照；公司与财务资料尚未接入可验证快照，不构成完整个股分析。")
	if _, err := step(ctx, e, t, "compute", func() (bool, error) { return true, nil }); err != nil {
		return err
	}
	if t.Status == "COLLECTING" {
		if err := e.transition(ctx, t, []string{"COLLECTING"}, "ANALYZING", "", ""); err != nil {
			return err
		}
	}
	sections := sk.Sections(t.Params)
	results := make([]SectionResult, len(sections))
	for i, section := range sections {
		if e.sectionState(t, section.ID) == "unavailable" {
			results[i] = SectionResult{ID: section.ID, Status: "omitted", Note: e.sectionReason(t, section.ID)}
			continue
		}
		results[i], err = e.runSection(ctx, t, sk, subject, sheet, section)
		if err != nil {
			return err
		}
		if section.Required && len(results[i].Claims) == 0 {
			return abstain("REQUIRED_SECTION_UNSUPPORTED", "模型未产生通过核验的行情事实")
		}
	}
	if t.Status == "ANALYZING" {
		if err := e.transition(parent, t, []string{"ANALYZING"}, "ASSEMBLING", "", ""); err != nil {
			return err
		}
	}
	return e.assemble(ctx, parent, t, sk, subject, sheet, sections, results)
}
