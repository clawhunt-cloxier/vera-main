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
const CloseCompanyVersion = "1.0.2"
const CloseDisclosureVersion = "1.0.3" // stock_analysis and capital_flow
const CloseUnifiedVersion = "1.0.4"

func IsCloseResearchVersion(version string) bool {
	return version == CloseResearchVersion || version == CloseCompanyVersion || version == CloseDisclosureVersion || version == CloseUnifiedVersion || version == ConversationCloseVersion
}

func (e *Engine) realResearchDisabled() bool {
	s := e.Skills["stock_analysis"]
	return !e.RealResearch && (!e.Synthetic || (s != nil && IsCloseResearchVersion(s.Version)))
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
	return tuShareSkills(true)
}

// UnifiedCloseSkills starts new company research without changing the
// requirements of an already confirmed close research version.
func UnifiedCloseSkills() map[string]*Skill {
	registry := TuShareSkills()
	for id, sk := range registry {
		sk.Version = CloseUnifiedVersion
		sections := sk.Sections
		sk.Sections = func(p Params) []Section {
			return unifiedCloseSections(id, sections(p))
		}
		if id == "deep_research" {
			compute := sk.Compute
			sk.Compute = func(b *builder, p Params) {
				compute(b, p)
				// The archived skill computed market facts for the observation
				// section but did not give the charts a dedicated section.
				for i := range b.S.Charts {
					if b.S.Charts[i].ID == "price_adj" || b.S.Charts[i].ID == "relative_perf" {
						b.S.Charts[i].Section = "S11"
					}
				}
			}
		}
	}
	return registry
}

func unifiedCloseSections(id string, base []Section) []Section {
	sections := slices.Clone(base)
	for i := range sections {
		s := &sections[i]
		switch id {
		case "stock_analysis":
			switch s.ID {
			case "S2":
				s.Guide = "说明证券上市及行业分类事实；只有存在可引用的当期年报原文或经核实的分部事实时，才讨论主营业务或收入构成。"
			case "S3", "S4":
				s.Required = false
			case "S10":
				s.Required = false
				s.Title = "观察要点"
				s.Groups = append(slices.Clone(s.Groups), "profile")
				s.Kinds = []string{KFact, KComp, KInf, KScen}
				s.Guide = "优先从已披露的财务事实或实际交易指标整理观察要点。缺少财报时仅写有依据的行情观察，不判断财务风险或预测价格。"
			}
		case "deep_research":
			switch s.ID {
			case "S2":
				s.Title = "公司与行业概况"
				s.Guide = "说明证券上市及行业分类事实；只有存在可引用的当期年报原文或经核实的分部事实时，才讨论商业模式或收入组成。"
			case "S4", "S5", "S8":
				s.Required = false
			case "S10":
				s.Required = false
				s.Groups = append(slices.Clone(s.Groups), "profile", "position")
				s.Kinds = []string{KFact, KComp, KInf, KScen}
				s.Guide = "仅由可引用的公司、市场或财务事实整理观察要点。缺少财报时不得写成对财务风险或商业模式的判断，不预测价格或概率。"
			}
		}
	}
	if id == "deep_research" {
		// Display observed market facts before any synthesis. Preserve the
		// archived section identifiers for other company research sections.
		sections = slices.Insert(sections, len(sections)-1,
			sec("S11", "行情与交易", true, []string{"mkt"}, factKinds, 6, 120,
				"说明有日期的最近收盘价及可计算的区间表现。它们是已收盘数据，不表述为盘中实时行情、不预测价格。"))
	}
	return sections
}

func applyUnifiedCloseCoverage(b *Bundle, id string, av map[string]Availability) {
	marketSection := "S6"
	switch id {
	case "capital_flow":
		marketSection = "S2"
	case "deep_research":
		marketSection = "S11"
	}
	validSince := previousCloseSession(b, b.Global("ref.trading_calendar"))
	lastTrade := ""
	completed := b.AsOf.In(cst)
	if completed.Hour() < 15 {
		completed = completed.AddDate(0, 0, -1)
	}
	lastCompletedDay := completed.Format(time.DateOnly)
	for _, r := range b.Rows("mkt.daily_bar", b.Subject.ID) {
		day := str(r, "trade_date")
		close, hasClose := dec(r, "close")
		_, tradingSession := slices.BinarySearch(b.Days, day)
		if hasClose && close.IsPositive() && !boolean(r, "suspended") && day <= lastCompletedDay && tradingSession {
			lastTrade = max(lastTrade, day)
		}
	}
	if validSince == "" || lastTrade < validSince {
		reason := "缺少近期已收盘交易日的有效价格或交易日历"
		if lastTrade != "" {
			reason += "，已取得的收盘价截至 " + lastTrade
		}
		av[marketSection] = unavailable(reason)
	} else if av[marketSection].State == "" {
		av[marketSection] = avail()
	}
	if id == "capital_flow" {
		return
	}
	rs := b.Rows("ref.security", b.Subject.ID)
	industry, ok := activeIndustry(b.Rows("ref.industry", b.Subject.ID), b.AsOf)
	if len(rs) == 0 || str(rs[len(rs)-1], "list_date") == "" {
		av["S2"] = unavailable("缺少可引用的证券上市档案")
		return
	}
	if !ok || str(industry, "level1_name") == "" || str(industry, "level2_name") == "" {
		av["S2"] = unavailable("缺少截至研究时点有效的申万行业分类")
		return
	}
	if av["S2"].State == "unavailable" {
		av["S2"] = limited("缺少可核对的最新年报主营业务原文；公司概况以已登记的证券档案和行业分类为基础")
	}
}

func tuShareSkills(disclosures bool) map[string]*Skill {
	registry := Skills()
	for id, sk := range registry {
		sk.Version = CloseResearchVersion
		if id == "stock_analysis" || id == "deep_research" {
			sk.Version = CloseCompanyVersion
		}
		useDisclosures := disclosures && (id == "stock_analysis" || id == "capital_flow")
		if useDisclosures {
			sk.Version = CloseDisclosureVersion
			collect := sk.Collect
			sk.Collect = func(ctx context.Context, e *Engine, s dataservice.EntityRow, p Params, at time.Time) (*Bundle, error) {
				b, err := collect(ctx, e, s, p, at)
				if err != nil {
					return nil, err
				}
				extra := []string{seatDayDataset}
				if id == "stock_analysis" {
					extra = append(extra, "corp.dividend_implementation")
				} else {
					extra = append(extra, "fund.disclosed_position")
				}
				rs, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: extra, EntityIDs: []uuid.UUID{s.ID}, AsOf: at})
				if err != nil {
					return nil, err
				}
				for _, r := range rs {
					k := key(r.DatasetID, s.ID)
					b.byKey[k] = append(b.byKey[k], r)
					b.IDs = append(b.IDs, r.ID)
				}
				return b, nil
			}
			compute := sk.Compute
			sk.Compute = func(b *builder, p Params) {
				b.disclosedSeats = true
				b.paidDividendEvents = id == "stock_analysis"
				b.disclosedFundPositions = id == "capital_flow"
				compute(b, p)
			}
		}
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
			err = checkCloseCoverage(ctx, e, id, s, *p, asOf, av, useDisclosures, sk.Version == CloseUnifiedVersion)
			return av, append(notes, "行情使用截至研究时点已入库的收盘数据，财报及公告使用同一时点已披露且已入库的版本。"), err
		}
	}
	sk := registry["deep_research"]
	sk.Schema["properties"].(map[string]any)["mode"] = map[string]any{"enum": []string{"company"}, "default": "company"}
	return registry
}

func checkCloseCoverage(ctx context.Context, e *Engine, id string, subject dataservice.EntityRow, p Params, at time.Time, av map[string]Availability, disclosures, unified bool) error {
	ds := []string{"ref.industry", "fin.statement", "doc.periodic_text", "news.item", "doc.announcement", "flow.margin", "mkt.daily_bar", "mkt.valuation_daily"}
	if disclosures {
		ds = append(ds, seatDayDataset, "flow.dragon_tiger", "flow.block_trade", "fund.disclosed_position")
	}
	if id == "deep_research" {
		ds = append(ds, "fin.segment")
	}
	if unified && id != "capital_flow" {
		ds = append(ds, "ref.security")
	}
	recs, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: ds, EntityIDs: []uuid.UUID{subject.ID}, AsOf: at})
	if err != nil {
		return err
	}
	calendar, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: []string{"ref.trading_calendar"}, AsOf: at})
	if err != nil {
		return err
	}
	b := newBundle(at, subject, nil, append(recs, calendar...))
	// Evaluate the real-evidence core after the historical availability
	// rules, including for the early-return capital flow branch.
	if unified {
		defer applyUnifiedCloseCoverage(b, id, av)
	}
	validSince := previousCloseSession(b, calendar)
	lastBar := lastMarketDay(b.Rows("mkt.daily_bar", subject.ID), "close", true)
	lastVal := lastMarketDay(b.Rows("mkt.valuation_daily", subject.ID), "total_mv", false)
	if validSince == "" || lastBar < validSince {
		reason := "收盘数据或交易日历需要刷新"
		if lastBar != "" {
			reason += "，已取得的行情截至 " + lastBar
		}
		if id == "stock_analysis" {
			av["S6"] = unavailable(reason)
		}
		if id == "capital_flow" {
			av["S2"] = unavailable(reason)
		}
	}
	if id != "capital_flow" && (validSince == "" || lastVal < validSince) {
		av["S5"] = unavailable("可比估值或交易日历需要刷新")
	}
	industry, hasIndustry := activeIndustry(b.Rows("ref.industry", subject.ID), at)
	if !hasIndustry && id != "capital_flow" {
		av["S2"] = unavailable("缺少截至研究时点有效的申万行业分类")
	}
	if disclosures && id == "stock_analysis" && av["S7"].State == "unavailable" && len(b.seatDisclosures(subject.ID, p.Window).days) > 0 {
		av["S7"] = limited("资金面数据仅覆盖窗口内已查询的龙虎榜披露")
	}
	if id == "capital_flow" {
		if disclosures {
			av["S4"] = b.seatDisclosures(subject.ID, p.Window).availability()
			if len(fundPositionSample(b, subject.ID)) > 0 && av["S5"].State == "unavailable" {
				av["S5"] = limited(fundPositionLimit)
			}
		}
		days := b.Window(p.Window)
		count := 0
		for _, r := range b.Rows("flow.margin", subject.ID) {
			if _, ok := input(r, "financing_balance", "CNY"); ok && inWindow(r, "trade_date", days) {
				count++
			}
		}
		if count < 2 {
			av["S3"] = unavailable("观察窗口内融资余额不足两个交易日")
		}
		if validSince == "" || lastMarketDay(b.Rows("flow.margin", subject.ID), "financing_balance", false) < validSince {
			av["S3"] = unavailable("融资余额尚未更新至最近收盘交易日或其前一交易日")
		}
		return nil
	}
	// Only consecutive annual statements support a year-on-year comparison.
	years := map[string]map[string]bool{}
	latest := ""
	annual := periodicDocuments(b.Rows("doc.announcement", subject.ID))
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
		if strings.HasSuffix(period, "-12-31") {
			latestDisclosed = max(latestDisclosed, period)
		}
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
		if _, rows := annualSegmentRows(b, subject.ID); len(rows) == 0 {
			av["S3"] = unavailable("缺少可对应最近年度公告版本的分部收入")
		}
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

// Accept the latest completed session or its predecessor while the vendor
// is publishing today's close. For a holiday, dated calendar rows (including
// closed sessions) distinguish a holiday from a stale installation.
func previousCloseSession(b *Bundle, calendar []dataservice.StoredRecord) string {
	if len(b.Days) < 2 {
		return ""
	}
	completed := b.AsOf.In(cst)
	if completed.Hour() < 15 {
		completed = completed.AddDate(0, 0, -1)
	}
	for completed.Weekday() == time.Saturday || completed.Weekday() == time.Sunday {
		completed = completed.AddDate(0, 0, -1)
	}
	date := completed.Format(time.DateOnly)
	idx, _ := slices.BinarySearch(b.Days, date)
	if idx < len(b.Days) && b.Days[idx] == date {
		idx++
	}
	if idx == 0 {
		return ""
	}
	lastCalendar := ""
	for _, r := range calendar {
		day := str(r, "trade_date")
		if day <= date {
			lastCalendar = max(lastCalendar, day)
		}
	}
	if lastCalendar < date {
		prior := completed.AddDate(0, 0, -1)
		for prior.Weekday() == time.Saturday || prior.Weekday() == time.Sunday {
			prior = prior.AddDate(0, 0, -1)
		}
		if lastCalendar < prior.Format(time.DateOnly) {
			return ""
		}
		return b.Days[idx-1]
	}
	return b.Days[max(0, idx-2)]
}

func lastMarketDay(rows []dataservice.StoredRecord, field string, allowSuspended bool) string {
	last := ""
	for _, r := range rows {
		if _, ok := dec(r, field); ok || (allowSuspended && boolean(r, "suspended")) {
			last = max(last, str(r, "trade_date"))
		}
	}
	return last
}
