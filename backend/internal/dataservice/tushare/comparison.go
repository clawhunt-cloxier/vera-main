package tushare

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// CollectComparison publishes a bounded closing-price batch through the same
// canonical mappings as the full stock collector. The optional sample is
// picked against the full dated industry roster, before prices are inspected.
func (p *Provider) CollectComparison(ctx context.Context, anchor string, others []string, end time.Time) (ds.Provider, ds.ComparisonSample, error) {
	var sample ds.ComparisonSample
	codes := append([]string{anchor}, others...)
	if len(codes) > ds.ComparisonSubjectLimit {
		return nil, sample, classified(ds.ErrNotCovered, "comparison scope")
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if !instrument.MatchString(code) || seen[code] {
			return nil, sample, classified(ds.ErrNotCovered, "comparison scope")
		}
		seen[code] = true
	}
	local := end.In(marketZone)
	if local.Hour() < 15 {
		local = local.AddDate(0, 0, -1)
	}
	c := marketCollector{Source: p.source, ctx: ctx, code: anchor,
		from: local.AddDate(0, 0, -150).Format(marketDay), until: local.Format(marketDay)}
	if err := c.calendar(); err != nil {
		return nil, sample, err
	}
	if _, err := c.stockPrices(); err != nil {
		return nil, sample, err
	}
	var members map[string]Row
	if len(others) == 0 {
		var err error
		sample, members, err = c.comparisonPeers()
		if err != nil {
			return nil, sample, err
		}
		others = sample.Codes
	}
	for _, code := range append([]string{anchor}, others...) {
		identity, err := p.listedStock(ctx, code)
		if err != nil {
			return nil, sample, fmt.Errorf("verify comparison stock %s: %w", code, err)
		}
		entity := comparisonEntity(identity, members[code])
		if slices.Contains(p.codes, code) {
			entity.Attrs["tushare_research_subject"] = true
		}
		c.Entities = append(c.Entities, entity)
		if members != nil {
			record, err := marketMembership(members[code])
			if err != nil {
				return nil, sample, err
			}
			c.Records = append(c.Records, record)
		}
		if code == anchor {
			continue
		}
		c.code = code
		if _, err := c.stockPrices(); err != nil {
			return nil, sample, err
		}
	}
	return observation{p, c.Batch}, sample, nil
}

func (c *marketCollector) comparisonPeers() (ds.ComparisonSample, map[string]Row, error) {
	var sample ds.ComparisonSample
	day := c.latestVal.Values["trade_date"]
	if day == "" {
		return sample, nil, classified(ds.ErrEmptyValid, "daily_basic")
	}
	anchorRows, err := c.members("ts_code", c.code)
	if err != nil {
		return sample, nil, err
	}
	current, err := marketCurrent(anchorRows, day)
	if err != nil {
		return sample, nil, err
	}
	anchor, ok := current[c.code]
	if !ok {
		return sample, nil, classified(ds.ErrNotCovered, "index_member_all")
	}
	industry := anchor.Values["l2_code"]
	industryRows, err := c.members("l2_code", industry)
	if err != nil {
		return sample, nil, err
	}
	members, err := marketCurrent(industryRows, day)
	if err != nil {
		return sample, nil, err
	}
	if member, ok := members[c.code]; !ok || member.Values["in_date"] != anchor.Values["in_date"] {
		return sample, nil, marketBad("index_member_all", "inconsistent comparison membership")
	}
	maps.DeleteFunc(members, func(code string, _ Row) bool { return !instrument.MatchString(code) })
	valuationRows, err := c.rows("daily_basic", map[string]string{"trade_date": day}, marketValFields, 6000, false)
	if err != nil {
		return sample, nil, err
	}
	values, err := marketUnique(valuationRows, "ts_code")
	if err != nil {
		return sample, nil, err
	}
	if row, ok := values[c.code]; ok && !maps.Equal(row.Values, c.latestVal.Values) {
		return sample, nil, marketBad("daily_basic", "conflicting observations")
	}
	values[c.code] = c.latestVal
	peers := marketPeers(c.code, members, values)
	if len(peers) == 0 {
		return sample, nil, classified(ds.ErrEmptyValid, "industry comparison")
	}
	peers = peers[:min(len(peers), ds.ComparisonPeerLimit)]
	for _, code := range peers {
		rec, ok, err := marketValuation(values[code])
		if err != nil {
			return sample, nil, err
		}
		if !ok {
			return sample, nil, marketBad("daily_basic", "comparison market value")
		}
		c.Records = append(c.Records, rec)
	}
	date, err := marketDate(day)
	if err != nil {
		return sample, nil, err
	}
	sample = ds.ComparisonSample{Codes: peers, Date: date.Format(time.DateOnly), IndustryCode: industry}
	if err := c.archiveComparisonSample(&sample, date, anchor.Values["l2_name"], anchorRows, industryRows, valuationRows); err != nil {
		return ds.ComparisonSample{}, nil, err
	}
	return sample, members, nil
}

// Pin the original inputs, including other candidates. A later canonical
// industry or valuation revision must not change the selection on a follow-up.
func (c *marketCollector) archiveComparisonSample(sample *ds.ComparisonSample, date time.Time, industryName string, anchorRows, industryRows, valuationRows []Row) error {
	payload := map[string]any{
		"trade_date": sample.Date, "level2_code": sample.IndustryCode, "level2_name": industryName,
		"peer_codes": strings.Join(sample.Codes, ","),
	}
	pages := map[string]map[string]marketInput{}
	available := c.latestVal.ReceivedAt
	for api, rows := range map[string][]Row{"anchor_membership": anchorRows, "industry_membership": industryRows, "market_valuations": valuationRows} {
		pages[api] = map[string]marketInput{}
		for _, row := range rows {
			pages[api][row.RawRef] = marketInput{row.RawRef, row.RawSHA256, "response_ref#$.data.items"}
			if row.ReceivedAt.After(available) {
				available = row.ReceivedAt
			}
		}
	}
	doc, err := json.Marshal(struct {
		Version         int                               `json:"version"`
		Anchor          string                            `json:"anchor"`
		Sample          map[string]any                    `json:"sample"`
		Inputs          map[string]map[string]marketInput `json:"input_pages"`
		AnchorValuation marketInput                       `json:"anchor_valuation"`
	}{1, c.code, payload, pages, marketInput{c.latestVal.RawRef, c.latestVal.RawSHA256, c.latestVal.RawLocator}})
	if err != nil {
		return err
	}
	ref, hash, err := c.Archive(c.ctx, "comparison-sample", doc)
	if err != nil {
		return err
	}
	sample.ProofSHA256 = hash
	r := Row{ReceivedAt: available, RawRef: ref, RawSHA256: hash, RawLocator: "$.sample"}
	c.Records = append(c.Records, marketRecord(r, ds.ComparisonSampleDataset, c.code, c.code+"|"+sample.Date+"|"+hash,
		"vendor_derived", "L2", "申万 2021 版二级行业有效成员，按与基准的当日总市值差距排序，差距相同按证券代码排序", &date, payload))
	return nil
}

func comparisonEntity(identity, member Row) ds.Entity {
	v, m := identity.Values, member.Values
	_, exchange, _ := strings.Cut(v["ts_code"], ".")
	status := "listed"
	if strings.HasPrefix(v["name"], "ST") || strings.HasPrefix(v["name"], "*ST") {
		status = "st"
	}
	e := ds.Entity{Kind: "stock", Code: v["ts_code"], Name: v["name"], Exchange: exchange, Status: status,
		Attrs: map[string]any{}, Aliases: []string{v["symbol"], v["name"]}}
	for _, alias := range []string{v["fullname"], v["cnspell"]} {
		if alias != "" && !slices.Contains(e.Aliases, alias) {
			e.Aliases = append(e.Aliases, alias)
		}
	}
	if m != nil {
		e.Classification = "SW2021 " + m["l1_name"] + " > " + m["l2_name"]
		e.Attrs["industry_code"] = m["l2_code"]
		e.Attrs["index_code"] = m["l2_code"]
		e.Attrs["benchmark_code"] = marketBenchmark
		e.IsFinancial = m["l1_code"] == "801780.SI" || m["l1_code"] == "801790.SI"
	}
	return e
}

func marketMembership(row Row) (ds.Record, error) {
	v := row.Values
	start, err := marketDate(v["in_date"])
	if err != nil {
		return ds.Record{}, err
	}
	payload := map[string]any{"taxonomy": "SW2021", "level1_code": v["l1_code"], "level1_name": v["l1_name"],
		"level2_code": v["l2_code"], "level2_name": v["l2_name"], "valid_from": start.Format(time.DateOnly)}
	if out := v["out_date"]; out != "" {
		to, err := marketDate(out)
		if err != nil {
			return ds.Record{}, err
		}
		payload["valid_to"] = to.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	k := v["ts_code"] + "|" + v["in_date"] + "|" + v["l2_code"]
	return marketRecord(row, "ref.industry", v["ts_code"], k, "vendor_derived", "L2", "申万 2021 版分类", &start, payload), nil
}
