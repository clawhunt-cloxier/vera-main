package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

// RealtimeEnabled means a deployment has an explicit quote source. Readiness
// for a subject and requirement is checked against current database grants.
func (e *Engine) RealtimeEnabled() bool {
	return e.Brief != nil && e.Brief.Runtime != nil && e.RealtimeSource != nil &&
		e.RealtimeSource.ProviderID != "" && e.RealtimeSource.Product != ""
}

// SkillVersion resolves persisted versions without changing the legacy
// observation entry point or silently upgrading a confirmed task.
func (e *Engine) SkillVersion(id, version string) *Skill {
	if id == "stock_analysis" && version == MixedObservationVersion && e.Brief != nil && e.hasCloseSkills() {
		return MixedObservationSkill()
	}
	if id == "stock_analysis" && version == RealtimeBriefVersion && e.RealtimeEnabled() {
		return RealtimeBriefSkill()
	}
	if sk := e.Skills[id]; sk != nil && sk.Version == version {
		return sk
	}
	if sk := e.Skills[id]; sk != nil && (sk.Version == CloseDisclosureVersion || sk.Version == CloseUnifiedVersion) {
		switch version {
		case CloseDisclosureVersion:
			if id == "stock_analysis" || id == "capital_flow" {
				return TuShareSkills()[id]
			}
		case CloseCompanyVersion:
			if id == "stock_analysis" || id == "deep_research" {
				return tuShareSkills(false)[id]
			}
		case CloseResearchVersion:
			if id == "capital_flow" {
				return tuShareSkills(false)[id]
			}
		}
	}
	if id == "stock_analysis" && version == emquant.ObservationBriefVersion && e.Brief != nil {
		return ObservationBriefSkill()
	}
	return nil
}

func (e *Engine) supportsSnapshotMode(mode, version string) bool {
	if mode == "fixed_legacy" {
		return version != RealtimeBriefVersion && version != emquant.ObservationBriefVersion && version != MixedObservationVersion
	}
	return mode == "collect_latest" && ((version == emquant.ObservationBriefVersion && e.Brief != nil) || (version == RealtimeBriefVersion && e.RealtimeEnabled()) || (version == MixedObservationVersion && e.Brief != nil && e.hasCloseSkills()))
}

func requireBriefSource(ctx context.Context, tx pgx.Tx, source BriefQuoteSource, code, requirement string) error {
	if source.ProviderID == "" || source.Product == "" || !mainlandStockCode.MatchString(code) ||
		(requirement != string(emquant.QuoteRealtimeTrade) && requirement != string(emquant.QuoteLatestAvailable)) {
		return errors.New("harness: invalid Choice quote source or requirement")
	}
	resolved, err := emquant.ResolveQuoteSourceTx(ctx, tx, source.ProviderID, source.Product, code, requirement)
	if err != nil {
		return fmt.Errorf("harness: Choice quote unavailable: %w", err)
	}
	for _, use := range []dataservice.SourceUse{dataservice.SourceStorage, dataservice.SourceDisplay, dataservice.SourceModel} {
		if err := dataservice.RequireSourceUse(ctx, tx, resolved.Grant(use, code)); err != nil {
			return fmt.Errorf("harness: Choice quote %s authorization unavailable: %w", use, err)
		}
	}
	// The authorization locks can wait. Recheck the selected mapping policy
	// under the same transaction before confirming the plan.
	var current uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM quote_mapping_policies WHERE id=$1 AND source_license_id=$2 AND dataset_id='mkt.intraday_quote'
		AND revoked_at IS NULL AND verified_at<=clock_timestamp() AND valid_from<=clock_timestamp()
		AND (valid_until IS NULL OR valid_until>clock_timestamp()) FOR SHARE`, resolved.PolicyID, resolved.LicenseID).Scan(&current); err != nil {
		return fmt.Errorf("harness: Choice quote mapping expired during confirmation: %w", err)
	}
	return nil
}

func (e *Engine) BuildRealtimePlan(ctx context.Context, ws uuid.UUID, skillID string, subject dataservice.EntityRow, raw map[string]any, requirement string) (*Plan, error) {
	if !e.RealResearch {
		return nil, errs.Unavailable("REAL_RESEARCH_DISABLED", "真实研究尚未开启")
	}
	if skillID != "stock_analysis" {
		return nil, errs.Unprocessable("SKILL_UNAVAILABLE", "当前行情简报仅支持单只 A 股")
	}
	// New clients use the deployment-wide latest-available policy and omit the
	// legacy selector. Keep an explicit realtime_trade value for old clients,
	// while treating an omitted value as latest_available.
	if requirement == "" {
		requirement = string(emquant.QuoteLatestAvailable)
	}
	if !e.RealtimeEnabled() {
		return nil, errs.Unavailable("REALTIME_UNAVAILABLE", "行情数据源尚未配置")
	}
	if err := onlyKeys(raw); err != nil {
		return nil, err
	}
	preview := e.Now().UTC().Truncate(time.Microsecond)
	ready := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		return requireBriefSource(ctx, tx, *e.RealtimeSource, subject.Code, requirement)
	}) == nil
	definition, err := NewQuoteBriefPlan(subject, preview, ready, requirement)
	if err != nil {
		return nil, errs.Unprocessable("INVALID_QUOTE_REQUEST", "请选择非模拟 A 股")
	}
	hash, err := definition.Hash()
	if err != nil {
		return nil, err
	}
	p := &Plan{ID: uuid.New(), Hash: hash, SkillID: definition.SkillID, SkillVersion: definition.SkillVersion, Subject: subject, AsOf: preview,
		Sections: definition.Sections, Executable: ready, Notes: []string{"确认后采集并冻结一次行情；最终实时条件按实际行情核验。"},
		P95Seconds: min(e.Policy.Budgets[skillID].DeadlineSeconds, 110), QuotaCost: 1, ExpiresAt: time.Now().Add(planTTL), SnapshotMode: definition.SnapshotMode,
		QuoteRequirement: &requirement}
	if !ready {
		code := "REQUIRED_SECTION_UNAVAILABLE"
		p.ReasonCode = &code
	}
	sections, _ := json.Marshal(p.Sections)
	err = db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO research_plans(id,workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,reason_code,expires_at,snapshot_mode,quote_requirement)
			VALUES($1,$2,$3,$4,$5,'{}',$6,$7,$8,$9,$10,$11,$12,$13)`, p.ID, ws, p.SkillID, p.SkillVersion, subject.ID, sections, p.AsOf, p.Hash, p.Executable, p.ReasonCode, p.ExpiresAt, p.SnapshotMode, requirement)
		return err
	})
	return p, err
}

// ParseRealtimePlan resolves catalog entities with deterministic rules. It
// never lets a router model select the quote requirement or collection scope.
func (e *Engine) ParseRealtimePlan(ctx context.Context, ws uuid.UUID, text, requirement string) (*ParseResult, error) {
	text = strings.TrimSpace(text)
	if adviceRe.MatchString(text) {
		return unsupported("Vera 不提供买卖、仓位或目标价建议。"), nil
	}
	if predictionRe.MatchString(text) {
		return unsupported("Vera 不预测价格或涨跌。"), nil
	}
	entities, err := e.Store.Mentioned(ctx, text)
	if err != nil {
		return nil, err
	}
	entities = dedupe(entities)
	if len(entities) == 0 {
		return unsupported("没有在已接入的数据中找到该证券，请先搜索并选择一只 A 股。"), nil
	}
	if len(entities) > 1 {
		out := &ParseResult{Kind: "choose", Candidates: []Candidate{}, Suggestions: []string{}}
		for _, entity := range entities {
			if entity.Kind == "stock" && !entity.Synthetic {
				out.Candidates = append(out.Candidates, Candidate{SkillID: "stock_analysis", Subject: entity, Label: "行情数据简报：" + entity.Name})
			}
		}
		if len(out.Candidates) == 0 {
			return unsupported("当前行情简报仅支持单只非模拟 A 股。"), nil
		}
		return out, nil
	}
	p, err := e.BuildRealtimePlan(ctx, ws, "stock_analysis", entities[0], map[string]any{}, requirement)
	if err != nil {
		return nil, err
	}
	return &ParseResult{Kind: "plan", Plan: p, Candidates: []Candidate{}, Suggestions: []string{}}, nil
}
