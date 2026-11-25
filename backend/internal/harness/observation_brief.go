package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

func ObservationBriefSkill() *Skill {
	return &Skill{ID: "stock_analysis", Version: emquant.ObservationBriefVersion, Title: "真实成交数据简报", Subjects: []string{"stock"},
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		Validate: func(subject dataservice.EntityRow, raw map[string]any) (Params, error) {
			if subject.Synthetic {
				return Params{}, errs.Unprocessable("SYNTHETIC_SUBJECT", "真实简报不接受模拟对象")
			}
			return Params{}, onlyKeys(raw)
		},
		Name: func(s dataservice.EntityRow, _ Params) string { return s.Name + " · 真实成交数据简报" },
		Sections: func(Params) []Section {
			return []Section{
				{ID: "S2", Title: "已取得的日成交数据", Required: true, Groups: []string{"mkt"}, Kinds: []string{KFact}, MaxClaims: 6, MaxChars: 200, Writer: true, Guide: "优先最近日期，简洁陈述厂商返回日期对应的成交量、成交额和换手率。不可推断价格、资金流向、交易状态、实时性或未来走势；仅用提供的事实，缺失项不补全。"},
				{ID: "S3", Title: "实时行情与财务数据", Required: false, Groups: []string{"unavailable"}, Kinds: []string{KFact}, MaxClaims: 1, MaxChars: 100, Writer: false},
			}
		},
		Plan: func(ctx context.Context, e *Engine, s dataservice.EntityRow, _ *Params, _ time.Time) (map[string]Availability, []string, error) {
			ready := e.Brief != nil
			if ready {
				ready = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
					for _, use := range []dataservice.SourceUse{dataservice.SourceStorage, dataservice.SourceDisplay, dataservice.SourceModel} {
						if err := e.Brief.Ready(ctx, tx, s.Code, use); err != nil {
							return err
						}
					}
					return nil
				}) == nil
			}
			state := limited("当前采集的日成交观察，不代表实时行情或完整个股研报")
			if !ready {
				state = unavailable("真实数据源或用途许可不可用")
			}
			return map[string]Availability{"S2": state, "S3": unavailable("实时行情的时点、状态和延迟，以及财报披露版本尚未验明，未纳入本简报")}, nil, nil
		},
	}
}

// complete rechecks the frozen inputs and holds current license locks for each
// model use, including cached step retries and summary/verifier calls.
func (e *Engine) complete(ctx context.Context, t *taskRow, req model.Request) (json.RawMessage, model.Meta, error) {
	if t.Version == ConversationCloseVersion {
		req.WithReuse = func(callCtx context.Context, cached func(pgx.Tx) error) error {
			return e.WithConversationSnapshot(callCtx, t.Workspace, t.ID, cached)
		}
		req.WithSend = func(callCtx context.Context, send func() (model.Raw, error)) (model.Raw, error) {
			if err := e.stillCurrent(callCtx, t); err != nil {
				return model.Raw{}, err
			}
			var raw model.Raw
			err := e.WithConversationSnapshot(callCtx, t.Workspace, t.ID, func(pgx.Tx) error {
				var err error
				raw, err = send()
				return err
			})
			return raw, err
		}
		return e.Gateway.Complete(ctx, req)
	}
	if t.Version == CloseUnifiedVersion {
		req.WithReuse = func(callCtx context.Context, cached func(pgx.Tx) error) error {
			return e.WithUnifiedSources(callCtx, t.Workspace, t.ID, dataservice.SourceModel, func(tx pgx.Tx, _ []BoundResearchSupplement) error { return cached(tx) })
		}
		req.WithSend = func(callCtx context.Context, send func() (model.Raw, error)) (model.Raw, error) {
			if err := e.stillCurrent(callCtx, t); err != nil {
				return model.Raw{}, err
			}
			var raw model.Raw
			err := e.WithUnifiedSources(callCtx, t.Workspace, t.ID, dataservice.SourceModel, func(pgx.Tx, []BoundResearchSupplement) error {
				var err error
				raw, err = send()
				return err
			})
			return raw, err
		}
		return e.Gateway.Complete(ctx, req)
	}
	if IsCloseResearchVersion(t.Version) {
		if t.Snapshot == nil || *t.Snapshot == "" {
			return nil, model.Meta{}, errors.New("harness: missing close research snapshot")
		}
		if err := e.withCloseRights(ctx, func() error { return nil }); err != nil {
			return nil, model.Meta{}, err
		}
		req.WithSend = func(callCtx context.Context, send func() (model.Raw, error)) (model.Raw, error) {
			if err := e.stillCurrent(callCtx, t); err != nil {
				return model.Raw{}, err
			}
			var raw model.Raw
			err := e.withCloseRights(callCtx, func() error {
				var err error
				raw, err = send()
				return err
			})
			return raw, err
		}
		return e.Gateway.Complete(ctx, req)
	}
	if t.Version == RealtimeBriefVersion {
		return e.completeRealtime(ctx, t, req)
	}
	if !emquant.IsDailyBriefVersion(t.Version) && t.Version != MixedObservationVersion {
		return e.Gateway.Complete(ctx, req)
	}
	if e.Brief == nil {
		return nil, model.Meta{}, errors.New("brief runtime unavailable")
	}
	// Validate cached reuse without retaining a connection while the gateway
	// reserves budget or waits for a slot. Every real send acquires fresh rights.
	req.WithReuse = func(callCtx context.Context, cached func(pgx.Tx) error) error {
		return pgx.BeginFunc(callCtx, e.Pool, func(tx pgx.Tx) error {
			if t.Version == MixedObservationVersion {
				if _, _, err := e.Brief.ReadMixed(callCtx, tx, t.Workspace, t.ID, dataservice.SourceModel, e.Store); err != nil {
					return err
				}
			} else if _, err := e.Brief.Read(callCtx, tx, t.Workspace, t.ID, dataservice.SourceModel); err != nil {
				return err
			}
			return cached(tx)
		})
	}
	req.WithSend = func(callCtx context.Context, send func() (model.Raw, error)) (model.Raw, error) {
		if err := e.stillCurrent(callCtx, t); err != nil {
			return model.Raw{}, err
		}
		var raw model.Raw
		err := e.withObservation(callCtx, t.Workspace, t.ID, t.Version, dataservice.SourceModel, func(emquant.BriefObservation, []dataservice.StoredRecord) error {
			var err error
			raw, err = send()
			return err
		})
		return raw, err
	}
	return e.Gateway.Complete(ctx, req)
}

func observationFacts(t *taskRow, o emquant.BriefObservation) *facts.Sheet {
	sheet := facts.NewSheet(t.ID, t.Epoch)
	rows := o.Rows
	if len(rows) > 20 {
		rows = rows[len(rows)-20:]
	}
	table := facts.Table{Title: "Choice 日成交观察（非实时）", Columns: []string{"厂商返回日期", "成交量（股）", "成交额（元）", "换手率（%）"}, Rows: [][]*string{}, Evidence: []uuid.UUID{o.Manifest.Liquidity.ID}}
	for i, row := range rows {
		volume, amount := decimal.RequireFromString(row.VolumeShares), decimal.RequireFromString(row.AmountCNY)
		if i >= len(rows)-3 {
			sheet.Value("mkt", o.Name, "成交量", row.TradeDate, facts.KindMarket, "Choice 日成交观察", "shares", volume, o.Manifest.Liquidity.ID)
			sheet.Value("mkt", o.Name, "成交额", row.TradeDate, facts.KindMarket, "Choice 日成交观察", "CNY", amount, o.Manifest.Liquidity.ID)
		}
		var turn *string
		if row.TurnoverRatio != nil {
			v := decimal.RequireFromString(*row.TurnoverRatio)
			if i >= len(rows)-3 {
				sheet.Value("mkt", o.Name, "换手率", row.TradeDate, facts.KindMarket, "Choice 日成交观察", "ratio", v, o.Manifest.Liquidity.ID)
			}
			s := v.Shift(2).String()
			turn = &s
		}
		day, v, a := row.TradeDate, row.VolumeShares, row.AmountCNY
		table.Rows = append(table.Rows, []*string{&day, &v, &a, turn})
	}
	sheet.Tables["S2"] = []facts.Table{table}
	sheet.Sources["mkt.daily_bar"] = o.Manifest.Liquidity.Source.ReceivedAt.UTC().Format(time.RFC3339Nano)
	sheet.Limitations = []string{
		"本简报使用真实 Choice 当前采集的日成交观察，非盘中实时行情，不构成完整个股分析。",
		"日期为厂商日序列标签；采集时间不等于交易事件或首次发布时间，不提供历史时点可见性保证。",
		"请求截至采集日的前一自然日，展示最近取得的至多二十个日期；缺失日期不推断为休市或停牌。",
		"实时价格、交易状态、延迟和财报版本尚未验明，未输入模型，未以模拟数据补齐。",
		fmt.Sprintf("本次采集：%s；最后取得的日成交日期：%s。", o.Manifest.Liquidity.Source.ReceivedAt.In(cst).Format("2006-01-02 15:04:05 MST"), rows[len(rows)-1].TradeDate),
	}
	sheet.Finalize()
	return sheet
}

func (e *Engine) observationPipeline(ctx, parent context.Context, t *taskRow, sk *Skill) error {
	if e.Brief == nil {
		return abstain("DATA_SOURCE_UNAVAILABLE", "真实数据源未配置")
	}
	if t.Status == "QUEUED" {
		if err := e.transition(ctx, t, []string{"QUEUED"}, "COLLECTING", "", ""); err != nil {
			return err
		}
	}
	ents, err := e.Store.Entities(ctx, []uuid.UUID{t.Subject})
	if err != nil {
		return err
	}
	subject, ok := ents[t.Subject]
	if !ok || subject.Synthetic {
		return abstain("SUBJECT_UNAVAILABLE", "真实证券身份不可用")
	}
	_, err = step(ctx, e, t, "collect", func() (bool, error) {
		// A committed binding survives a crash before the step ledger is completed.
		var bound bool
		if err := db.InWorkspace(ctx, e.Pool, t.Workspace, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT snapshot_manifest_hash IS NOT NULL FROM research_tasks WHERE id=$1`, t.ID).Scan(&bound)
		}); err != nil {
			return false, err
		}
		if bound {
			return true, nil
		}
		m, err := e.Brief.Collect(ctx, subject.Code)
		if err != nil {
			return false, abstain("DATA_SOURCE_UNAVAILABLE", "Choice 日成交数据采集或许可校验失败")
		}
		if t.Version == MixedObservationVersion {
			if err := e.Brief.FreezeMixed(ctx, t.Workspace, t.ID, t.Epoch, m, e.Store); err != nil {
				return false, abstain("MIXED_SOURCE_UNAVAILABLE", "双来源数据或权限不可用")
			}
		} else if err := e.Brief.Freeze(ctx, t.Workspace, t.ID, t.Epoch, m); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	var sheet *facts.Sheet
	err = e.withObservation(ctx, t.Workspace, t.ID, t.Version, dataservice.SourceModel, func(o emquant.BriefObservation, rows []dataservice.StoredRecord) error {
		t.AsOf = o.Cutoff
		t.Snapshot = &o.Hash
		subject.Name = o.Name
		sheet = observationFacts(t, o)
		if t.Version == MixedObservationVersion {
			return addMixedFacts(sheet, o, e.Brief.ProviderID, rows[0])
		}
		return nil
	})
	if err != nil {
		return abstain("SOURCE_AUTHORIZATION_UNAVAILABLE", "冻结来源不可读取或当前用途许可不可用")
	}
	if _, err = step(ctx, e, t, "compute", func() (bool, error) { return true, nil }); err != nil {
		return err
	}
	if t.Status == "COLLECTING" {
		if err := e.transition(ctx, t, []string{"COLLECTING"}, "ANALYZING", "", ""); err != nil {
			return err
		}
	}
	secs := sk.Sections(t.Params)
	results := make([]SectionResult, len(secs))
	for i, sec := range secs {
		if e.sectionState(t, sec.ID) == "unavailable" {
			results[i] = SectionResult{ID: sec.ID, Status: "omitted", Note: e.sectionReason(t, sec.ID)}
			continue
		}
		results[i], err = e.runSection(ctx, t, sk, subject, sheet, sec)
		if err != nil {
			return err
		}
		if sec.Required && len(results[i].Claims) == 0 {
			return abstain("REQUIRED_SECTION_UNSUPPORTED", "模型未产生通过核验的成交数据结论")
		}
	}
	if t.Status == "ANALYZING" {
		if err := e.transition(parent, t, []string{"ANALYZING"}, "ASSEMBLING", "", ""); err != nil {
			return err
		}
	}
	return e.assemble(ctx, parent, t, sk, subject, sheet, secs, results)
}

func (e *Engine) confirmTransaction(ctx context.Context, ws uuid.UUID, plan *Plan, fn func(pgx.Tx) error) error {
	if plan.SkillVersion == CloseUnifiedVersion || plan.SkillVersion == ConversationCloseVersion {
		return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
			if err := e.Store.RequireResearchUseTx(ctx, tx); err != nil {
				return errs.Unavailable("DATA_SOURCE_UNAVAILABLE", "收盘研究数据源当前不可用").Wrap(err)
			}
			if err := setResearchWorkspace(ctx, tx, ws); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	if !emquant.IsDailyBriefVersion(plan.SkillVersion) && plan.SkillVersion != MixedObservationVersion {
		return db.InWorkspace(ctx, e.Pool, ws, fn)
	}
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if e.Brief == nil {
			return errs.Unavailable("DATA_SOURCE_UNAVAILABLE", "真实数据源未配置")
		}
		if plan.SkillVersion == MixedObservationVersion {
			if err := e.requireMixedPlan(ctx, tx, plan.Subject, plan.AsOf); err != nil {
				return errs.Unavailable("MIXED_SOURCE_UNAVAILABLE", "确认所需的双来源数据或权限不可用")
			}
		}
		for _, use := range []dataservice.SourceUse{dataservice.SourceStorage, dataservice.SourceModel, dataservice.SourceDisplay} {
			if err := e.Brief.Ready(ctx, tx, plan.Subject.Code, use); err != nil {
				return errs.Unavailable("SOURCE_PERMISSION_UNAVAILABLE", "当前数据用途许可不可用")
			}
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE vera_app"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.workspace_id',$1,true)", ws.String()); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (e *Engine) deliverTransaction(ctx context.Context, t *taskRow, fn func(pgx.Tx) error) error {
	if t.Version == ConversationCloseVersion {
		err := e.WithConversationSnapshot(ctx, t.Workspace, t.ID, func(tx pgx.Tx) error {
			if err := setResearchWorkspace(ctx, tx, t.Workspace); err != nil {
				return err
			}
			return fn(tx)
		})
		if err != nil && errs.As(err).Status == 404 {
			return errStale
		}
		return err
	}
	if t.Version == CloseUnifiedVersion {
		return e.WithUnifiedSources(ctx, t.Workspace, t.ID, dataservice.SourceDisplay, func(tx pgx.Tx, _ []BoundResearchSupplement) error {
			if err := setResearchWorkspace(ctx, tx, t.Workspace); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	if IsCloseResearchVersion(t.Version) {
		return e.withCloseRights(ctx, func() error { return db.InWorkspace(ctx, e.Pool, t.Workspace, fn) })
	}
	if t.Version == RealtimeBriefVersion {
		return e.withRealtimeSnapshot(ctx, t, dataservice.SourceDisplay, func(tx pgx.Tx, _ emquant.QuoteView) error {
			if _, err := tx.Exec(ctx, "SET LOCAL ROLE vera_app"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "SELECT set_config('app.workspace_id',$1,true)", t.Workspace.String()); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	if !emquant.IsDailyBriefVersion(t.Version) && t.Version != MixedObservationVersion {
		return db.InWorkspace(ctx, e.Pool, t.Workspace, fn)
	}
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if e.Brief == nil {
			return errors.New("brief runtime unavailable")
		}
		if t.Version == MixedObservationVersion {
			if _, _, err := e.Brief.ReadMixed(ctx, tx, t.Workspace, t.ID, dataservice.SourceDisplay, e.Store); err != nil {
				return err
			}
		} else if _, err := e.Brief.Read(ctx, tx, t.Workspace, t.ID, dataservice.SourceDisplay); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE vera_app"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.workspace_id',$1,true)", t.Workspace.String()); err != nil {
			return err
		}
		return fn(tx)
	})
}
