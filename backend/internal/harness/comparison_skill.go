package harness

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

const (
	ComparisonTopic              = "stock_comparison"
	ComparisonCloseVersion       = "1.3.0-chat-compare-close.2"
	legacyComparisonCloseVersion = "1.3.0-chat-compare-close.1"
	ComparisonFixtureVersion     = "1.3.0-chat-compare-fixture.1"
)

func IsComparisonVersion(version string) bool {
	return IsCloseComparisonVersion(version) || version == ComparisonFixtureVersion
}

func IsCloseComparisonVersion(version string) bool {
	return version == ComparisonCloseVersion || version == legacyComparisonCloseVersion
}

func IsCloseConversationVersion(version string) bool {
	return version == ConversationCloseVersion || IsCloseComparisonVersion(version)
}

func CloseConversationVersions() []string {
	return []string{ConversationCloseVersion, ComparisonCloseVersion, legacyComparisonCloseVersion}
}

// ComparisonParams stores the resolved comparison set. The subject of the
// plan is the reference stock. SampleProof identifies the original ranked
// industry roster and valuation inputs in a governed record.
type ComparisonParams struct {
	OtherIDs    []uuid.UUID `json:"other_ids"`
	SampleDate  string      `json:"sample_date,omitempty"`
	SampleProof string      `json:"sample_proof,omitempty"`
}

func (p ComparisonParams) ids(subject uuid.UUID) []uuid.UUID {
	return append([]uuid.UUID{subject}, p.OtherIDs...)
}

func (e *Engine) comparisonAvailable() bool {
	return !e.realResearchDisabled() && (e.hasCloseSkills() && e.PrepareTuShareComparison != nil ||
		e.Synthetic && !e.hasCloseSkills() && e.Brief == nil)
}

func (e *Engine) StartStockComparison(ctx context.Context, ws uuid.UUID, subject dataservice.EntityRow, window int, p ComparisonParams, launch ConversationLaunch) (*Plan, error) {
	if !e.comparisonAvailable() {
		return nil, errs.Unprocessable("SKILL_UNAVAILABLE", "当前环境未开放股价横向对比")
	}
	version := ComparisonFixtureVersion
	if e.hasCloseSkills() {
		version = ComparisonCloseVersion
		if p.SampleDate != "" && p.SampleProof == "" {
			// An existing preview conversation can continue using its original sample.
			version = legacyComparisonCloseVersion
		}
		if err := e.withCloseRights(ctx, func() error { return nil }); err != nil {
			return nil, errs.Unavailable("DATA_SOURCE_UNAVAILABLE", "收盘研究数据源当前不可用").Wrap(err)
		}
	}
	return e.buildSkillPlanForTurn(ctx, ws, stockComparisonSkill(version), subject,
		map[string]any{"window": float64(window), "comparison": p}, &launch, true)
}

// ComparisonFixtureSample uses the registered demo universe and the same
// market value ranking used by formal research. Live selection uses the
// provider's current roster instead of the partial local catalog.
func (e *Engine) ComparisonFixtureSample(ctx context.Context, subject dataservice.EntityRow) (ComparisonParams, error) {
	if !subject.Synthetic || !e.Synthetic || e.hasCloseSkills() {
		return ComparisonParams{}, errs.Unprocessable("INVALID_PARAMS", "当前标的不支持模拟同行样本")
	}
	at := e.Now()
	industry, err := industryOf(ctx, e, subject, at)
	if err != nil {
		return ComparisonParams{}, err
	}
	members, err := (Loader{e.Store}).Members(ctx, industry.Code, at)
	if err != nil {
		return ComparisonParams{}, err
	}
	peers, err := selectPeers(ctx, e, subject, members, at)
	if err != nil {
		return ComparisonParams{}, err
	}
	return ComparisonParams{OtherIDs: peers[:min(len(peers), dataservice.ComparisonPeerLimit)], SampleDate: at.In(cst).Format(time.DateOnly)}, nil
}

func stockComparisonSkill(version string) *Skill {
	section := sec("S2", "股价横向对比", true, []string{ComparisonTopic}, []string{KComp}, dataservice.ComparisonSubjectLimit, 150, "")
	collect := func(ctx context.Context, e *Engine, subject dataservice.EntityRow, p Params, at time.Time) (*Bundle, error) {
		return collectComparison(ctx, e, subject, p, at, version)
	}
	return &Skill{
		ID: "stock_analysis", Version: version, Title: "股价横向对比", Subjects: []string{"stock"},
		Sections: func(Params) []Section { return []Section{section} },
		Validate: func(subject dataservice.EntityRow, raw map[string]any) (Params, error) {
			if err := onlyKeys(raw, "window", "comparison"); err != nil {
				return Params{}, err
			}
			window, err := intParam(raw, "window", 20, ConversationWindows(ComparisonTopic))
			if err != nil {
				return Params{}, err
			}
			comparison, ok := raw["comparison"].(ComparisonParams)
			if !ok || len(comparison.OtherIDs) == 0 || len(comparison.OtherIDs) >= dataservice.ComparisonSubjectLimit {
				return Params{}, errs.Unprocessable("INVALID_PARAMS", "股价对比需要 2 至 5 只不同证券")
			}
			seen := map[uuid.UUID]bool{subject.ID: true}
			for _, id := range comparison.OtherIDs {
				if id == uuid.Nil || seen[id] {
					return Params{}, errs.Unprocessable("INVALID_PARAMS", "请使用不同的证券作为对比对象")
				}
				seen[id] = true
			}
			if comparison.SampleDate != "" {
				if _, err := time.Parse(time.DateOnly, comparison.SampleDate); err != nil || len(comparison.OtherIDs) > dataservice.ComparisonPeerLimit {
					return Params{}, errs.Unprocessable("INVALID_PARAMS", "同行样本范围无效")
				}
			}
			if comparison.SampleProof != "" || (version == ComparisonCloseVersion && comparison.SampleDate != "") {
				proof, err := hex.DecodeString(comparison.SampleProof)
				if err != nil || len(proof) != 32 || comparison.SampleDate == "" {
					return Params{}, errs.Unprocessable("INVALID_PARAMS", "同行样本证据无效")
				}
			}
			return Params{Window: window, Comparison: &comparison}, nil
		},
		Plan: func(ctx context.Context, e *Engine, subject dataservice.EntityRow, p *Params, at time.Time) (map[string]Availability, []string, error) {
			bundle, err := collect(ctx, e, subject, *p, at)
			if err != nil {
				return nil, nil, err
			}
			c := alignComparison(bundle, *p)
			state := avail()
			switch {
			case c.sampleInvalid:
				state = unavailable("缺少可核验的申万行业样本。请点名要比较的股票。")
			case !c.comparable():
				state = unavailable("没有至少两只股票在同一窗口内达到共同复权日线覆盖要求，请缩短窗口或更换标的。")
			case len(c.common) < p.Window || len(c.included) < len(c.ids):
				state = limited(fmt.Sprintf("所选 %d 个交易日内，有 %d 只股票的 %d 个交易日可对齐。", p.Window, len(c.included), len(c.common)))
			}
			return map[string]Availability{section.ID: state}, nil, nil
		},
		Collect: collect,
		Compute: func(b *builder, p Params) { b.stockComparison(p, section.ID) },
		ClaimsFromFacts: func(s *facts.Sheet, section Section) []check.Claim {
			var claims []check.Claim
			for _, f := range allowed(s, section) {
				if claim, ok := sentence(f); ok {
					claims = append(claims, claim)
				}
			}
			return claims
		},
		Name: func(subject dataservice.EntityRow, _ Params) string { return subject.Name + " · 股价横向对比" },
	}
}

func collectComparison(ctx context.Context, e *Engine, subject dataservice.EntityRow, p Params, at time.Time, version string) (*Bundle, error) {
	if p.Comparison == nil {
		return nil, errs.Unprocessable("INVALID_PARAMS", "股价对比范围缺失")
	}
	ids := p.Comparison.ids(subject.ID)
	entities, err := e.Store.Entities(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if entity, ok := entities[id]; !ok || entity.Kind != "stock" || entity.Synthetic != subject.Synthetic {
			return nil, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "该对比对象当前不可用")
		}
	}
	calendar, err := (Loader{e.Store}).Load(ctx, at, nil, []string{"ref.trading_calendar"})
	if err != nil {
		return nil, err
	}
	bundle := newBundle(at, subject, entities, calendar)
	bundle.Days = conversationDays(bundle, version)
	days := bundle.Window(p.Window)
	if len(days) == 0 {
		return newBundle(at, subject, entities, nil), nil
	}
	from, _ := time.Parse(time.DateOnly, days[0])
	until, _ := time.Parse(time.DateOnly, days[len(days)-1])
	quotes, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{"mkt.daily_bar"}, EntityIDs: ids, From: &from, To: &until, AsOf: at, ModelUse: true})
	if err != nil {
		return nil, err
	}
	records := slices.DeleteFunc(calendar, func(r dataservice.StoredRecord) bool { return !inWindow(r, "trade_date", days) })
	records = append(records, slices.DeleteFunc(quotes, func(r dataservice.StoredRecord) bool { return !inWindow(r, "trade_date", days) })...)
	if p.Comparison.SampleDate != "" {
		sampleDay, err := time.ParseInLocation(time.DateOnly, p.Comparison.SampleDate, cst)
		if err != nil || sampleDay.After(at) {
			return nil, errs.Unprocessable("INVALID_PARAMS", "同行样本日期无效")
		}
		if p.Comparison.SampleProof != "" {
			day, _ := time.Parse(time.DateOnly, p.Comparison.SampleDate)
			samples, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{dataservice.ComparisonSampleDataset}, EntityIDs: []uuid.UUID{subject.ID},
				From: &day, To: &day, AsOf: at, ModelUse: true})
			if err != nil {
				return nil, err
			}
			for _, sample := range samples {
				if sample.RawSHA256 != nil && *sample.RawSHA256 == p.Comparison.SampleProof {
					records = append(records, sample)
				}
			}
			return newBundle(at, subject, entities, records), nil
		}
		memberships, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{"ref.industry"}, EntityIDs: ids, AsOf: at, ModelUse: true})
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if record, ok := activeIndustry(slices.DeleteFunc(slices.Clone(memberships), func(r dataservice.StoredRecord) bool {
				return r.EntityID == nil || *r.EntityID != id
			}), sampleDay); ok {
				records = append(records, record)
			}
		}
		if version == legacyComparisonCloseVersion {
			day, _ := time.Parse(time.DateOnly, p.Comparison.SampleDate)
			values, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{"mkt.valuation_daily"}, EntityIDs: ids, From: &day, To: &day, AsOf: at, ModelUse: true})
			if err != nil {
				return nil, err
			}
			records = append(records, values...)
		}
	}
	return newBundle(at, subject, entities, records), nil
}

type comparisonCoverage struct {
	ids, included []uuid.UUID
	points        map[uuid.UUID]map[string]facts.Point
	quotes        map[uuid.UUID]map[string]dataservice.StoredRecord
	days, common  []string
	industry      string
	sampleInvalid bool
}

func (c comparisonCoverage) comparable() bool { return !c.sampleInvalid && len(c.common) >= 2 }

func alignComparison(b *Bundle, p Params) comparisonCoverage {
	c := comparisonCoverage{
		ids: p.Comparison.ids(b.Subject.ID), days: b.Window(p.Window),
		points: map[uuid.UUID]map[string]facts.Point{}, quotes: map[uuid.UUID]map[string]dataservice.StoredRecord{},
	}
	for _, id := range c.ids {
		c.quotes[id] = map[string]dataservice.StoredRecord{}
		c.points[id] = map[string]facts.Point{}
		for _, r := range b.Rows("mkt.daily_bar", id) {
			day := str(r, "trade_date")
			if !slices.Contains(c.days, day) {
				continue
			}
			c.quotes[id][day] = r
			closePrice, hasClose := dec(r, "close")
			factor, hasFactor := dec(r, "adj_factor")
			if !boolean(r, "suspended") && hasClose && closePrice.IsPositive() && hasFactor && factor.IsPositive() {
				c.points[id][day] = facts.Point{Date: day, EvidenceID: r.ID, Value: closePrice.Mul(factor)}
			}
		}
		n := len(c.points[id])
		if n >= 2 && n*5 >= p.Window*4 {
			c.included = append(c.included, id)
		}
	}
	if p.Comparison.SampleDate != "" {
		c.industry, c.sampleInvalid = comparisonIndustry(b, c.ids, *p.Comparison)
	}
	if len(c.included) < 2 || !slices.Contains(c.included, b.Subject.ID) {
		return c
	}
	for _, day := range c.days {
		shared := true
		for _, id := range c.included {
			if _, ok := c.points[id][day]; !ok {
				shared = false
				break
			}
		}
		if shared {
			c.common = append(c.common, day)
		}
	}
	if n := len(c.common); n < 2 || n*5 < p.Window*4 || !slices.Contains(b.Window(2), c.common[n-1]) {
		c.common = nil
	}
	return c
}

func comparisonIndustry(b *Bundle, ids []uuid.UUID, sample ComparisonParams) (name string, invalid bool) {
	if sample.SampleProof != "" {
		rows := b.Rows(dataservice.ComparisonSampleDataset, b.Subject.ID)
		if len(rows) != 1 || rows[0].RawSHA256 == nil || *rows[0].RawSHA256 != sample.SampleProof {
			return "", true
		}
		record := rows[0]
		peerCodes := make([]string, 0, len(sample.OtherIDs))
		for _, id := range sample.OtherIDs {
			peerCodes = append(peerCodes, b.Entities[id].Code)
		}
		name = str(record, "level2_name")
		if name == "" || str(record, "level2_code") == "" || str(record, "trade_date") != sample.SampleDate ||
			str(record, "peer_codes") != strings.Join(peerCodes, ",") {
			return "", true
		}
		return name, false
	}
	var code string
	for _, id := range ids {
		rows := b.Rows("ref.industry", id)
		if len(rows) != 1 || str(rows[0], "level2_code") == "" || str(rows[0], "level2_name") == "" {
			return "", true
		}
		if code == "" {
			code, name = str(rows[0], "level2_code"), str(rows[0], "level2_name")
		} else if code != str(rows[0], "level2_code") || name != str(rows[0], "level2_name") {
			return "", true
		}
		if !b.Subject.Synthetic {
			value, found := onTradingDay(b.Rows("mkt.valuation_daily", id), sample.SampleDate)
			if total, ok := dec(value, "total_mv"); !found || !ok || !total.IsPositive() {
				return "", true
			}
		}
	}
	return name, false
}

func (b *builder) stockComparison(p Params, section string) {
	c := alignComparison(b.B, p)
	if !c.comparable() {
		return
	}
	first, last := c.common[0], c.common[len(c.common)-1]
	period := first + " 至 " + last
	title := "同区间收盘价与涨跌幅"
	if c.industry != "" {
		title = "申万二级 · " + c.industry + "同行样本"
		b.S.Limit(fmt.Sprintf("同行样本按 %s 的申万二级行业归属和总市值选取。按总市值差距从小到大选至多 %d 只。", p.Comparison.SampleDate, dataservice.ComparisonPeerLimit))
	}
	note := fmt.Sprintf("%s，共 %d 个共同交易日。所有股票在首个共同交易日归一到 100。", period, len(c.common))
	b.S.Limit("区间涨跌幅按收盘价与复权因子计算。表中期初及期末收盘价为不复权价格。")
	table := facts.Table{Title: title, Columns: []string{"股票", "期初收盘价（元）", "期末收盘价（元）", "区间涨跌幅", "较基准差（百分点）", "有效日 / 窗口"}}
	for _, record := range b.B.Rows(dataservice.ComparisonSampleDataset, b.B.Subject.ID) {
		b.source(record)
		table.Evidence = append(table.Evidence, record.ID)
	}
	chart := facts.Series{ID: ComparisonTopic, Title: "股价走势对比", Kind: "line", X: "trade_date", Unit: "点", Section: section, Note: &note}
	var reference facts.Calc
	for _, id := range c.ids {
		entity := b.B.Entities[id]
		name := entity.Name + "（" + entity.Code + "）"
		coverage := fmt.Sprintf("%d / %d", len(c.points[id]), p.Window)
		for _, ds := range []string{"ref.industry", "mkt.valuation_daily"} {
			for _, record := range b.B.Rows(ds, id) {
				b.source(record)
				table.Evidence = append(table.Evidence, record.ID)
			}
		}
		if !slices.Contains(c.included, id) {
			b.sources(b.B.Rows("mkt.daily_bar", id))
			message := coverage + " · 覆盖不足"
			table.Rows = append(table.Rows, []*string{&name, nil, nil, nil, nil, &message})
			continue
		}
		points := make([]facts.Point, 0, len(c.common))
		for _, day := range c.common {
			points = append(points, c.points[id][day])
		}
		ret := facts.PeriodReturn(points, p.Window, "CNY/share")
		if !ret.OK() {
			return
		}
		group := ComparisonTopic
		if id != b.B.Subject.ID {
			// The peer's return is displayed in the table. Its comparison
			// claim uses F13 so that both legs are accessible in one place.
			group = "comparison_values"
		}
		retFact := b.S.Computed(group, entity.Name, "区间涨跌幅（复权）", period, ret, entity.Code+"|compare|return")
		retDisplay := retFact.Display + "%"
		var difference *string
		if id == b.B.Subject.ID {
			reference = ret
			name += " · 基准"
		} else {
			diff := facts.Excess(ret, reference)
			fact := b.S.Computed(ComparisonTopic, entity.Name, "相对"+b.B.Subject.Name+"的区间涨跌幅差", period, diff, entity.Code+"|compare|difference")
			// Finalize sorts the facts in place. Keep the table cell independent of that slice.
			display := fact.Display
			difference = &display
		}
		startQuote, endQuote := c.quotes[id][first], c.quotes[id][last]
		startPrice, endPrice := str(startQuote, "close"), str(endQuote, "close")
		table.Evidence = append(table.Evidence, startQuote.ID, endQuote.ID)
		table.Rows = append(table.Rows, []*string{&name, &startPrice, &endPrice, &retDisplay, difference, &coverage})
		line := facts.SeriesLine{Name: entity.Name + "（" + entity.Code + "）"}
		base := points[0].Value
		for _, day := range c.days {
			var value *string
			if slices.Contains(c.common, day) {
				point := c.points[id][day]
				v := point.Value.Div(base).Mul(decimal.NewFromInt(100)).StringFixed(2)
				value = &v
				chart.Evidence = append(chart.Evidence, point.EvidenceID)
				b.source(c.quotes[id][day])
			}
			line.Points = append(line.Points, [2]*string{&day, value})
		}
		chart.Lines = append(chart.Lines, line)
	}
	for _, record := range b.B.Global("ref.trading_calendar") {
		b.source(record)
		table.Evidence = append(table.Evidence, record.ID)
	}
	b.S.Tables[section] = append(b.S.Tables[section], table)
	b.S.Charts = append(b.S.Charts, chart)
}
