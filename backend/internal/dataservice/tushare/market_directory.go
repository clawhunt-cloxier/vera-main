package tushare

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const marketMemberFields = "ts_code name l1_code l1_name l2_code l2_name l3_code l3_name in_date out_date is_new"
const securityFields = "ts_code symbol name fullname cnspell market exchange list_status list_date"

// TuShare defaults to latest memberships. Both partitions are needed to
// identify which SW2021 classification actually applied on the closing day.
func (c *marketCollector) members(field, value string) ([]Row, error) {
	var all []Row
	for _, newFlag := range []string{"Y", "N"} {
		rs, err := c.rows("index_member_all", map[string]string{field: value, "is_new": newFlag}, marketMemberFields, 1500, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			v := r.Values
			if _, err = marketDate(v["in_date"]); err != nil {
				return nil, err
			}
			if out := v["out_date"]; out != "" {
				if _, err = marketDate(out); err != nil || out <= v["in_date"] {
					return nil, marketBad("index_member_all", "out_date")
				}
			}
			if v["ts_code"] == "" || v["l1_code"] == "" || v["l1_name"] == "" || v["l2_code"] == "" || v["l2_name"] == "" {
				return nil, marketBad("index_member_all", "membership")
			}
			if v["in_date"] <= c.day {
				all = append(all, r)
			}
		}
	}
	return all, nil
}

func marketCurrent(all []Row, day string) (map[string]Row, error) {
	m := map[string]Row{}
	for _, r := range all {
		v := r.Values
		if v["in_date"] > day || v["out_date"] != "" && v["out_date"] <= day {
			continue
		}
		code := v["ts_code"]
		prev, ok := m[code]
		if ok && v["l2_code"] != prev.Values["l2_code"] {
			return nil, marketBad("index_member_all", "overlapping industries")
		}
		if !ok || v["in_date"] > prev.Values["in_date"] {
			m[code] = r
		}
	}
	return m, nil
}

func (c *marketCollector) directory() error {
	// Capital and eligibility belong to a common session, even when daily_basic lags the calendar.
	day := cmp.Or(c.latestVal.Values["trade_date"], c.day)
	subjectHistory, err := c.members("ts_code", c.code)
	if err != nil {
		return err
	}
	current, err := marketCurrent(subjectHistory, day)
	if err != nil {
		return err
	}
	subject, ok := current[c.code]
	if !ok {
		return fmt.Errorf("TuShare: no SW2021 industry membership at close for %s", c.code)
	}
	ind := subject.Values["l2_code"]
	industryMembers, err := c.members("l2_code", ind)
	if err != nil {
		return err
	}
	current, err = marketCurrent(industryMembers, day)
	if err != nil {
		return err
	}
	if member, ok := current[c.code]; !ok || member.Values["in_date"] != subject.Values["in_date"] {
		return marketBad("index_member_all", "inconsistent member lists")
	}
	vals := map[string]Row{}
	if c.latestVal.Values["trade_date"] != "" {
		// Doc 32 permits up to 6000 stocks per request. Rows follows the
		// next page when the server returns a full page or has_more.
		raw, err := c.rows("daily_basic", map[string]string{"trade_date": day}, marketValFields, 6000, false)
		if err != nil {
			return err
		}
		vals, err = marketUnique(raw, "ts_code")
		if err != nil {
			return err
		}
		if row, ok := vals[c.code]; ok && !maps.Equal(row.Values, c.latestVal.Values) {
			return marketBad("daily_basic", "conflicting observations")
		}
		vals[c.code] = c.latestVal
	}
	c.Peers = marketPeers(c.code, current, vals)
	selected := append([]string{c.code}, c.Peers...)
	// The complete dated roster also supplies evidence of absence. Never infer
	// negative eligibility when the endpoint is empty or unentitled.
	margin := map[string]Row{}
	pagesByExchange := map[string]map[string]Row{}
	for _, code := range selected {
		_, suffix, _ := strings.Cut(code, ".")
		exchange := map[string]string{"SH": "SSE", "SZ": "SZSE", "BJ": "BSE"}[suffix]
		if exchange == "" {
			return marketBad("index_member_all", "A-share code")
		}
		if _, done := pagesByExchange[suffix]; done {
			continue
		}
		// Doc 326 accepts a single exchange and session. An otherwise valid
		// roster from Shanghai is not evidence of Shenzhen eligibility.
		rows, err := c.rows("margin_secs", map[string]string{"trade_date": day, "exchange": exchange}, "trade_date ts_code exchange", 6000, true)
		if err != nil {
			return err
		}
		pages := map[string]Row{}
		for _, r := range rows {
			security := r.Values["ts_code"]
			if !strings.HasSuffix(security, "."+suffix) {
				return marketBad("margin_secs", "security exchange")
			}
			margin[security] = r
			if _, ok := pages[r.RawRef]; !ok {
				page := r
				page.RawLocator = "response_ref#$.data.items"
				pages[r.RawRef] = page
			}
		}
		pagesByExchange[suffix] = pages
	}
	var refs []dataservice.Record
	var refKeys []string
	joins := marketJoins{}
	c.Entities = append(c.Entities, dataservice.Entity{Kind: "industry", Code: ind, Name: subject.Values["l2_name"], Status: "active", Classification: "SW2021 L2", Aliases: []string{subject.Values["l2_name"]}, Attrs: map[string]any{"index_code": ind, "level1_name": subject.Values["l1_name"]}})
	for _, code := range selected {
		m := current[code]
		_, suffix, _ := strings.Cut(code, ".")
		entity, rec, err := c.security(code, m, vals[code], margin[code], pagesByExchange[suffix], joins)
		if err != nil {
			return err
		}
		c.Entities = append(c.Entities, entity)
		if rec != nil {
			refs = append(refs, *rec)
			refKeys = append(refKeys, code)
		}
		if code != c.code {
			rec, ok, err := marketValuation(vals[code])
			if err != nil {
				return err
			}
			if ok {
				c.Records = append(c.Records, rec)
			}
		}
	}
	if err := c.archiveJoins(joins, refs, refKeys); err != nil {
		return err
	}
	var history []Row
	history = append(history, subjectHistory...)
	for _, r := range industryMembers {
		if slices.Contains(c.Peers, r.Values["ts_code"]) {
			history = append(history, r)
		}
	}
	seen := map[string]map[string]any{}
	for _, r := range history {
		rec, err := marketMembership(r)
		if err != nil {
			return err
		}
		if prev, ok := seen[rec.LogicalKey]; ok {
			if !maps.Equal(prev, rec.Payload) {
				return marketBad("index_member_all", "ambiguous interval")
			}
			continue
		}
		seen[rec.LogicalKey] = rec.Payload
		c.Records = append(c.Records, rec)
	}
	return c.index("sw_daily", ind, 4)
}

func marketPeers(subject string, members, vals map[string]Row) []string {
	own, err := decimal.NewFromString(vals[subject].Values["total_mv"])
	if err != nil || !own.IsPositive() {
		return nil
	}
	dist := map[string]decimal.Decimal{}
	for c := range members {
		row := vals[c]
		m, err := decimal.NewFromString(row.Values["total_mv"])
		if c != subject && err == nil && m.IsPositive() && row.Values["circ_mv"] != "" {
			dist[c] = m.Sub(own).Abs()
		}
	}
	codes := slices.Sorted(maps.Keys(dist))
	slices.SortFunc(codes, func(a, b string) int {
		if d := dist[a].Cmp(dist[b]); d != 0 {
			return d
		}
		return cmp.Compare(a, b)
	})
	return codes[:min(len(codes), 8)]
}

func (c *marketCollector) security(code string, member, value, eligible Row, pages map[string]Row, joins marketJoins) (dataservice.Entity, *dataservice.Record, error) {
	var r Row
	if code == c.code && c.requestedIdentity != nil {
		r = *c.requestedIdentity
	} else {
		rs, err := c.rows("stock_basic", map[string]string{"ts_code": code, "list_status": "L"}, securityFields, 1000, false)
		if err != nil {
			return dataservice.Entity{}, nil, err
		}
		if len(rs) != 1 {
			return dataservice.Entity{}, nil, fmt.Errorf("TuShare stock_basic: expected one listed stock for %s, got %d", code, len(rs))
		}
		r = rs[0]
	}
	v := r.Values
	exchanges := map[string]string{"SSE": "SH", "SZSE": "SZ", "BSE": "BJ"}
	ex := exchanges[v["exchange"]]
	board := map[string]string{"主板": "main", "创业板": "chinext", "科创板": "star", "北交所": "bse"}[v["market"]]
	if ex == "" || !strings.HasSuffix(code, "."+ex) || board == "" || v["name"] == "" {
		return dataservice.Entity{}, nil, marketBad("stock_basic", "A-share identity")
	}
	day := cmp.Or(value.Values["trade_date"], c.day)
	listDate, e := marketDate(v["list_date"])
	if e != nil || v["list_date"] > day {
		return dataservice.Entity{}, nil, marketBad("stock_basic", "list_date")
	}
	ind := member.Values["l2_code"]
	fin := member.Values["l1_code"] == "801780.SI" || member.Values["l1_code"] == "801790.SI"
	attrs := map[string]any{"industry_code": ind, "index_code": ind, "benchmark_code": marketBenchmark, "tushare_research_subject": code == c.code}
	if len(pages) > 0 {
		attrs["margin_eligible"] = eligible.Values != nil
	}
	aliases := []string{}
	for _, f := range []string{"symbol", "name", "fullname", "cnspell"} {
		if s := v[f]; s != "" && !slices.Contains(aliases, s) {
			aliases = append(aliases, s)
		}
	}
	status := "listed"
	if strings.HasPrefix(v["name"], "ST") || strings.HasPrefix(v["name"], "*ST") {
		status = "st"
	}
	entity := dataservice.Entity{Kind: "stock", Code: code, Name: v["name"], Exchange: ex, Status: status, IsFinancial: fin, Aliases: aliases, Classification: "SW2021 " + member.Values["l1_name"] + " > " + member.Values["l2_name"], Attrs: attrs}
	p := map[string]any{"security_code": code, "short_name": v["name"], "exchange": ex, "board": board, "list_date": listDate.Format(time.DateOnly), "status": status, "is_financial": fin}
	ok, err := marketNumbers(value, p, marketField{"total_share", "total_shares", 4, true}, marketField{"float_share", "float_shares", 4, true})
	if err != nil {
		return entity, nil, err
	}
	if !ok || len(pages) == 0 {
		c.Notes = append(c.Notes, "TuShare "+code+" 缺少股本或当日两融资格，未发布完整证券快照")
		return entity, nil, nil
	}
	p["margin_eligible"] = eligible.Values != nil
	joins.add(code, "stock_basic", r)
	joins.add(code, "index_member_all", member)
	joins.add(code, "daily_basic", value)
	if eligible.Values != nil {
		joins.add(code, "margin_secs", eligible)
	} else {
		for _, k := range slices.Sorted(maps.Keys(pages)) {
			joins.add(code, "margin_secs:"+k, pages[k])
		}
	}
	d, _ := marketDate(day)
	rec := marketRecord(r, "ref.security", code, code, "exchange", "L2", "TuShare 本次证券名录及同一交易日的股本、行业归属和两融标的名单", &d, p)
	rec.MappingVersion = 2
	return entity, &rec, nil
}
