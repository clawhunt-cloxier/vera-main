package harness

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

// CloseResearchVersion identifies the company reports backed by registered
// close/disclosure data. Real-time and fixture plans retain their own versions.
const CloseResearchVersion = "1.0.1"

func (e *Engine) realResearchDisabled() bool {
	s := e.Skills["stock_analysis"]
	return !e.RealResearch && (!e.Synthetic || (s != nil && s.Version == CloseResearchVersion))
}

func (e *Engine) withCloseRights(ctx context.Context, fn func() error) error {
	if !e.RealResearch || e.WithCloseResearchUse == nil {
		return &abstainError{"BLOCKED", "REAL_RESEARCH_DISABLED", "真实研究或数据用途未开启"}
	}
	err := e.WithCloseResearchUse(ctx, fn)
	var pe *dataservice.ProviderError
	if errors.As(err, &pe) && pe.Class == dataservice.ErrAuthFailed {
		return &abstainError{"BLOCKED", "DATA_SOURCE_FORBIDDEN", "数据来源的研究用途已关闭"}
	}
	return err
}

func TuShareSkills() map[string]*Skill {
	registry := Skills()
	for id, sk := range registry {
		sk.Version = CloseResearchVersion
		sk.Subjects = []string{"stock"}
		validate := sk.Validate
		sk.Validate = func(s dataservice.EntityRow, raw map[string]any) (Params, error) {
			if enabled, _ := s.Attrs["tushare_research_subject"].(bool); !enabled || s.Synthetic || s.Kind != "stock" {
				return Params{}, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "本技能仅支持已登记的 A 股个股")
			}
			p, err := validate(s, raw)
			if err == nil && id == "deep_research" && p.Mode != "company" {
				return Params{}, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "当前采集范围支持公司深度研究")
			}
			return p, err
		}
		plan := sk.Plan
		sk.Plan = func(ctx context.Context, e *Engine, s dataservice.EntityRow, p *Params, asOf time.Time) (map[string]Availability, []string, error) {
			av, notes, err := plan(ctx, e, s, p, asOf)
			if err != nil {
				return nil, nil, err
			}
			err = checkCloseCoverage(ctx, e, id, s, *p, asOf, av)
			return av, append(notes, "行情使用截至研究时点已入库的收盘数据，财报及公告使用同一时点已披露且已入库的版本。"), err
		}
	}
	sk := registry["deep_research"]
	sk.Schema["properties"].(map[string]any)["mode"] = map[string]any{"enum": []string{"company"}, "default": "company"}
	return registry
}

func checkCloseCoverage(ctx context.Context, e *Engine, id string, subject dataservice.EntityRow, p Params, at time.Time, av map[string]Availability) error {
	ds := []string{"ref.industry", "fin.statement", "doc.periodic_text", "news.item", "doc.announcement", "flow.margin"}
	recs, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: ds, EntityIDs: []uuid.UUID{subject.ID}, AsOf: at})
	if err != nil {
		return err
	}
	b := newBundle(at, subject, nil, recs)
	industry, hasIndustry := activeIndustry(b.Rows("ref.industry", subject.ID), at)
	if !hasIndustry && id != "capital_flow" {
		av["S2"] = unavailable("缺少截至研究时点有效的申万行业分类")
	}
	if id == "capital_flow" {
		open, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: []string{"ref.trading_calendar"}, AsOf: at})
		if err != nil {
			return err
		}
		cal := newBundle(at, subject, nil, open)
		if len(cal.Days) < 2 {
			av["S2"] = unavailable("缺少有效交易日历")
		}
		days := cal.Window(p.Window)
		count := 0
		for _, r := range b.Rows("flow.margin", subject.ID) {
			if _, ok := input(r, "financing_balance", "CNY"); ok && inWindow(r, "trade_date", days) {
				count++
			}
		}
		if count < 2 {
			av["S3"] = unavailable("观察窗口内融资余额不足两个交易日")
		}
		return nil
	}
	// Only consecutive annual statements support a year-on-year comparison.
	years := map[string]map[string]bool{}
	latest := ""
	annual := annualDocuments(b.Rows("doc.announcement", subject.ID))
	for _, r := range b.Rows("fin.statement", subject.ID) {
		period := str(r, "report_period")
		if str(r, "scope") != "consolidated" || str(r, "report_type") != "FY" || !strings.HasSuffix(period, "-12-31") {
			continue
		}
		if _, ok := dec(r, "value"); !ok {
			continue
		}
		if document := str(annual[period], "announcement_id"); document == "" || document != str(r, "filing_id") {
			continue
		}
		if years[period] == nil {
			years[period] = map[string]bool{}
		}
		years[period][str(r, "item_code")] = true
		latest = max(latest, period)
	}
	finOK := latest != "" && years[latest]["IS.revenue"] && years[latest]["IS.net_profit_parent"] &&
		years[priorYear(latest)]["IS.revenue"] && years[priorYear(latest)]["IS.net_profit_parent"]
	sections := map[string]bool{}
	latestDisclosed := ""
	for period := range annual {
		latestDisclosed = max(latestDisclosed, period)
	}
	announcement := str(annual[latestDisclosed], "announcement_id")
	finOK = finOK && latestDisclosed == latest && announcement != ""
	for _, r := range b.Rows("doc.periodic_text", subject.ID) {
		if str(r, "report_period") == latestDisclosed && str(r, "announcement_id") == announcement && announcement != "" &&
			strings.TrimSpace(str(r, "text")) != "" && !check.Suspicious(str(r, "text")) {
			sections[str(r, "section")] = true
		}
	}
	if !sections["business_overview"] {
		av["S2"] = unavailable("缺少最近年度财报可引用的主营业务原文")
	}
	if id == "stock_analysis" {
		if !finOK {
			av["S3"] = unavailable("缺少连续两个年度可比的合并营收和归母净利润")
		}
		if !years[latest]["CF.operating_net"] {
			av["S4"] = unavailable("缺少最近年度经营现金流净额")
		}
		recent := func(ds string) bool {
			return slices.ContainsFunc(b.Rows(ds, subject.ID), func(r dataservice.StoredRecord) bool {
				return r.PublishedAt != nil && !r.PublishedAt.Before(at.AddDate(0, 0, -30)) && !r.PublishedAt.After(at)
			})
		}
		if !recent("news.item") {
			av["S9"] = limited("近 30 天没有可引用的授权新闻，仅使用近期公告")
			if !recent("doc.announcement") {
				av["S9"] = unavailable("近 30 天没有可引用的新闻或公告")
			}
		}
	} else {
		if !finOK {
			av["S4"] = unavailable("缺少连续两个年度可比的合并营收和归母净利润")
		}
		if !sections["risk_factors"] {
			av["S8"] = unavailable("缺少最近年度财报可引用的风险因素原文")
		}
	}
	if hasIndustry {
		members, err := (Loader{e.Store}).Members(ctx, str(industry, "level2_code"), at)
		if err != nil {
			return err
		}
		peers, err := selectPeers(ctx, e, subject, members, at)
		if err != nil {
			return err
		}
		if len(peers) < 5 && av["S5"].State == "available" {
			av["S5"] = limited("同日有市值可比较的同行不足五家，仅展示有依据的估值信息")
			if id == "deep_research" {
				av["S5"] = unavailable("公司行业位置缺少五家同日可比的同行")
			}
		}
	}
	return nil
}
