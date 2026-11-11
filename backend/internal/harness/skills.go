package harness

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

const (
	KFact = "fact"
	KComp = "computed"
	KInf  = "inference"
	KAttr = "attributed"
	KScen = "scenario"
)

var factKinds = []string{KFact, KComp}
var allKinds = []string{KFact, KComp, KInf, KAttr, KScen}

// Section is one report section (HARNESS §4 SectionSpec).
type Section struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Required  bool     `json:"required"`
	Groups    []string `json:"groups"`
	Kinds     []string `json:"kinds"`
	MaxClaims int      `json:"max_claims"`
	MaxChars  int      `json:"max_chars"`
	Guide     string   `json:"guide"`
	Writer    bool     `json:"writer"`
}

// Availability of a section in a plan.
type Availability struct {
	State  string  `json:"availability"` // available | limited | unavailable
	Reason *string `json:"reason"`
}

// Params are the validated skill parameters; zero values are filled with defaults by the skill.
type Params struct {
	Window int    `json:"window,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Focus  string `json:"focus,omitempty"`
}

type Skill struct {
	ID       string
	Version  string
	Title    string
	Subjects []string
	Schema   map[string]any
	// Sections returns the ordered sections for the params (deep_research varies by mode).
	Sections func(p Params) []Section
	Validate func(subject dataservice.EntityRow, raw map[string]any) (Params, error)
	// Plan computes section availability from capability coverage; it may adjust params (e.g. window).
	Plan    func(ctx context.Context, e *Engine, subject dataservice.EntityRow, p *Params, asOf time.Time) (map[string]Availability, []string, error)
	Collect func(ctx context.Context, e *Engine, subject dataservice.EntityRow, p Params, asOf time.Time) (*Bundle, error)
	Compute func(b *builder, p Params)
	Name    func(subject dataservice.EntityRow, p Params) string
}

func avail() Availability { return Availability{State: "available"} }
func limited(r string) Availability {
	return Availability{State: "limited", Reason: &r}
}
func unavailable(r string) Availability { return Availability{State: "unavailable", Reason: &r} }

func intParam(raw map[string]any, k string, def int, allowed []int) (int, error) {
	v, ok := raw[k]
	if !ok || v == nil {
		return def, nil
	}
	f, ok := v.(float64)
	if !ok || f != float64(int(f)) || !slices.Contains(allowed, int(f)) {
		return 0, errs.Unprocessable("INVALID_PARAMS", fmt.Sprintf("参数 %s 必须是 %v 之一", k, allowed))
	}
	return int(f), nil
}

func onlyKeys(raw map[string]any, keys ...string) error {
	for k := range raw {
		if !slices.Contains(keys, k) {
			return errs.Unprocessable("INVALID_PARAMS", "不支持的参数 "+k)
		}
	}
	return nil
}

// sanitizeFocus keeps the user's focus as plain text: no control chars or markup, ≤200 runes.
func sanitizeFocus(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune("<>{}`", r) {
			return -1
		}
		return r
	}, s)
	return clip(strings.TrimSpace(s), 200)
}

// coverage helper: which datasets an entity has.
type cov map[uuid.UUID]map[string]bool

func (c cov) has(e uuid.UUID, ds ...string) bool {
	for _, d := range ds {
		if c[e][d] {
			return true
		}
	}
	return false
}

func listedTradingDays(ctx context.Context, e *Engine, subject dataservice.EntityRow, asOf time.Time) (int, error) {
	recs, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: []string{"mkt.daily_bar"}, EntityIDs: []uuid.UUID{subject.ID}, AsOf: asOf})
	return len(recs), err
}

func adjustWindow(listed int, p *Params, options []int, notes *[]string) {
	if listed >= p.Window {
		return
	}
	best := options[0]
	for _, o := range options {
		if o <= listed {
			best = o
		}
	}
	*notes = append(*notes, fmt.Sprintf("上市以来仅有 %d 个交易日行情，观察窗口由 %d 调整为 %d 个交易日；历史分位不适用。", listed, p.Window, best))
	p.Window = best
}

// selectPeers picks up to 8 industry peers closest in total market value (rule-based, never model-chosen).
func selectPeers(ctx context.Context, e *Engine, subject dataservice.EntityRow, members []uuid.UUID, asOf time.Time) ([]uuid.UUID, error) {
	from := asOf.AddDate(0, 0, -14)
	recs, err := e.Store.Query(ctx, dataservice.Query{ModelUse: true, DatasetIDs: []string{"mkt.valuation_daily"}, EntityIDs: members, From: &from, AsOf: asOf})
	if err != nil {
		return nil, err
	}
	latest := map[uuid.UUID]dataservice.StoredRecord{}
	for _, r := range recs {
		latest[*r.EntityID] = r
	}
	me, ok := latest[subject.ID]
	if !ok {
		return nil, nil
	}
	mv, _ := dec(me, "total_mv")
	var peers []uuid.UUID
	for id := range latest {
		if id != subject.ID {
			peers = append(peers, id)
		}
	}
	slices.SortFunc(peers, func(a, b uuid.UUID) int {
		va, _ := dec(latest[a], "total_mv")
		vb, _ := dec(latest[b], "total_mv")
		return va.Sub(mv).Abs().Cmp(vb.Sub(mv).Abs())
	})
	if len(peers) > 8 {
		peers = peers[:8]
	}
	return peers, nil
}

func industryOf(ctx context.Context, e *Engine, subject dataservice.EntityRow) (dataservice.EntityRow, error) {
	code, _ := subject.Attrs["industry_code"].(string)
	return e.Store.EntityByCode(ctx, code)
}

var stockData = []string{"ref.security", "ref.industry", "fin.statement", "fin.segment", "doc.periodic_text", "doc.announcement", "news.item",
	"mkt.daily_bar", "mkt.valuation_daily", "flow.margin", "flow.dragon_tiger", "flow.block_trade", "flow.connect_holding", "fund.holding",
	"flow.order_size", "corp.holder", "corp.insider_trade", "corp.pledge", "corp.unlock", "corp.dividend"}
var peerData = []string{"ref.security", "ref.industry", "fin.statement", "mkt.valuation_daily"}
var memberData = []string{"ref.security", "ref.industry", "fin.statement", "mkt.daily_bar", "mkt.valuation_daily", "flow.margin",
	"flow.dragon_tiger", "flow.block_trade", "fund.holding", "flow.order_size", "corp.insider_trade"}
var globalData = []string{"ref.trading_calendar", "mkt.index_bar", "flow.connect_turnover"}

func (e *Engine) collect(ctx context.Context, subject dataservice.EntityRow, asOf time.Time, groups map[uuid.UUID][]string, members, peers []uuid.UUID) (*Bundle, error) {
	recs, err := Loader{e.Store}.Load(ctx, asOf, groups, globalData)
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{subject.ID}
	for id := range groups {
		ids = append(ids, id)
	}
	ents, err := e.Store.Entities(ctx, ids)
	if err != nil {
		return nil, err
	}
	b := newBundle(asOf, subject, ents, recs)
	b.Members, b.Peers = members, peers
	return b, nil
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// Skills is the registry. Adding a skill does not change the engine.
func Skills() map[string]*Skill {
	return map[string]*Skill{"stock_analysis": stockAnalysis(), "capital_flow": capitalFlow(), "deep_research": deepResearch()}
}

func sec(id, title string, req bool, groups []string, kinds []string, maxClaims, maxChars int, guide string) Section {
	return Section{ID: id, Title: title, Required: req, Groups: groups, Kinds: kinds, MaxClaims: maxClaims, MaxChars: maxChars, Guide: guide, Writer: true}
}

func stockAnalysis() *Skill {
	sections := []Section{
		sec("S2", "公司概况", true, []string{"profile", "text.business_overview", "text.core_competence", "seg"}, []string{KFact, KComp, KAttr}, 6, 120,
			"概述主营业务、所属行业与主要产品。管理层表述写成“公司在年报中表示……”。"),
		sec("S3", "财务表现", true, []string{"fin.annual", "fin.interim"}, factKinds, 6, 120,
			"描述营收、归母净利、扣非净利、毛利率、净利率、ROE、资产负债率的水平与同比变化。同比不适用时用两期原值描述。"),
		sec("S4", "盈利质量与财务风险", true, []string{"fin.quality"}, []string{KFact, KComp, KInf}, 6, 120,
			"描述经营现金流与利润的关系、应收与存货变化、商誉、非经常性损益和有息负债。推断须克制并引用事实。"),
		sec("S5", "估值水平", false, []string{"val"}, factKinds, 6, 120,
			"只陈述当前倍数、历史分位、与可比公司中位数的高低和排位。禁止“低估/高估/合理/便宜/贵”。必须说明厂商估值口径。"),
		sec("S6", "行情与交易", true, []string{"mkt"}, factKinds, 6, 120, "描述区间涨跌幅、超额收益、最大回撤、成交额与换手率；不适用的指标说明原因。"),
		sec("S7", "资金面", false, []string{"flow"}, factKinds, 6, 120,
			"交易所披露数据可作为事实；厂商资金流必须写明“按厂商……口径”。禁止“主力”“游资”“机构抱团”等标签，不推断资金意图。"),
		sec("S8", "股东与公司事项", false, []string{"corp"}, factKinds, 6, 120, "描述股东户数变化、增减持、质押、解禁与分红，只陈述已披露事实。"),
		sec("S9", "近期动态", false, []string{"news"}, []string{KFact, KAttr}, 5, 120,
			"概括最近 30 天的公告与授权新闻事实；新闻只能写成“据某媒体报道”，不复述评级、目标价或观点。"),
		sec("S10", "风险提示与观察要点", true, []string{"fin.annual", "fin.quality", "mkt", "flow.margin", "corp", "val"}, []string{KInf, KScen}, 4, 140,
			"由前文事实推导风险项；情景最多 3 条，格式为“若[可观察条件]，则[已知指标]可能[方向]”，填写触发条件、观察指标和失效条件；禁止概率、价格与时间预测。"),
	}
	return &Skill{ID: "stock_analysis", Version: "1.0.0", Title: "个股分析", Subjects: []string{"stock"},
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"window": map[string]any{"enum": []int{20, 60, 120, 250}, "default": 60, "description": "行情与资金章节的观察窗口（交易日）"}}},
		Sections: func(Params) []Section { return sections },
		Validate: func(subject dataservice.EntityRow, raw map[string]any) (Params, error) {
			if err := onlyKeys(raw, "window"); err != nil {
				return Params{}, err
			}
			w, err := intParam(raw, "window", 60, []int{20, 60, 120, 250})
			return Params{Window: w}, err
		},
		Plan: func(ctx context.Context, e *Engine, s dataservice.EntityRow, p *Params, asOf time.Time) (map[string]Availability, []string, error) {
			c, err := e.Store.Coverage(ctx, []uuid.UUID{s.ID}, asOf)
			if err != nil {
				return nil, nil, err
			}
			cv := cov(c)
			var notes []string
			a := map[string]Availability{}
			a["S2"] = avail()
			if !cv.has(s.ID, "doc.periodic_text") {
				a["S2"] = unavailable("缺少定期报告正文")
			} else if !cv.has(s.ID, "fin.segment") {
				a["S2"] = limited("缺少分部收入数据")
			}
			switch {
			case s.IsFinancial:
				a["S3"], a["S4"] = unavailable("金融类公司报表结构不同，首版财务章节不适用"), unavailable("金融类公司报表结构不同，首版财务章节不适用")
			case !cv.has(s.ID, "fin.statement"):
				a["S3"], a["S4"] = unavailable("缺少财务报表数据"), unavailable("缺少财务报表数据")
			default:
				a["S3"], a["S4"] = avail(), avail()
			}
			a["S5"] = avail()
			if !cv.has(s.ID, "mkt.valuation_daily") {
				a["S5"] = unavailable("缺少估值数据")
			}
			a["S6"] = avail()
			if !cv.has(s.ID, "mkt.daily_bar") {
				a["S6"] = unavailable("缺少行情数据")
			} else {
				n, err := listedTradingDays(ctx, e, s, asOf)
				if err != nil {
					return nil, nil, err
				}
				before := p.Window
				adjustWindow(n, p, []int{20, 60, 120, 250}, &notes)
				if p.Window != before {
					a["S6"] = limited(notes[len(notes)-1])
				}
			}
			a["S7"] = avail()
			if !cv.has(s.ID, "flow.margin", "flow.dragon_tiger", "flow.block_trade", "flow.connect_holding", "fund.holding", "flow.order_size") {
				a["S7"] = unavailable("缺少资金面数据")
			}
			a["S8"] = avail()
			if !cv.has(s.ID, "corp.holder", "corp.insider_trade", "corp.pledge", "corp.unlock", "corp.dividend") {
				a["S8"] = unavailable("缺少股东与公司事项数据")
			}
			a["S9"] = avail()
			if !cv.has(s.ID, "news.item") {
				a["S9"] = limited("缺少授权新闻，仅使用公司公告")
				if !cv.has(s.ID, "doc.announcement") {
					a["S9"] = unavailable("缺少新闻与公告")
				}
			}
			a["S10"] = avail()
			return a, notes, nil
		},
		Collect: func(ctx context.Context, e *Engine, s dataservice.EntityRow, p Params, asOf time.Time) (*Bundle, error) {
			ind, err := industryOf(ctx, e, s)
			if err != nil {
				return nil, err
			}
			members, err := Loader{e.Store}.Members(ctx, ind.Code, asOf)
			if err != nil {
				return nil, err
			}
			peers, err := selectPeers(ctx, e, s, members, asOf)
			if err != nil {
				return nil, err
			}
			groups := map[uuid.UUID][]string{s.ID: stockData}
			for _, pe := range peers {
				groups[pe] = peerData
			}
			return e.collect(ctx, s, asOf, groups, members, peers)
		},
		Compute: func(b *builder, p Params) {
			s := b.B.Subject.ID
			b.profile(s, "profile")
			b.periodicText(s, []string{"business_overview", "core_competence"}, 3, 300, 1)
			b.segments(s, "seg", "S2")
			if !b.B.Subject.IsFinancial {
				b.financials(s, 4, "S3")
			}
			b.valuation(s, b.B.Peers, "val")
			b.market(s, p.Window, "mkt", "S6")
			b.margin(s, p.Window, "flow.margin", "S7")
			b.seats(s, p.Window, "flow.seats", "S7")
			b.holdings(s, "flow.hold")
			b.vendorFlow(s, p.Window, "flow.vendor")
			b.corp(s, "corp")
			b.news(s, "news")
		},
		Name: func(s dataservice.EntityRow, p Params) string { return s.Name + " 个股分析" },
	}
}

func capitalFlow() *Skill {
	sections := []Section{
		sec("S2", "区间行情与成交", true, []string{"mkt"}, factKinds, 5, 100, "描述区间涨跌幅、超额收益与成交变化。"),
		sec("S3", "杠杆资金", true, []string{"flow.margin"}, factKinds, 5, 100, "描述融资余额与融资买入额的变化、占流通市值比例或净增最多的对象。"),
		sec("S4", "交易所席位与大宗交易", false, []string{"flow.seats"}, factKinds, 5, 100, "描述龙虎榜上榜与席位买卖额、大宗交易成交额与成交价相对收盘价，只陈述披露数据。"),
		sec("S5", "陆股通与公募持仓", false, []string{"flow.hold"}, factKinds, 5, 100, "描述最近两个季度陆股通持股与公募基金持仓的变化，注明季度披露。不写“北向资金净买入”。"),
		sec("S6", "股东与产业资本", false, []string{"corp"}, factKinds, 5, 100, "描述增减持、解禁、股东户数等已披露事项。"),
		sec("S7", "厂商资金流（口径数据）", false, []string{"flow.vendor"}, factKinds, 5, 100, "每句必须写明“按厂商……口径”，禁止写成“主力资金”“北向资金”。"),
		sec("S8", "观察要点", true, []string{"mkt", "flow.margin", "flow.hold", "flow.vendor"}, []string{KInf, KScen}, 3, 140,
			"条件化情景，格式同个股分析；观察指标必须来自本报告的事实；禁止推断资金意图、概率与价格。"),
	}
	return &Skill{ID: "capital_flow", Version: "1.0.0", Title: "资金动向", Subjects: []string{"stock", "industry", "market"},
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"window": map[string]any{"enum": []int{5, 20, 60, 120}, "default": 20, "description": "观察窗口（交易日）"}}},
		Sections: func(Params) []Section { return sections },
		Validate: func(subject dataservice.EntityRow, raw map[string]any) (Params, error) {
			if err := onlyKeys(raw, "window"); err != nil {
				return Params{}, err
			}
			w, err := intParam(raw, "window", 20, []int{5, 20, 60, 120})
			return Params{Window: w}, err
		},
		Plan: func(ctx context.Context, e *Engine, s dataservice.EntityRow, p *Params, asOf time.Time) (map[string]Availability, []string, error) {
			a := map[string]Availability{"S2": avail(), "S8": avail()}
			var notes []string
			switch s.Kind {
			case "stock":
				c, err := e.Store.Coverage(ctx, []uuid.UUID{s.ID}, asOf)
				if err != nil {
					return nil, nil, err
				}
				cv := cov(c)
				if !cv.has(s.ID, "mkt.daily_bar") {
					a["S2"] = unavailable("缺少行情数据")
				} else if n, err := listedTradingDays(ctx, e, s, asOf); err == nil {
					before := p.Window
					adjustWindow(n, p, []int{5, 20, 60, 120}, &notes)
					if before != p.Window {
						a["S2"] = limited(notes[len(notes)-1])
					}
				}
				a["S3"] = avail()
				if !cv.has(s.ID, "flow.margin") {
					a["S3"] = unavailable("该股不是融资融券标的或缺少融资融券数据")
				}
				a["S4"], a["S5"], a["S6"], a["S7"] = avail(), avail(), avail(), avail()
				if !cv.has(s.ID, "flow.dragon_tiger", "flow.block_trade") {
					a["S4"] = unavailable("缺少龙虎榜与大宗交易数据")
				}
				if !cv.has(s.ID, "flow.connect_holding", "fund.holding") {
					a["S5"] = unavailable("缺少陆股通与基金持仓数据")
				}
				if !cv.has(s.ID, "corp.insider_trade", "corp.unlock", "corp.holder", "corp.dividend") {
					a["S6"] = unavailable("缺少股东与产业资本数据")
				}
				if !cv.has(s.ID, "flow.order_size") {
					a["S7"] = unavailable("缺少厂商资金流数据")
				}
			case "industry", "market":
				code := s.Code
				if s.Kind == "market" {
					code = ""
				}
				members, err := Loader{e.Store}.Members(ctx, code, asOf)
				if err != nil {
					return nil, nil, err
				}
				c, err := e.Store.Coverage(ctx, members, asOf)
				if err != nil {
					return nil, nil, err
				}
				cv := cov(c)
				any := func(ds ...string) bool {
					return slices.ContainsFunc(members, func(m uuid.UUID) bool { return cv.has(m, ds...) })
				}
				a["S3"], a["S4"], a["S5"], a["S6"], a["S7"] = avail(), avail(), avail(), avail(), avail()
				if !any("flow.margin") {
					a["S3"] = unavailable("成分股均无融资融券数据")
				}
				if !any("flow.dragon_tiger", "flow.block_trade") {
					a["S4"] = unavailable("缺少龙虎榜与大宗交易数据")
				}
				if !any("fund.holding") {
					a["S5"] = unavailable("缺少基金持仓数据")
				}
				if s.Kind == "market" {
					a["S6"] = unavailable("全市场模式不汇总股东与产业资本")
					ok, err := e.Store.HasGlobal(ctx, "flow.connect_turnover", asOf)
					if err != nil {
						return nil, nil, err
					}
					if !ok && a["S5"].State == "available" {
						a["S5"] = limited("缺少陆股通每日成交数据，仅使用季度基金持仓")
					}
				} else if !any("corp.insider_trade") {
					a["S6"] = unavailable("缺少股东增减持数据")
				}
				if !any("flow.order_size") {
					a["S7"] = unavailable("缺少厂商资金流数据")
				}
			}
			return a, notes, nil
		},
		Collect: func(ctx context.Context, e *Engine, s dataservice.EntityRow, p Params, asOf time.Time) (*Bundle, error) {
			switch s.Kind {
			case "stock":
				return e.collect(ctx, s, asOf, map[uuid.UUID][]string{s.ID: stockData}, nil, nil)
			default:
				code := s.Code
				if s.Kind == "market" {
					code = ""
				}
				members, err := Loader{e.Store}.Members(ctx, code, asOf)
				if err != nil {
					return nil, err
				}
				groups := map[uuid.UUID][]string{}
				for _, m := range members {
					groups[m] = memberData
				}
				b, err := e.collect(ctx, s, asOf, groups, members, nil)
				if err != nil {
					return nil, err
				}
				if s.Kind == "market" {
					inds, err := e.Store.EntitiesByKind(ctx, "industry")
					if err != nil {
						return nil, err
					}
					for _, ind := range inds {
						b.Entities[ind.ID] = ind
					}
				}
				return b, nil
			}
		},
		Compute: func(b *builder, p Params) {
			s := b.B.Subject
			switch s.Kind {
			case "stock":
				b.market(s.ID, p.Window, "mkt", "S2")
				b.margin(s.ID, p.Window, "flow.margin", "S3")
				b.seats(s.ID, p.Window, "flow.seats", "S4")
				b.holdings(s.ID, "flow.hold")
				b.corp(s.ID, "corp")
				b.vendorFlow(s.ID, p.Window, "flow.vendor")
			case "industry":
				label := s.Name + "（" + s.Code + "）"
				b.industryValuation(s, b.B.Members, p.Window, "mkt", "S2")
				b.groupMargin(label, s.Code, b.B.Members, p.Window, "flow.margin")
				b.groupEvents(label, s.Code, b.B.Members, p.Window, "flow.seats")
				b.groupHoldings(label, s.Code, b.B.Members, "flow.hold")
				b.groupInsider(label, b.B.Members, p.Window, "corp")
				b.groupVendor(label, s.Code, b.B.Members, p.Window, "flow.vendor")
				b.composition(s, b.B.Members, "ind.composition", "")
			case "market":
				label := s.Name
				b.marketOverview(p.Window, "mkt", "S2")
				b.groupMargin(label, "market", b.B.Members, p.Window, "flow.margin")
				var inds []dataservice.EntityRow
				membersOf := map[uuid.UUID][]uuid.UUID{}
				for _, en := range b.B.Entities {
					if en.Kind == "industry" {
						inds = append(inds, en)
					}
				}
				slices.SortFunc(inds, func(a, c dataservice.EntityRow) int { return strings.Compare(a.Code, c.Code) })
				for _, m := range b.B.Members {
					if code, ok := b.B.Entities[m].Attrs["industry_code"].(string); ok {
						for _, ind := range inds {
							if ind.Code == code {
								membersOf[ind.ID] = append(membersOf[ind.ID], m)
							}
						}
					}
				}
				b.industryMargins(inds, membersOf, p.Window, "flow.margin")
				b.groupEvents(label, "market", b.B.Members, p.Window, "flow.seats")
				b.groupHoldings(label, "market", b.B.Members, "flow.hold")
				b.groupVendor(label, "market", b.B.Members, p.Window, "flow.vendor")
				if len(b.B.Global("flow.connect_turnover")) == 0 {
					b.S.Limit("演示数据不含陆股通每日成交总额；2024 年 8 月起个股陆股通持股改为按季度披露。")
				}
			}
		},
		Name: func(s dataservice.EntityRow, p Params) string {
			return fmt.Sprintf("%s 资金动向（近 %d 个交易日）", s.Name, p.Window)
		},
	}
}

func drCompany(prefixShift int) []Section {
	id := func(n int) string { return fmt.Sprintf("S%d", n+prefixShift) }
	return []Section{
		sec(id(2), "业务与商业模式", true, []string{"profile", "text.business_overview", "seg"}, []string{KFact, KComp, KAttr}, 8, 150,
			"说明主营业务、商业模式与收入构成。管理层表述写成“公司在年报中表示……”。"),
		sec(id(3), "分业务与产品", false, []string{"seg", "text.mdna"}, []string{KFact, KComp, KAttr}, 8, 150, "描述分业务收入、占比与毛利率。"),
		sec(id(4), "财务轨迹", true, []string{"fin.annual", "fin.interim", "fin.quality"}, []string{KFact, KComp, KInf}, 8, 150,
			"描述最近年度的收入、利润、利润率、现金流与资产负债的水平和变化；推断须克制。"),
		sec(id(5), "行业位置与可比公司", true, []string{"val", "position", "profile"}, factKinds, 8, 150,
			"陈述与可比公司的倍数中位数比较与排位、在行业成分中的排位；可比公司由规则选出。禁止“低估/高估”。"),
		sec(id(6), "管理层表述与兑现", false, []string{"text.outlook_plan", "fin.annual"}, []string{KAttr, KFact, KComp}, 8, 150,
			"只做事实比对：“某年报计划……，实际……”。不评价管理层。"),
		sec(id(7), "增长驱动（归属陈述）", false, []string{"text.mdna", "text.core_competence"}, []string{KAttr}, 8, 150,
			"全部写成归属陈述：“公司在某报告中表示……”。"),
		sec(id(8), "主要风险", true, []string{"text.risk_factors", "fin.quality", "fin.annual"}, []string{KAttr, KFact, KComp, KInf}, 8, 150,
			"列出定期报告披露的风险因素（归属陈述）与财务风险事实。"),
		sec(id(9), "外部观点（归属陈述）", false, []string{"research"}, []string{KAttr}, 8, 150, "只陈述“某机构认为”。"),
		sec(id(10), "情景与观察要点", true, []string{"fin.annual", "fin.quality", "val", "mkt"}, []string{KInf, KScen}, 4, 150,
			"条件化情景最多 4 条，填写触发条件、观察指标和失效条件；禁止概率、价格与时间预测。"),
	}
}

func drIndustry() []Section {
	return []Section{
		sec("S2", "行业构成", true, []string{"ind.composition"}, factKinds, 8, 150, "描述成分股数量、总市值与头部公司占比，以及成分调整。"),
		sec("S3", "行业财务汇总", true, []string{"ind.fin"}, []string{KFact, KComp, KInf}, 8, 150, "描述成分股收入、利润合计及同口径增速、毛利率与盈利公司数量。"),
		sec("S4", "分化与集中度", true, []string{"ind.concentration"}, []string{KFact, KComp, KInf}, 8, 150, "描述 CR5、增速中位数与增速为正的公司数量。"),
		sec("S5", "估值与表现", false, []string{"ind.valuation"}, factKinds, 8, 150, "陈述倍数中位数与指数表现，说明厂商估值口径。禁止“低估/高估”。"),
		sec("S6", "资金与持仓", false, []string{"ind.flow"}, factKinds, 8, 150, "描述融资余额合计变化与基金持有家次变化。"),
		sec("S7", "重点公司概览", true, []string{"ind.top"}, factKinds, 8, 200, "按营收前 5 名，每家一句事实摘要。"),
		sec("S8", "风险与观察要点", true, []string{"ind.fin", "ind.concentration", "ind.valuation", "ind.flow"}, []string{KInf, KScen}, 4, 150,
			"风险项与条件化情景；禁止概率、价格与时间预测。"),
	}
}

func deepResearch() *Skill {
	sectionsFor := func(p Params) []Section {
		switch p.Mode {
		case "industry":
			return drIndustry()
		case "company_industry":
			ind := drIndustry()[:3]
			out := append([]Section{}, ind...)
			out = append(out, drCompany(3)...)
			out = append(out, sec("SX", "公司在行业中的位置", false, []string{"position"}, factKinds, 6, 150, "陈述公司在行业成分中的营收、利润与增速排位，全部为计算事实。"))
			return out
		}
		return drCompany(0)
	}
	return &Skill{ID: "deep_research", Version: "1.0.0", Title: "深度研究", Subjects: []string{"stock", "industry"},
		Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"mode":  map[string]any{"enum": []string{"company", "industry", "company_industry"}, "description": "由研究对象推断；个股可选 company_industry"},
			"focus": map[string]any{"type": "string", "maxLength": 200, "description": "关注点，仅影响写作提示，不扩大数据范围"}}},
		Sections: sectionsFor,
		Validate: func(subject dataservice.EntityRow, raw map[string]any) (Params, error) {
			if err := onlyKeys(raw, "mode", "focus"); err != nil {
				return Params{}, err
			}
			p := Params{Mode: "company"}
			if subject.Kind == "industry" {
				p.Mode = "industry"
			}
			if m, ok := raw["mode"].(string); ok && m != "" {
				p.Mode = m
			}
			if (subject.Kind == "industry") != (p.Mode == "industry") || !slices.Contains([]string{"company", "industry", "company_industry"}, p.Mode) {
				return p, errs.Unprocessable("INVALID_PARAMS", "研究模式与研究对象不匹配")
			}
			if f, ok := raw["focus"].(string); ok {
				p.Focus = sanitizeFocus(f)
			}
			return p, nil
		},
		Plan: func(ctx context.Context, e *Engine, s dataservice.EntityRow, p *Params, asOf time.Time) (map[string]Availability, []string, error) {
			a := map[string]Availability{}
			for _, sct := range sectionsFor(*p) {
				a[sct.ID] = avail()
			}
			if p.Mode != "company" {
				code := s.Code
				if s.Kind == "stock" {
					ind, err := industryOf(ctx, e, s)
					if err != nil {
						return nil, nil, err
					}
					code = ind.Code
				}
				members, err := Loader{e.Store}.Members(ctx, code, asOf)
				if err != nil {
					return nil, nil, err
				}
				c, err := e.Store.Coverage(ctx, members, asOf)
				if err != nil {
					return nil, nil, err
				}
				n := 0
				for _, m := range members {
					if cov(c).has(m, "fin.statement") {
						n++
					}
				}
				if n < 2 {
					for _, id := range []string{"S3", "S4"} {
						a[id] = unavailable("有财务数据的成分股不足 2 家")
					}
				}
				if p.Mode == "industry" {
					if len(members) < 5 {
						a["S7"] = limited("成分股少于 5 家")
					}
					return a, []string{"已接入数据不含行业数据库（市场规模、政策、技术路线），相关内容不生成。"}, nil
				}
			}
			shift := 0
			if p.Mode == "company_industry" {
				shift = 3
			}
			id := func(n int) string { return fmt.Sprintf("S%d", n+shift) }
			c, err := e.Store.Coverage(ctx, []uuid.UUID{s.ID}, asOf)
			if err != nil {
				return nil, nil, err
			}
			cv := cov(c)
			if !cv.has(s.ID, "doc.periodic_text") {
				a[id(2)] = unavailable("缺少定期报告正文")
				a[id(7)] = unavailable("缺少定期报告正文")
				a[id(8)] = unavailable("缺少定期报告正文")
			}
			if !cv.has(s.ID, "fin.segment") {
				a[id(3)] = unavailable("缺少分部收入数据")
			}
			if s.IsFinancial || !cv.has(s.ID, "fin.statement") {
				r := "缺少财务报表数据"
				if s.IsFinancial {
					r = "金融类公司报表结构不同，首版财务章节不适用"
				}
				a[id(4)], a[id(5)], a[id(10)] = unavailable(r), unavailable(r), unavailable(r)
			}
			a[id(6)] = limited("已接入数据不含业绩说明会纪要，仅使用定期报告中的经营计划段落")
			a[id(7)] = limited("已接入数据不含业绩说明会纪要，仅使用定期报告正文")
			a[id(9)] = unavailable("已接入数据不含研报与一致预期")
			if p.Mode == "company_industry" && s.IsFinancial {
				a["SX"] = unavailable("金融类公司报表结构不同")
			}
			return a, nil, nil
		},
		Collect: func(ctx context.Context, e *Engine, s dataservice.EntityRow, p Params, asOf time.Time) (*Bundle, error) {
			groups := map[uuid.UUID][]string{}
			var members, peers []uuid.UUID
			indCode := s.Code
			if s.Kind == "stock" {
				ind, err := industryOf(ctx, e, s)
				if err != nil {
					return nil, err
				}
				indCode = ind.Code
			}
			members, err := Loader{e.Store}.Members(ctx, indCode, asOf)
			if err != nil {
				return nil, err
			}
			if p.Mode != "company" {
				for _, m := range members {
					groups[m] = memberData
				}
			}
			if s.Kind == "stock" {
				if peers, err = selectPeers(ctx, e, s, members, asOf); err != nil {
					return nil, err
				}
				for _, pe := range peers {
					groups[pe] = union(groups[pe], peerData)
				}
				for _, m := range members {
					groups[m] = union(groups[m], []string{"fin.statement", "ref.industry"})
				}
				groups[s.ID] = union(groups[s.ID], stockData)
			}
			b, err := e.collect(ctx, s, asOf, groups, members, peers)
			if err != nil {
				return nil, err
			}
			if s.Kind == "stock" {
				ind, err := e.Store.EntityByCode(ctx, indCode)
				if err == nil {
					b.Entities[ind.ID] = ind
				}
			}
			return b, nil
		},
		Compute: func(b *builder, p Params) {
			s := b.B.Subject
			if p.Mode != "company" {
				ind := s
				if s.Kind == "stock" {
					for _, en := range b.B.Entities {
						if en.Kind == "industry" {
							ind = en
						}
					}
				}
				b.composition(ind, b.B.Members, "ind.composition", "S2")
				b.industryFin(ind, b.B.Members, "ind.fin", "ind.concentration", "ind.top", "S7")
				if p.Mode == "industry" {
					b.industryValuation(ind, b.B.Members, 60, "ind.valuation", "S5")
					b.groupMargin(ind.Name+"（"+ind.Code+"）", ind.Code, b.B.Members, 20, "ind.flow")
					b.groupHoldings(ind.Name+"（"+ind.Code+"）", ind.Code, b.B.Members, "ind.flow")
					b.S.Limit("已接入数据不含行业数据库（市场规模、政策、技术路线），相关内容不生成。")
					return
				}
				b.position(s.ID, ind, b.B.Members, "position")
			}
			b.profile(s.ID, "profile")
			b.periodicText(s.ID, []string{"business_overview", "mdna", "risk_factors", "core_competence"}, 6, 600, 1)
			b.periodicText(s.ID, []string{"outlook_plan"}, 4, 600, 2)
			b.segments(s.ID, "seg", "S3")
			if !s.IsFinancial {
				b.financials(s.ID, 5, fmt.Sprintf("S%d", 4+map[bool]int{true: 3}[p.Mode == "company_industry"]))
			}
			b.valuation(s.ID, b.B.Peers, "val")
			if p.Mode == "company" {
				ind := dataservice.EntityRow{}
				for _, en := range b.B.Entities {
					if en.Kind == "industry" {
						ind = en
					}
				}
				if ind.Name == "" {
					if code, ok := s.Attrs["industry_code"].(string); ok {
						ind = dataservice.EntityRow{Code: code, Name: "所属行业"}
					}
				}
				b.position(s.ID, ind, b.B.Members, "position")
			}
			b.market(s.ID, 60, "mkt", "")
		},
		Name: func(s dataservice.EntityRow, p Params) string {
			switch p.Mode {
			case "industry":
				return s.Name + " 行业深度研究"
			case "company_industry":
				return s.Name + " 公司与行业深度研究"
			}
			return s.Name + " 深度研究"
		},
	}
}

var _ = facts.KindText
