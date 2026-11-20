package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const MixedObservationVersion = emquant.MixedObservationVersion

func (e *Engine) hasCloseSkills() bool {
	s := e.Skills["stock_analysis"]
	return s != nil && IsCloseResearchVersion(s.Version)
}

func MixedObservationSkill() *Skill {
	sk := ObservationBriefSkill()
	sk.Version = MixedObservationVersion
	sk.Title = "Choice 成交与 TuShare 收盘简报"
	sk.Name = func(s dataservice.EntityRow, _ Params) string { return s.Name + " · " + sk.Title }
	sections := sk.Sections
	sk.Sections = func(p Params) []Section {
		s := sections(p)
		s[1] = Section{ID: "S3", Title: "TuShare 收盘价", Required: true, Groups: []string{"close"}, Kinds: []string{KFact}, MaxClaims: 1, MaxChars: 120, Writer: true,
			Guide: "仅陈述给定交易日期对应的 TuShare 不复权收盘价并标注日期，不跨来源推断，不推算涨跌。"}
		return s
	}
	sk.Plan = func(ctx context.Context, e *Engine, s dataservice.EntityRow, _ *Params, at time.Time) (map[string]Availability, []string, error) {
		err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error { return e.requireMixedPlan(ctx, tx, s, at) })
		if err != nil {
			return nil, nil, errs.Unavailable("MIXED_SOURCE_UNAVAILABLE", "双来源数据当前不可用")
		}
		return map[string]Availability{"S2": limited("确认后采集 Choice 日成交情况"), "S3": limited("采用计划时已入库的最近一条 TuShare 收盘价，两个来源的交易日期分别标注")}, nil, nil
	}
	return sk
}

func (e *Engine) requireMixedPlan(ctx context.Context, tx pgx.Tx, subject dataservice.EntityRow, preview time.Time) error {
	if !e.RealResearch || e.Brief == nil || e.Store == nil {
		return errors.New("mixed research unavailable")
	}
	if err := e.Brief.MixedReady(ctx, tx, subject.Code, dataservice.SourceModel); err != nil {
		return err
	}
	_, err := e.Store.ObservationBar(ctx, tx, subject.ID, subject.Code, preview, nil)
	return err
}

func (e *Engine) BuildObservationPlan(ctx context.Context, ws uuid.UUID, skillID string, subject dataservice.EntityRow, raw map[string]any) (*Plan, error) {
	if !e.RealResearch || e.Brief == nil {
		return nil, errs.Unavailable("DATA_SOURCE_UNAVAILABLE", "Choice 成交简报未启用")
	}
	if skillID != "stock_analysis" {
		return nil, errs.Unprocessable("SKILL_UNAVAILABLE", "成交简报仅支持单只 A 股")
	}
	if e.hasCloseSkills() && e.Store != nil && e.Store.RegisteredResearchSubject(subject.Code) {
		p, err := e.buildSkillPlan(ctx, ws, MixedObservationSkill(), subject, raw)
		if err == nil || errs.As(err).Code != "MIXED_SOURCE_UNAVAILABLE" {
			return p, err
		}
		// A new plan may offer the historical single-source product. The server
		// returns .2 and the user confirms that version before any collection.
		p, err = e.buildSkillPlan(ctx, ws, ObservationBriefSkill(), subject, raw)
		if err == nil {
			p.Notes = append(p.Notes, "当前可生成 Choice 成交简报，收盘价补充不可用。")
		}
		return p, err
	}
	return e.buildSkillPlan(ctx, ws, ObservationBriefSkill(), subject, raw)
}

func (e *Engine) ParseObservationPlan(ctx context.Context, ws uuid.UUID, text string) (*ParseResult, error) {
	text = strings.TrimSpace(text)
	if adviceRe.MatchString(text) || predictionRe.MatchString(text) {
		return unsupported("可整理一只 A 股的日成交情况。"), nil
	}
	catalog := e.ChoiceCatalog
	if catalog == nil {
		catalog = e.Store
	}
	matches, err := catalog.Mentioned(ctx, text)
	if err != nil {
		return nil, err
	}
	candidates := []Candidate{}
	for _, v := range dedupe(matches) {
		if v.Kind == "stock" && !v.Synthetic {
			candidates = append(candidates, Candidate{SkillID: "stock_analysis", Subject: v, Label: "日成交简报：" + v.Name})
		}
	}
	switch len(candidates) {
	case 0:
		return unsupported("请先搜索并选择一只已登记的 A 股。"), nil
	case 1:
		p, err := e.BuildObservationPlan(ctx, ws, "stock_analysis", candidates[0].Subject, nil)
		if err != nil {
			return nil, err
		}
		return &ParseResult{Kind: "plan", Plan: p, Candidates: []Candidate{}, Suggestions: []string{}}, nil
	default:
		return &ParseResult{Kind: "choose", Candidates: candidates, Suggestions: []string{}}, nil
	}
}

func (e *Engine) WithChoiceResearchUse(ctx context.Context, fn func() error) error {
	if e.Brief == nil {
		return errors.New("Choice 数据源不可用")
	}
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := e.Brief.MixedProviderUse(ctx, tx, dataservice.SourceModel); err != nil {
			return err
		}
		return fn()
	})
}

func (e *Engine) WithMixedResearchUse(ctx context.Context, fn func() error) error {
	if !e.RealResearch || e.Brief == nil || e.Store == nil {
		return errors.New("双来源研究不可用")
	}
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := e.Brief.MixedProviderUse(ctx, tx, dataservice.SourceModel); err != nil {
			return err
		}
		if err := e.Store.RequireTuShareResearch(ctx, tx); err != nil {
			return err
		}
		return fn()
	})
}

func (e *Engine) WithMixedDisplay(ctx context.Context, ws, task uuid.UUID, fn func(emquant.BriefObservation, []dataservice.StoredRecord) error) error {
	return e.withObservation(ctx, ws, task, MixedObservationVersion, dataservice.SourceDisplay, fn)
}

func (e *Engine) withObservation(ctx context.Context, ws, task uuid.UUID, version string, use dataservice.SourceUse, fn func(emquant.BriefObservation, []dataservice.StoredRecord) error) error {
	if e.Brief == nil {
		return errors.New("Choice 数据源不可用")
	}
	if version == emquant.ObservationBriefVersion {
		return e.Brief.WithSnapshot(ctx, ws, task, use, func(o emquant.BriefObservation) error { return fn(o, nil) })
	}
	if version != MixedObservationVersion || !e.RealResearch {
		return errors.New("双来源研究不可用")
	}
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		o, rs, err := e.Brief.ReadMixed(ctx, tx, ws, task, use, e.Store)
		if err != nil {
			return err
		}
		return fn(o, rs)
	})
}

func addMixedFacts(sheet *facts.Sheet, o emquant.BriefObservation, choice string, row dataservice.StoredRecord) error {
	price, ok := dec(row, "close")
	if !ok || !price.IsPositive() {
		return errors.New("TuShare 收盘价无效")
	}
	day := str(row, "trade_date")
	sheet.Value("close", o.Name, "不复权收盘价", day, facts.KindMarket, "TuShare 不复权日线", "price", price, row.ID)
	val := price.String()
	sheet.Tables["S3"] = []facts.Table{{Title: "TuShare 收盘日线", Columns: []string{"交易日期", "不复权收盘价（元）"}, Rows: [][]*string{{&day, &val}}, Evidence: []uuid.UUID{row.ID}}}
	sheet.SourceProviders = map[string]map[string]string{"mkt.daily_bar": {choice: o.Manifest.Liquidity.Source.ReceivedAt.UTC().Format(time.RFC3339Nano), row.ProviderID: row.AvailableAt.UTC().Format(time.RFC3339Nano)}}
	sheet.Limitations = append(sheet.Limitations, fmt.Sprintf("TuShare 收盘日线的交易日期为 %s，系统可用时间为 %s。两个来源各按所列期间解读。", day, row.AvailableAt.In(cst).Format("2006-01-02 15:04:05 MST")))
	sheet.Finalize()
	return nil
}
