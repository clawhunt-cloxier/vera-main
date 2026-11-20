package tushare

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// TuShare Pro documents 33, 36 and 44. These are consolidated CNY amounts.
// profit_dedt (doc 79) does not identify the ownership scope; the deducted
// parent net profit is read only from an explicitly labelled filing instead.
var filingAccounts = map[string]map[string]string{
	"income": {"total_revenue": "IS.total_revenue", "revenue": "IS.revenue", "oper_cost": "IS.operating_cost",
		"sell_exp": "IS.selling_expense", "admin_exp": "IS.admin_expense", "rd_exp": "IS.rd_expense", "fin_exp": "IS.finance_expense",
		"operate_profit": "IS.operating_profit", "total_profit": "IS.total_profit", "n_income": "IS.net_profit", "n_income_attr_p": "IS.net_profit_parent"},
	"balancesheet": {"total_assets": "BS.total_assets", "total_liab": "BS.total_liabilities", "total_hldr_eqy_exc_min_int": "BS.equity_parent",
		"money_cap": "BS.cash", "accounts_receiv": "BS.accounts_receivable", "notes_receiv": "BS.notes_receivable",
		"inventories": "BS.inventory", "goodwill": "BS.goodwill", "st_borr": "BS.short_term_borrowing", "lt_borr": "BS.long_term_borrowing",
		"bond_payable": "BS.bonds_payable", "non_cur_liab_due_1y": "BS.current_portion_noncurrent_liab"},
	"cashflow": {"n_cashflow_act": "CF.operating_net", "n_cashflow_inv_act": "CF.investing_net", "n_cash_flows_fnc_act": "CF.financing_net",
		"c_pay_acq_const_fiolta": "CF.capex", "c_pay_dist_dpcp_int_exp": "CF.dividends_paid"},
}

var filingZone = time.FixedZone("Asia/Shanghai", 8*3600)
var annualTitle = regexp.MustCompile(`((?:19|20)[0-9]{2})年?(年度|半年度|第一季度|第三季度|一季度|三季度)报告(?:全文)?(?:[（(](修订(?:版|本|稿|后)?|更正(?:版|本|后)?|更新(?:版|后)?)[）)])?$`)
var filingEnd = map[string]string{"年度": "1231", "半年度": "0630", "第一季度": "0331", "第三季度": "0930", "一季度": "0331", "三季度": "0930"}
var filingPeriods = map[string]string{"1231": "FY", "0630": "H1", "0331": "Q1", "0930": "Q3"}

type disclosure struct {
	row                               Row
	code, id, title, url, day, period string
	published                         time.Time
	exact, corrected                  bool
	revision                          int
}

func (a disclosure) periodDate() time.Time { t, _ := time.Parse("20060102", a.period); return t }
func (a disclosure) basis() string {
	if a.exact {
		return "TuShare 公告发布时间（北京时间）"
	}
	return "TuShare 公告日期（精确到日，按北京时间当日结束计入）"
}

func disclosureFrom(row Row, code string, end time.Time) (disclosure, bool) {
	v := row.Values
	a := disclosure{row: row, code: code, day: v["ann_date"], title: strings.TrimSpace(v["title"])}
	d, err := time.ParseInLocation("20060102", a.day, filingZone)
	if err != nil || v["ts_code"] != code || a.title == "" {
		return disclosure{}, false
	}
	a.published = d.AddDate(0, 0, 1).Add(-time.Nanosecond)
	if raw := v["rec_time"]; raw != "" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
			if t, e := time.ParseInLocation(layout, raw, filingZone); e == nil {
				a.published = t
				a.exact = true
				break
			}
		}
	}
	if a.published.After(end) {
		return disclosure{}, false
	}
	a.url, a.id = filingURL(v["url"])
	if a.id == "" {
		return disclosure{}, false
	}
	title := strings.Join(strings.Fields(a.title), "")
	if m := annualTitle.FindStringSubmatch(title); m != nil && !strings.Contains(title, "关于") && !strings.Contains(title, "审议") {
		a.period = m[1] + filingEnd[m[2]]
		a.corrected = m[3] != ""
	}
	return a, true
}

func (a disclosure) record(ds, key, basis, tier string, payload map[string]any, row Row) dataservice.Record {
	return dataservice.Record{DatasetID: ds, EntityCode: a.code, RecordKey: "tushare|" + a.code + "|" + key, PublishedAt: &a.published,
		AvailableAt: row.ReceivedAt, Origin: "company_filing", SourceTier: tier, Basis: basis, Payload: payload,
		RawRef: row.RawRef, RawSHA256: row.RawSHA256, RawLocator: row.RawLocator, MappingVersion: 1}
}

func (a disclosure) announcement() dataservice.Record {
	category := "other"
	if a.period != "" {
		category = "periodic_report"
	}
	r := a.record("doc.announcement", a.id, a.basis(), "L2", map[string]any{"announcement_id": a.id, "title": a.title,
		"category": category, "announced_at": a.published.Format(time.RFC3339Nano)}, a.row)
	if a.period != "" {
		d := a.periodDate()
		r.Period = &d
	}
	return r
}

func (a disclosure) statement(row Row, item, value, basis, field string) dataservice.Record {
	d := a.periodDate()
	logical := "tushare|" + a.code + "|" + a.period + "|consolidated|" + item
	r := a.record("fin.statement", a.period+"|"+item+"|"+a.id, basis, "L2", map[string]any{
		"report_period": d.Format(time.DateOnly), "report_type": filingPeriods[a.period[4:]], "scope": "consolidated", "item_code": item,
		"value": value, "revision": a.revision, "filing_id": a.id}, row)
	r.Period = &d
	r.Version = a.revision + 1
	r.LogicalKey = logical
	r.RawLocator += field
	return r
}

type filingIndex struct {
	all      []disclosure
	byPeriod map[string][]disclosure
}

// Same-period/day editions with different IDs cannot identify one financial row.
// Keep their metadata but omit ambiguous rows and corresponding text.
func indexFilings(rows []Row, code string, end time.Time) filingIndex {
	i := filingIndex{byPeriod: map[string][]disclosure{}}
	byID := map[string]disclosure{}
	bad := map[string]bool{}
	for _, row := range rows {
		a, ok := disclosureFrom(row, code, end)
		if !ok {
			continue
		}
		if prev, ok := byID[a.id]; ok && (prev.day != a.day || prev.title != a.title || !prev.published.Equal(a.published)) {
			bad[a.id] = true
		}
		byID[a.id] = a
	}
	for _, a := range byID {
		if !bad[a.id] {
			i.all = append(i.all, a)
		}
	}
	slices.SortFunc(i.all, func(a, b disclosure) int {
		if c := a.published.Compare(b.published); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	for _, a := range i.all {
		if a.period != "" {
			a.revision = len(i.byPeriod[a.period])
			i.byPeriod[a.period] = append(i.byPeriod[a.period], a)
		}
	}
	return i
}

func (i filingIndex) match(period, day string) (disclosure, bool) {
	var found []disclosure
	for _, a := range i.byPeriod[period] {
		if a.day == day || (a.exact && a.published.In(filingZone).Format("20060102") == day) {
			found = append(found, a)
		}
	}
	if len(found) != 1 || found[0].corrected && found[0].revision == 0 {
		return disclosure{}, false
	}
	return found[0], true
}

func filingDecimal(s string) (string, error) {
	v, err := decimal.NewFromString(s)
	if err != nil || v.Exponent() < -40 || v.Exponent() > 30 {
		return "", fmt.Errorf("TuShare financial field has invalid decimal")
	}
	return v.String(), nil
}

func mapStatements(code, api string, rows []Row, index filingIndex) ([]dataservice.Record, error) {
	byKey := map[string]dataservice.Record{}
	bad := map[string]bool{}
	for _, row := range rows {
		v := row.Values
		if v["ts_code"] != code || v["report_type"] != "1" || v["comp_type"] != "1" {
			continue
		}
		// f_ann_date is the actual disclosure date. Do not use end_date or an
		// update flag to infer a version. Type 4 is a later comparative column.
		day := v["f_ann_date"]
		if day == "" {
			day = v["ann_date"]
		}
		a, ok := index.match(v["end_date"], day)
		if !ok {
			continue
		}
		for field, item := range filingAccounts[api] {
			if v[field] == "" {
				continue
			}
			value, err := filingDecimal(v[field])
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", api, field, err)
			}
			r := a.statement(row, item, value, "TuShare "+api+" 合并报表（一般工商业，人民币元）；"+a.basis(), " field="+field)
			if prev, exists := byKey[r.RecordKey]; exists && prev.Payload["value"] != value {
				bad[r.RecordKey] = true
			}
			byKey[r.RecordKey] = r
		}
	}
	var out []dataservice.Record
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		if !bad[k] {
			out = append(out, byKey[k])
		}
	}
	return out, nil
}

func mapSegments(rows []Row, code string, a disclosure, typ string) ([]dataservice.Record, error) {
	var out []dataservice.Record
	seen := map[string]dataservice.Record{}
	conflict := map[string]bool{}
	for _, row := range rows {
		v := row.Values
		if v["ts_code"] != code || v["end_date"] != a.period || v["curr_type"] != "CNY" ||
			(v["bz_code"] != "" && v["bz_code"] != typ) || v["bz_item"] == "" || v["bz_sales"] == "" {
			continue
		}
		dim := map[string]string{"P": "product", "D": "region", "I": "industry"}[typ]
		sales, err := filingDecimal(v["bz_sales"])
		if err != nil {
			return nil, err
		}
		d := a.periodDate()
		p := map[string]any{"report_period": d.Format(time.DateOnly), "dimension": dim, "segment_name": v["bz_item"], "revenue": sales}
		if v["bz_cost"] != "" {
			p["cost"], err = filingDecimal(v["bz_cost"])
			if err != nil {
				return nil, err
			}
		}
		r := a.record("fin.segment", a.period+"|"+dim+"|"+v["bz_item"], "TuShare fina_mainbz（人民币元）；按报告期关联唯一年度公告 "+a.id+"，以本次采集时间可见", "L2", p, row)
		r.Period = &d
		if prev, ok := seen[r.RecordKey]; ok && r.Hash() != prev.Hash() {
			conflict[r.RecordKey] = true
		}
		seen[r.RecordKey] = r
	}
	for _, k := range slices.Sorted(maps.Keys(seen)) {
		if !conflict[k] {
			out = append(out, seen[k])
		}
	}
	return out, nil
}

func collectFilings(ctx context.Context, src Source, code string, end time.Time, peer bool) (Batch, error) {
	var result Batch
	day := end.In(filingZone)
	start := time.Date(day.Year()-5, 1, 1, 0, 0, 0, 0, filingZone)
	var rows []Row
	// anns_d documents date-window collection. Keep every year's denominator
	// bounded before following the provider's pagination (doc 176).
	for lo := start; !lo.After(day); lo = lo.AddDate(1, 0, 0) {
		hi := lo.AddDate(1, 0, -1)
		if hi.After(day) {
			hi = day
		}
		r, err := src.Rows(ctx, "anns_d", map[string]string{"ts_code": code, "start_date": lo.Format("20060102"), "end_date": hi.Format("20060102")},
			[]string{"ts_code", "ann_date", "name", "title", "url", "rec_time"}, 2000)
		if err != nil {
			return result, err
		}
		rows = append(rows, r...)
	}
	idx := indexFilings(rows, code, end)
	if len(idx.byPeriod) == 0 {
		result.Notes = append(result.Notes, code+" 缺少可关联的定期报告公告")
	}
	for _, a := range idx.all {
		if a.period != "" || (!peer && !a.published.Before(day.AddDate(0, 0, -30))) {
			result.Records = append(result.Records, a.announcement())
		}
	}
	params := map[string]string{"ts_code": code, "start_date": start.Format("20060102"), "end_date": day.Format("20060102"), "report_type": "1"}
	for _, api := range []string{"income", "balancesheet", "cashflow"} {
		if peer && api == "cashflow" {
			continue
		}
		fields := append([]string{"ts_code", "ann_date", "f_ann_date", "end_date", "report_type", "comp_type"}, slices.Sorted(maps.Keys(filingAccounts[api]))...)
		rows, err := src.Rows(ctx, api, params, fields, 100)
		if err != nil {
			return result, err
		}
		rs, err := mapStatements(code, api, rows, idx)
		if err != nil {
			return result, err
		}
		result.Records = append(result.Records, rs...)
	}
	if peer {
		return result, nil
	}
	periods := slices.Sorted(maps.Keys(idx.byPeriod))
	slices.Reverse(periods)
	annual := 0
	latestText := false
	for _, p := range periods {
		if !strings.HasSuffix(p, "1231") {
			continue
		}
		annual++
		editions := idx.byPeriod[p]
		last := editions[len(editions)-1]
		a, unique := idx.match(p, last.day)
		if !unique {
			result.Notes = append(result.Notes, p+" 的年报版本无法唯一定位")
			continue
		}
		if annual == 1 && len(editions) == 1 {
			for _, typ := range []string{"P", "D", "I"} {
				r, err := src.Rows(ctx, "fina_mainbz", map[string]string{"ts_code": code, "period": p, "type": typ},
					[]string{"ts_code", "end_date", "bz_item", "bz_code", "bz_sales", "bz_cost", "curr_type"}, 100)
				if err != nil {
					if ctx.Err() != nil {
						return result, ctx.Err()
					}
					result.Notes = append(result.Notes, "TuShare fina_mainbz "+typ+" 不可用")
					continue
				}
				segs, err := mapSegments(r, code, a, typ)
				if err != nil {
					return result, err
				}
				result.Records = append(result.Records, segs...)
			}
		}
		if annual <= 5 {
			rs, ref, sha, err := filingDocument(ctx, src, a, annual == 1 || annual == 2 && latestText)
			if err != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				result.Notes = append(result.Notes, fmt.Sprintf("%s 年报正文不可用：%v", p, err))
				continue
			}
			if annual == 1 {
				latestText = true
			}
			result.Records = append(result.Records, rs...)
			for j, r := range result.Records {
				if r.DatasetID == "doc.announcement" && r.Payload["announcement_id"] == a.id {
					result.Records[j].Payload["body_ref"] = ref
					result.Records[j].Payload["body_sha256"] = sha
					break
				}
			}
		}
	}
	return result, nil
}
