package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tdewolff/canvas"
	canvaspdf "github.com/tdewolff/canvas/renderers/pdf"
)

// These generated upstream responses are deliberately fictitious. They exercise
// the documented TuShare wire format, including numeric cells, nulls, request
// filters, offset pagination and independently downloadable annual-report PDFs.
// They do not use Vera's fixture provider, canonical records or fact builders.
type tsTestRow map[string]any

type tsTestUpstream struct {
	rows map[string][]tsTestRow
	pdfs map[string][]byte
}

const tsTestSubject = "600519.SH"

func newTuShareUpstream(t *testing.T, end time.Time) http.RoundTripper {
	t.Helper()
	s := &tsTestUpstream{rows: map[string][]tsTestRow{}, pdfs: map[string][]byte{}}
	end = end.In(time.FixedZone("Asia/Shanghai", 8*3600))
	codes := []string{tsTestSubject, "600010.SH", "600011.SH", "600012.SH", "600013.SH", "600014.SH", "600015.SH"}
	s.marketRows(codes, end)
	s.filingRows(t, codes, end)
	return s
}

func (s *tsTestUpstream) add(api string, row tsTestRow) { s.rows[api] = append(s.rows[api], row) }

func tsTestName(i int) string {
	if i == 0 {
		return "合成样例股份"
	}
	return fmt.Sprintf("合成同行%s", string(rune('甲'+i)))
}

func (s *tsTestUpstream) marketRows(codes []string, end time.Time) {
	for i, code := range codes {
		s.add("stock_basic", tsTestRow{"ts_code": code, "symbol": code[:6], "name": tsTestName(i), "fullname": tsTestName(i) + "有限公司", "cnspell": "HCYL", "market": "主板", "exchange": "SSE", "list_status": "L", "list_date": "20090216"})
		s.add("index_member_all", tsTestRow{"ts_code": code, "name": tsTestName(i), "l1_code": "801120.SI", "l1_name": "合成食品饮料", "l2_code": "801125.SI", "l2_name": "合成饮品", "l3_code": "851251.SI", "l3_name": "合成产品", "in_date": "20130101", "is_new": "Y"})
	}
	start := end.AddDate(-5, -1, -10)
	n := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("20060102")
		open := 0
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			open = 1
		}
		s.add("trade_cal", tsTestRow{"cal_date": day, "is_open": open, "exchange": "SSE"})
		if open == 0 {
			continue
		}
		n++
		closePrice := 20 + float64(n%197)/100 + float64(n)/1000
		s.add("daily", tsTestRow{"ts_code": tsTestSubject, "trade_date": day, "open": closePrice - 0.1, "high": closePrice + 0.2, "low": closePrice - 0.25, "close": closePrice, "pre_close": closePrice - 0.04, "vol": 120.25 + float64(n%10), "amount": 2512.44 + float64(n%15)})
		s.add("adj_factor", tsTestRow{"ts_code": tsTestSubject, "trade_date": day, "adj_factor": 1.14})
		for i, code := range codes {
			s.add("daily_basic", tsTestRow{"ts_code": code, "trade_date": day, "total_mv": 75670 + n + i*302, "circ_mv": 58940 + n + i*202,
				"pe_ttm": 18.0 + float64(n%61)/10 + float64(i), "pb": 2.0 + float64(n%127)/100, "ps_ttm": 3.0 + float64(n%92)/100,
				"turnover_rate": 0.65 + float64(n%19)/100, "dv_ttm": 1.23, "total_share": 45000, "float_share": 43000})
		}
		s.add("index_daily", tsTestRow{"ts_code": "000300.SH", "trade_date": day, "close": 3600 + n, "amount": 2105123.88})
		s.add("sw_daily", tsTestRow{"ts_code": "801125.SI", "trade_date": day, "close": 1400 + n, "amount": 89321.25})
		if d.Before(end.AddDate(-1, -1, 0)) {
			continue
		}
		s.add("margin_secs", tsTestRow{"ts_code": tsTestSubject, "trade_date": day, "exchange": "SSE"})
		s.add("margin_detail", tsTestRow{"ts_code": tsTestSubject, "trade_date": day, "rzye": 5000000 + n*53, "rzmre": 20000 + n, "rzche": 19500 + n%25, "rqyl": 4500, "rqye": 60450})
		s.add("moneyflow", tsTestRow{"ts_code": tsTestSubject, "trade_date": day, "buy_sm_amount": 12.1, "sell_sm_amount": 15.22, "buy_md_amount": 55.6, "sell_md_amount": 40.29, "buy_lg_amount": 125.5, "sell_lg_amount": 122.7, "buy_elg_amount": 278, "sell_elg_amount": 260.9})
		if n%12 == 0 {
			s.add("block_trade", tsTestRow{"ts_code": tsTestSubject, "trade_date": day, "price": 20.5, "vol": 5, "amount": 102.5, "buyer": "合成买方营业部", "seller": "合成卖方营业部"})
		}
	}
	s.rows["suspend_d"] = []tsTestRow{}
}

func (s *tsTestUpstream) filingRows(t *testing.T, codes []string, end time.Time) {
	t.Helper()
	family := canvas.NewFontFamily("Test filing font")
	if err := family.LoadFontFile("../reportpdf/fonts/SourceHanSansCN-Regular.otf", canvas.FontRegular); err != nil {
		t.Fatal(err)
	}
	face := family.Face(12, canvas.Black, canvas.FontRegular)
	for i, code := range codes {
		for year := end.Year() - 6; year <= end.Year(); year++ {
			for _, spec := range []struct {
				period, title string
				month         time.Month
				yearOffset    int
				denominator   int
			}{
				{"1231", "年度", 3, 1, 1}, {"0630", "半年度", 8, 0, 2}, {"0930", "第三季度", 10, 0, 1},
			} {
				pub := time.Date(year+spec.yearOffset, spec.month, 25, 18, 0, 0, 0, end.Location())
				if pub.After(end) {
					continue
				}
				period := fmt.Sprintf("%d%s", year, spec.period)
				title := fmt.Sprintf("%s：%d年%s报告", tsTestName(i), year, spec.title)
				id := fmt.Sprintf("%02d%s", i+10, period)
				url := "https://static.cninfo.com.cn/finalpage/" + pub.Format(time.DateOnly) + "/" + id + ".PDF"
				row := tsTestRow{"ts_code": code, "ann_date": pub.Format("20060102"), "name": tsTestName(i), "rec_time": pub.Format("2006-01-02 15:04:05"), "title": title, "url": url}
				s.add("anns_d", row)
				rev := (3_000_000_000 + int64(year-end.Year()+6)*200_000_000 + int64(i)*30_000_000) / int64(spec.denominator)
				s.statements(code, period, pub, rev)
				if i != 0 {
					continue
				}
				s.add("stk_holdernumber", tsTestRow{"ts_code": code, "ann_date": pub.Format("20060102"), "end_date": period, "holder_num": 28000 + year + int(spec.month)*22})
				if spec.title != "年度" {
					continue
				}
				s.pdfs[url] = annualTestPDF(t, face, year, rev)
				for _, typ := range []string{"P", "D", "I"} {
					for j, name := range map[string][]string{"P": {"合成产品甲", "合成产品乙"}, "D": {"合成境内", "合成境外"}, "I": {"合成产业甲", "合成产业乙"}}[typ] {
						sales := rev * int64(3+j) / 7
						s.add("fina_mainbz", tsTestRow{"ts_code": code, "end_date": period, "bz_item": name, "bz_code": typ, "bz_sales": sales, "bz_cost": sales * 3 / 5, "curr_type": "CNY"})
					}
				}
			}
		}
	}
	pub := end.AddDate(0, 0, -5)
	s.add("anns_d", tsTestRow{"ts_code": codes[0], "name": tsTestName(0), "title": "合成样例股份关于更新业务披露的公告", "ann_date": pub.Format("20060102"), "rec_time": pub.Format("2006-01-02 15:04:05"), "url": "https://static.cninfo.com.cn/finalpage/" + pub.Format(time.DateOnly) + "/123456789001.PDF"})
}

func (s *tsTestUpstream) statements(code, period string, pub time.Time, revenue int64) {
	profit := revenue / 5
	for api, values := range map[string]tsTestRow{
		"income":       {"total_revenue": revenue, "revenue": revenue, "oper_cost": revenue * 3 / 5, "sell_exp": revenue / 22, "admin_exp": revenue / 24, "rd_exp": revenue / 27, "fin_exp": revenue / 140, "operate_profit": profit * 6 / 5, "total_profit": profit * 7 / 5, "n_income": profit * 11 / 10, "n_income_attr_p": profit},
		"balancesheet": {"total_assets": revenue * 2, "total_liab": revenue, "total_hldr_eqy_exc_min_int": revenue * 9 / 10, "money_cap": revenue / 8, "accounts_receiv": revenue / 20, "notes_receiv": revenue / 50, "inventories": revenue / 7, "goodwill": revenue / 300, "st_borr": revenue / 15, "lt_borr": revenue / 22, "bond_payable": revenue / 65, "non_cur_liab_due_1y": revenue / 90},
		"cashflow":     {"n_cashflow_act": profit * 11 / 10, "n_cashflow_inv_act": -revenue / 10, "n_cash_flows_fnc_act": -revenue / 12, "c_pay_acq_const_fiolta": revenue / 8, "c_pay_dist_dpcp_int_exp": revenue / 9},
	} {
		values["ts_code"], values["end_date"], values["ann_date"], values["f_ann_date"], values["comp_type"], values["report_type"] = code, period, pub.Format("20060102"), pub.Format("20060102"), "1", "1"
		s.add(api, values)
	}
}

func annualTestPDF(t *testing.T, face *canvas.FontFace, year int, revenue int64) []byte {
	t.Helper()
	lines := []string{
		fmt.Sprintf("合成样例股份 600519 %d年年度报告", year), "二、报告期内公司从事的主要业务",
		"公司生产合成饮品并向国内客户提供配套服务。本页为接口测试构造的披露文本。",
		"三、核心竞争力分析", "公司通过自有生产线管理原料采购、品质检验及产品配送。",
		"四、经营情况讨论与分析", "公司在报告期内持续维护经销渠道，主要产品通过区域经销网络销售。",
		"五、公司面临的风险", "公司披露的经营风险包括原材料成本波动以及经销渠道回款变化。",
		"六、公司未来发展的展望", "公司披露的经营计划包括持续改善质量管理并跟踪核心原材料价格。",
		"第七节 主要会计数据", fmt.Sprintf("%d年                 %d年", year, year-1),
		fmt.Sprintf("归属于上市公司股东的扣除非经常性损益后的净利润（元）   %d", revenue*18/100),
	}
	var out bytes.Buffer
	writer := canvaspdf.New(&out, 210, 297, nil)
	page := canvas.New(210, 297)
	cc := canvas.NewContext(page)
	for i, line := range lines {
		cc.DrawText(10, 281-float64(i)*15, canvas.NewTextLine(face, line, canvas.Left))
	}
	page.RenderTo(writer)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (s *tsTestUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	response := func(status int, typ string, body []byte) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{typ}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}
	}
	if req.Method == http.MethodGet && req.URL.Host == "static.cninfo.com.cn" && req.URL.Scheme == "https" {
		if b, ok := s.pdfs[req.URL.String()]; ok {
			return response(200, "application/pdf", b), nil
		}
		return response(404, "text/plain", nil), nil
	}
	if req.URL.Scheme != "https" || req.URL.Host != "api.tushare.pro" || req.Method != http.MethodPost {
		return nil, fmt.Errorf("synthetic upstream: unregistered destination or method")
	}
	var query struct {
		API    string         `json:"api_name"`
		Fields string         `json:"fields"`
		Token  string         `json:"token"`
		Params map[string]any `json:"params"`
	}
	defer req.Body.Close()
	if err := json.NewDecoder(req.Body).Decode(&query); err != nil {
		return nil, err
	}
	if query.Token == "" {
		return nil, fmt.Errorf("synthetic TuShare POST has no authentication")
	}
	set, known := s.rows[query.API]
	if !known {
		return nil, fmt.Errorf("unexpected TuShare test interface %q", query.API)
	}
	fields := strings.Split(query.Fields, ",")
	if len(fields) == 0 {
		return nil, fmt.Errorf("TuShare test request has no fields")
	}
	var matches []tsTestRow
	for _, row := range set {
		matchesQuery := true
		for k, v := range query.Params {
			value := fmt.Sprint(v)
			switch k {
			case "limit", "offset":
				continue
			case "start_date", "end_date":
				dateField := "trade_date"
				switch query.API {
				case "trade_cal":
					dateField = "cal_date"
				case "anns_d", "income", "balancesheet", "cashflow", "stk_holdernumber":
					dateField = "ann_date"
				}
				date := fmt.Sprint(row[dateField])
				if (k == "start_date" && date < value) || (k == "end_date" && date > value) {
					matchesQuery = false
				}
			case "period", "type":
				outField := map[string]string{"period": "end_date", "type": "bz_code"}[k]
				if fmt.Sprint(row[outField]) != value {
					matchesQuery = false
				}
			default:
				if fmt.Sprint(row[k]) != value {
					matchesQuery = false
				}
			}
			if !matchesQuery {
				break
			}
		}
		if matchesQuery {
			matches = append(matches, row)
		}
	}
	// Return the most recent observations first, as historical Pro endpoints do.
	slices.Reverse(matches)
	limit, err := strconv.Atoi(fmt.Sprint(query.Params["limit"]))
	if err != nil || limit < 1 {
		return nil, fmt.Errorf("missing TuShare test page limit")
	}
	offset, err := strconv.Atoi(fmt.Sprint(query.Params["offset"]))
	if err != nil || offset < 0 {
		return nil, fmt.Errorf("bad TuShare test page offset")
	}
	hi := min(len(matches), offset+min(500, limit))
	offset = min(offset, len(matches))
	items := make([][]any, 0, hi-offset)
	for _, row := range matches[offset:hi] {
		cells := make([]any, len(fields))
		for j, f := range fields {
			cells[j] = row[f]
		}
		items = append(items, cells)
	}
	payload := map[string]any{"code": 0, "msg": nil, "data": map[string]any{"fields": fields, "items": items, "has_more": hi < len(matches)}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return response(200, "application/json", raw), nil
}

func TestTuShareUpstreamWireFixture(t *testing.T) {
	end := time.Date(2026, 11, 20, 19, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	upstream := newTuShareUpstream(t, end)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.tushare.pro", strings.NewReader(`{"api_name":"daily","token":"SYNTHETIC","fields":"close,ts_code,trade_date","params":{"ts_code":"600519.SH","limit":3000,"offset":500}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := upstream.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Data struct {
			Items   [][]any `json:"items"`
			HasMore bool    `json:"has_more"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data.Items) != 500 || !got.Data.HasMore || got.Data.Items[0][1] != tsTestSubject {
		t.Fatal("expected paginated historical stock data")
	}
}
