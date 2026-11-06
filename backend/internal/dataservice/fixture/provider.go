package fixture

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// Pinyin initials for the fictional names, so search by initials works in the demo.
var initials = map[string]string{
	"DM0001": "YSZZ", "DM0002": "YSKS", "DM0003": "YSQS", "DM0004": "YSYH", "DM0005": "YSCX", "DM0006": "YSWBB",
	"DM0007": "YSSBJ", "DM0008": "YSSBY", "DM0009": "YSSBB", "DM0010": "YSSBD", "DM0011": "YSSBW", "DM0012": "YSYHY",
}

type Provider struct {
	m    *Manifest
	once sync.Once
	g    *gen
}

func New(manifestPath string) (*Provider, error) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	return &Provider{m: m}, nil
}

func (p *Provider) ID() string                 { return "fixture" }
func (p *Provider) Name() string               { return "演示数据（模拟）" }
func (p *Provider) Synthetic() bool            { return true }
func (p *Provider) AllowModelProcessing() bool { return true }
func (p *Provider) Now() time.Time             { return p.m.AsOf }
func (p *Provider) Basis() map[string]string {
	return map[string]string{
		"flow.order_size":     "演示厂商分档口径 demo-vendor-os-v1：超大单 ≥100 万元，大单 20–100 万元，中单 4–20 万元，小单 <4 万元（虚构口径，仅用于演示）",
		"mkt.valuation_daily": "演示厂商估值口径：市值 / 最近年度归母净利润（虚构口径）",
	}
}

func (p *Provider) generate() *gen {
	p.once.Do(func() {
		g := &gen{m: p.m, bars: map[string][]bar{}, fin: map[string]map[string]finYear{}}
		g.calendar()
		g.securities()
		g.indexes()
		g.fundamentals()
		g.texts()
		g.flows()
		g.industries()
		p.g = g
	})
	return p.g
}

func (p *Provider) Records(context.Context) ([]dataservice.Record, error) {
	return p.generate().records, nil
}

func (p *Provider) Entities(context.Context) ([]dataservice.Entity, error) {
	var out []dataservice.Entity
	inds := map[string]Industry{}
	for _, ind := range p.m.Industries {
		inds[ind.ID] = ind
		out = append(out, dataservice.Entity{Kind: "industry", Code: ind.ID, Name: ind.Level2Name, Status: "active",
			Classification: ind.Taxonomy + " L2", Aliases: []string{ind.Level1Name, ind.Level2Name},
			Attrs: map[string]any{"level1_name": ind.Level1Name, "index_code": "IDX-" + ind.ID}})
	}
	for _, s := range p.m.Securities {
		short := strings.TrimSuffix(s.Code, ".DEMO")
		ind := inds[s.Industry]
		status := "listed"
		if profileFor(s).suspendLast > 0 {
			status = "suspended"
		}
		out = append(out, dataservice.Entity{Kind: "stock", Code: s.Code, Name: s.Name, Exchange: "DEMO", Status: status,
			IsFinancial: s.IsFinancial, Classification: ind.Taxonomy + " " + ind.Level1Name + " > " + ind.Level2Name,
			Aliases: []string{s.Name, short, initials[short]}, Attrs: map[string]any{"industry_code": s.Industry,
				"margin_eligible": s.MarginEligible, "profile": s.Profile}})
	}
	out = append(out, dataservice.Entity{Kind: "market", Code: "market", Name: "A 股全市场（演示）", Status: "active",
		Aliases: []string{"全市场", "A股", "大盘", "两市"}, Attrs: map[string]any{"index_code": "DM300.DEMO"}})
	return out, nil
}

func (p *Provider) Capabilities(context.Context) ([]dataservice.Capability, error) {
	seen := map[[2]string]bool{}
	var out []dataservice.Capability
	for _, r := range p.generate().records {
		k := [2]string{r.DatasetID, r.EntityCode}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, dataservice.Capability{DatasetID: r.DatasetID, EntityCode: r.EntityCode})
	}
	return out, nil
}

// industries emits SW-style classification records; DM0011 joins its industry 30 trading days before as_of.
func (g *gen) industries() {
	for _, ind := range g.m.Industries {
		for _, member := range ind.Members {
			code := member + ".DEMO"
			from := g.days[0]
			if member == "DM0011" {
				from = g.days[len(g.days)-30]
			}
			g.add(dataservice.Record{DatasetID: "ref.industry", EntityCode: code, RecordKey: code + "|" + ind.Taxonomy + "|" + from.Format("2006-01-02"),
				Period: dp(from), AvailableAt: at(from, 0), Origin: "vendor_derived", SourceTier: "L2", Payload: map[string]any{
					"taxonomy": ind.Taxonomy, "level1_code": ind.ID + "-L1", "level1_name": ind.Level1Name, "level2_code": ind.ID,
					"level2_name": ind.Level2Name, "valid_from": from.Format("2006-01-02"), "valid_to": nil}})
		}
	}
}

var _ dataservice.Provider = (*Provider)(nil)
