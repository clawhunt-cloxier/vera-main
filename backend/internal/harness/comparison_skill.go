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
	ComparisonTopic                = "stock_comparison"
	ComparisonCloseVersion         = "1.3.0-chat-compare-close.3"
	rankedComparisonCloseVersion   = "1.3.0-chat-compare-close.2"
	legacyComparisonCloseVersion   = "1.3.0-chat-compare-close.1"
	ComparisonFixtureVersion       = "1.3.0-chat-compare-fixture.2"
	legacyComparisonFixtureVersion = "1.3.0-chat-compare-fixture.1"
)

func IsComparisonVersion(version string) bool {
	return IsCloseComparisonVersion(version) || isFixtureComparisonVersion(version)
}

func IsCloseComparisonVersion(version string) bool {
	return version == ComparisonCloseVersion || version == rankedComparisonCloseVersion || version == legacyComparisonCloseVersion
}

func isFixtureComparisonVersion(version string) bool {
	return version == ComparisonFixtureVersion || version == legacyComparisonFixtureVersion
}

func IsCloseConversationVersion(version string) bool {
	return version == ConversationCloseVersion || IsCloseComparisonVersion(version)
}

func CloseConversationVersions() []string {
	return []string{ConversationCloseVersion, ComparisonCloseVersion, rankedComparisonCloseVersion, legacyComparisonCloseVersion}
}

// ComparisonParams stores the resolved comparison set. The subject of the
// plan is the reference stock. SampleProof identifies the original ranked
// industry roster and valuation inputs in a governed record.
type ComparisonParams struct {
	OtherIDs    []uuid.UUID `json:"other_ids"`
	SampleDate  string      `json:"sample_date,omitempty"`
	SampleProof string      `json:"sample_proof,omitempty"`
	SingleStock bool        `json:"single_stock,omitempty"`
	dataservice.ComparisonBenchmarks
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
	section := sec("S2", "股价横向对比", true, []string{ComparisonTopic}, []string{KComp}, dataservice.ComparisonLineLimit, 150, "")
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
			if !ok || len(comparison.OtherIDs) >= dataservice.ComparisonSubjectLimit ||
				(comparison.SingleStock && (len(comparison.OtherIDs) != 0 || comparison.SampleDate != "" || !comparison.HasIndexes())) ||
				(!comparison.SingleStock && len(comparison.OtherIDs) == 0) {
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
			if !comparison.Valid() ||
				comparison.HasIndexes() && version != ComparisonCloseVersion && version != ComparisonFixtureVersion {
				return Params{}, errs.Unprocessable("INVALID_PARAMS", "比较指数范围无效")
			}
			if comparison.SampleProof != "" || (IsCloseComparisonVersion(version) && version != legacyComparisonCloseVersion && comparison.SampleDate != "") {
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
			case !c.comparable() && p.Comparison.SingleStock:
				state = unavailable("股票和请求的指数未达到共同交易日覆盖要求，请缩短窗口或更换对比对象。")
			case !c.comparable():
				state = unavailable("没有至少两只股票在同一窗口内达到共同复权日线覆盖要求，请缩短窗口或更换标的。")
			case len(c.common) < p.Window || c.lineCount() < len(c.lines):
				state = limited(fmt.Sprintf("所选 %d 个交易日内，有 %d 个对象的 %d 个交易日可对齐。", p.Window, c.lineCount(), len(c.common)))
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
	if p.Comparison.SampleProof != "" {
		samples, err := comparisonSampleRecords(ctx, e.Store, subject.ID, *p.Comparison, at)
		if err != nil {
			return nil, err
		}
		records = append(records, samples...)
	} else if p.Comparison.SampleDate != "" || p.Comparison.IndustryIndex {
		sampleDay := until
		membershipIDs := []uuid.UUID{subject.ID}
		if p.Comparison.SampleDate != "" {
			sampleDay, err = time.Parse(time.DateOnly, p.Comparison.SampleDate)
			if err != nil || sampleDay.Format(time.DateOnly) > at.In(cst).Format(time.DateOnly) {
				return nil, errs.Unprocessable("INVALID_PARAMS", "同行样本日期无效")
			}
			membershipIDs = ids
		}
		memberships, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{"ref.industry"}, EntityIDs: membershipIDs, AsOf: at, ModelUse: true})
		if err != nil {
			return nil, err
		}
		for _, id := range membershipIDs {
			if record, ok := activeIndustry(slices.DeleteFunc(slices.Clone(memberships), func(r dataservice.StoredRecord) bool {
				return r.EntityID == nil || *r.EntityID != id
			}), sampleDay); ok {
				records = append(records, record)
			}
		}
		if version == legacyComparisonCloseVersion && p.Comparison.SampleDate != "" {
			values, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{"mkt.valuation_daily"}, EntityIDs: ids,
				From: &sampleDay, To: &sampleDay, AsOf: at, ModelUse: true})
			if err != nil {
				return nil, err
			}
			records = append(records, values...)
		}
	}
	if p.Comparison.HasIndexes() {
		codes := slices.Clone(p.Comparison.BroadIndexes)
		if p.Comparison.IndustryIndex {
			code, _ := comparisonIndustryIndex(newBundle(at, subject, entities, records))
			if code != "" {
				codes = append(codes, code)
			}
		}
		if len(codes) > 0 {
			indexes, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{"mkt.index_bar"}, IndexCodes: codes, From: &from, To: &until, AsOf: at, ModelUse: true})
			if err != nil {
				return nil, err
			}
			for _, index := range indexes {
				if inWindow(index, "trade_date", days) {
					records = append(records, index)
				}
			}
		}
	}
	bundle = newBundle(at, subject, entities, records)
	// Snapshot reconstruction must also retain the identities of stocks with no bars.
	bundle.Peers = p.Comparison.OtherIDs
	return bundle, nil
}

func comparisonSampleRecords(ctx context.Context, store *dataservice.Store, subject uuid.UUID, p ComparisonParams, at time.Time) ([]dataservice.StoredRecord, error) {
	day, err := time.Parse(time.DateOnly, p.SampleDate)
	if err != nil || p.SampleDate > at.In(cst).Format(time.DateOnly) {
		return nil, errs.Unprocessable("INVALID_PARAMS", "同行样本日期无效")
	}
	samples, err := store.Query(ctx, dataservice.Query{DatasetIDs: []string{dataservice.ComparisonSampleDataset}, EntityIDs: []uuid.UUID{subject},
		From: &day, To: &day, AsOf: at, ModelUse: true})
	return slices.DeleteFunc(samples, func(r dataservice.StoredRecord) bool {
		return r.RawSHA256 == nil || *r.RawSHA256 != p.SampleProof
	}), err
}

func comparisonIndustryIndex(b *Bundle) (code, name string) {
	rows := b.Rows(dataservice.ComparisonSampleDataset, b.Subject.ID)
	if len(rows) == 0 {
		rows = b.Rows("ref.industry", b.Subject.ID)
	}
	if len(rows) != 1 {
		return "", ""
	}
	code, name = str(rows[0], "level2_code"), str(rows[0], "level2_name")
	if code == "" || name == "" {
		return "", ""
	}
	if b.Subject.Synthetic {
		return "IDX-" + code, name
	}
	if !dataservice.IsComparisonIndustryCode(code) {
		return "", ""
	}
	return code, name
}

type comparisonLine struct {
	code, name, category, missing string
	points                        map[string]facts.Point
	quotes                        map[string]dataservice.StoredRecord
}

func (l comparisonLine) label() string {
	if l.code == "" {
		return l.name
	}
	return l.name + "（" + l.code + "）"
}

func newComparisonLine(code, name, category string, quotes []dataservice.StoredRecord, days []string) comparisonLine {
	line := comparisonLine{code: code, name: name, category: category, points: map[string]facts.Point{}, quotes: map[string]dataservice.StoredRecord{}}
	for _, r := range quotes {
		day := str(r, "trade_date")
		if !slices.Contains(days, day) {
			continue
		}
		line.quotes[day] = r
		closeValue, ok := dec(r, "close")
		if !ok || !closeValue.IsPositive() {
			continue
		}
		if category == "" {
			factor, hasFactor := dec(r, "adj_factor")
			if boolean(r, "suspended") || !hasFactor || !factor.IsPositive() {
				continue
			}
			closeValue = closeValue.Mul(factor)
		}
		line.points[day] = facts.Point{Date: day, EvidenceID: r.ID, Value: closeValue}
	}
	return line
}

type comparisonCoverage struct {
	lines         []comparisonLine
	days, common  []string
	industry      string
	sampleInvalid bool
}

func (c comparisonCoverage) comparable() bool { return !c.sampleInvalid && len(c.common) >= 2 }

func (c comparisonCoverage) lineCount() int {
	n := 0
	for _, line := range c.lines {
		if line.missing == "" {
			n++
		}
	}
	return n
}

func alignComparison(b *Bundle, p Params) comparisonCoverage {
	c := comparisonCoverage{days: b.Window(p.Window)}
	ids := p.Comparison.ids(b.Subject.ID)
	if p.Comparison.SampleDate != "" {
		c.industry, c.sampleInvalid = comparisonIndustry(b, ids, *p.Comparison)
	}
	meetsMinimum := func(n int) bool { return n >= 2 && n*5 >= p.Window*4 }
	sharedDays := func(days []string, line comparisonLine) []string {
		var result []string
		for _, day := range days {
			if _, ok := line.points[day]; ok {
				result = append(result, day)
			}
		}
		return result
	}
	validCommon := func(days []string) bool {
		return meetsMinimum(len(days)) && slices.Contains(b.Window(2), days[len(days)-1])
	}
	common := c.days
	for _, id := range ids {
		entity := b.Entities[id]
		line := newComparisonLine(entity.Code, entity.Name, "", b.Rows("mkt.daily_bar", id), c.days)
		if meetsMinimum(len(line.points)) {
			common = sharedDays(common, line)
		} else {
			line.missing = "覆盖不足"
		}
		c.lines = append(c.lines, line)
	}
	if c.lines[0].missing != "" || (!p.Comparison.SingleStock && c.lineCount() < 2) || !validCommon(common) {
		return c
	}
	addIndex := func(code, name, category string) {
		line := newComparisonLine(code, name, category, b.Index(code), c.days)
		switch {
		case code == "":
			line.missing = "缺少基准股票的申万二级行业映射"
		case len(line.points) == 0:
			line.missing = "暂无可用数据"
		case !meetsMinimum(len(line.points)):
			line.missing = "覆盖不足"
		default:
			shared := sharedDays(common, line)
			if validCommon(shared) {
				common = shared
			} else {
				line.missing = "共同交易日不足"
			}
		}
		c.lines = append(c.lines, line)
	}
	if p.Comparison.IndustryIndex {
		code, name := comparisonIndustryIndex(b)
		if name == "" {
			name = "申万二级"
		}
		addIndex(code, name, "行业指数")
	}
	for _, code := range p.Comparison.BroadIndexes {
		addIndex(code, dataservice.ComparisonBroadIndexName(code), "宽基指数")
	}
	if c.lineCount() >= 2 {
		c.common = common
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
	note := fmt.Sprintf("%s，共 %d 个共同交易日。所有曲线在首个共同交易日归一到 100。", period, len(c.common))
	b.S.Limit("股票涨跌幅使用复权收盘价，表中收盘价为不复权价格。")
	table := facts.Table{Title: title, Columns: []string{"股票", "期初收盘价（元）", "期末收盘价（元）", "区间涨跌幅", "较基准差（百分点）", "有效日 / 窗口"}}
	if p.Comparison.HasIndexes() {
		table.Columns[0], table.Columns[1], table.Columns[2] = "对比对象", "期初收盘", "期末收盘"
		b.S.Limit("指数涨跌幅使用收盘点位。百分点差为该行的涨跌幅减去基准股票的同期涨跌幅。")
	}
	for _, id := range p.Comparison.ids(b.B.Subject.ID) {
		for _, dataset := range []string{dataservice.ComparisonSampleDataset, "ref.industry", "mkt.valuation_daily"} {
			for _, record := range b.B.Rows(dataset, id) {
				b.source(record)
				table.Evidence = append(table.Evidence, record.ID)
			}
		}
	}
	chart := facts.Series{ID: ComparisonTopic, Title: "股价走势对比", Kind: "line", X: "trade_date", Unit: "点", Section: section, Note: &note}
	var reference facts.Calc
	for i, line := range c.lines {
		name := line.label()
		if line.category != "" {
			name += " · " + line.category
		}
		coverage := fmt.Sprintf("%d / %d", len(line.points), p.Window)
		if line.missing != "" {
			for _, record := range line.quotes {
				b.source(record)
			}
			message := coverage + " · " + line.missing
			table.Rows = append(table.Rows, []*string{&name, nil, nil, nil, nil, &message})
			continue
		}
		points := make([]facts.Point, 0, len(c.common))
		for _, day := range c.common {
			points = append(points, line.points[day])
		}
		unit, displayUnit, label := "CNY/share", "元", "区间涨跌幅（复权）"
		if line.category != "" {
			unit, displayUnit, label = "index_point", "点", "指数区间涨跌幅"
		}
		ret := facts.PeriodReturn(points, p.Window, unit)
		group := ComparisonTopic
		if i != 0 {
			group = "comparison_values" // the comparison claim exposes both legs of F13
		}
		retFact := b.S.Computed(group, line.name, label, period, ret, line.code+"|compare|return")
		retDisplay := retFact.Display + "%"
		seriesName := name
		var difference *string
		if i == 0 {
			reference = ret
			name += " · 基准"
		} else {
			diff := facts.Excess(ret, reference)
			fact := b.S.Computed(ComparisonTopic, line.name, "相对"+b.B.Subject.Name+"的区间涨跌幅差", period, diff, line.code+"|compare|difference")
			// Finalize sorts the facts in place. Keep the table cell independent of that slice.
			display := fact.Display
			difference = &display
		}
		startQuote, endQuote := line.quotes[first], line.quotes[last]
		startPrice, endPrice := str(startQuote, "close"), str(endQuote, "close")
		if p.Comparison.HasIndexes() {
			startPrice += " " + displayUnit
			endPrice += " " + displayUnit
		}
		table.Evidence = append(table.Evidence, startQuote.ID, endQuote.ID)
		table.Rows = append(table.Rows, []*string{&name, &startPrice, &endPrice, &retDisplay, difference, &coverage})
		series := facts.SeriesLine{Name: seriesName}
		base := points[0].Value
		for _, day := range c.days {
			var value *string
			if slices.Contains(c.common, day) {
				point := line.points[day]
				v := point.Value.Div(base).Mul(decimal.NewFromInt(100)).StringFixed(2)
				value = &v
				chart.Evidence = append(chart.Evidence, point.EvidenceID)
				b.source(line.quotes[day])
			}
			series.Points = append(series.Points, [2]*string{&day, value})
		}
		chart.Lines = append(chart.Lines, series)
	}
	for _, record := range b.B.Global("ref.trading_calendar") {
		b.source(record)
		table.Evidence = append(table.Evidence, record.ID)
	}
	b.S.Tables[section] = append(b.S.Tables[section], table)
	b.S.Charts = append(b.S.Charts, chart)
}
