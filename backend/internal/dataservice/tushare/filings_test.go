package tushare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const filingTestCode = "600001.SH"
const filingTestURL = "http://static.cninfo.com.cn/finalpage/2026-03-31/1234567890.PDF"

var filingTestEnd = time.Date(2026, 5, 1, 12, 0, 0, 0, filingZone)

func filingTestRow(v map[string]string) Row {
	v["ts_code"] = filingTestCode
	return Row{Values: v, ReceivedAt: filingTestEnd, RawRef: "raw/official-shape.json", RawSHA256: strings.Repeat("a", 64), RawLocator: "$.data.items[0]"}
}

func filingTestAnnouncement() Row {
	return filingTestRow(map[string]string{"name": "合成甲公司", "title": "合成甲公司：2025年年度报告", "ann_date": "20260331", "url": filingTestURL,
		"rec_time": "2026-03-30 18:30:00"})
}

func TestFilingDisclosureTimeScopeAndRevisions(t *testing.T) {
	original := filingTestAnnouncement()
	corr := filingTestAnnouncement()
	corr.Values["url"] = strings.ReplaceAll(filingTestURL, "1234567890", "2234567890")
	corr.Values["title"] += "（修订版）"
	corr.Values["ann_date"] = "20260410"
	delete(corr.Values, "rec_time")
	originalRow := filingTestRow(map[string]string{"ann_date": "20260331", "f_ann_date": "20260331", "end_date": "20251231", "comp_type": "1", "report_type": "1", "revenue": "340000001.42"})
	revised := filingTestRow(map[string]string{"ann_date": "20260410", "f_ann_date": "20260410", "end_date": "20251231", "comp_type": "1", "report_type": "1", "revenue": "330000000"})
	for _, tc := range []struct {
		end  time.Time
		want int
	}{
		{time.Date(2026, 3, 30, 18, 29, 59, 0, filingZone), 0},
		{time.Date(2026, 4, 10, 12, 0, 0, 0, filingZone), 1},
		{filingTestEnd, 2},
	} {
		i := indexFilings([]Row{corr, original}, filingTestCode, tc.end)
		rs, err := mapStatements(filingTestCode, "income", []Row{originalRow, revised}, i)
		if err != nil || len(rs) != tc.want {
			t.Fatalf("cutoff=%v rows=%v err=%v", tc.end, rs, err)
		}
		if tc.want == 2 && (rs[0].LogicalKey != rs[1].LogicalKey || rs[0].Version != 1 || rs[1].Version != 2 || rs[1].Payload["revision"] != 1) {
			t.Fatalf("revisions %+v", rs)
		}
	}
	idx := indexFilings([]Row{original, corr}, filingTestCode, filingTestEnd)
	for _, change := range []map[string]string{{"comp_type": "2"}, {"report_type": "2"}, {"report_type": "4"}, {"report_type": "6"},
		{"end_date": "20241231"}, {"ts_code": "600002.SH"}, {"f_ann_date": "20260420"}} {
		r := filingTestRow(map[string]string{"ann_date": "20260331", "f_ann_date": "20260331", "end_date": "20251231", "comp_type": "1", "report_type": "1", "revenue": "10"})
		for k, v := range change {
			r.Values[k] = v
		}
		rs, err := mapStatements(filingTestCode, "income", []Row{r}, idx)
		if err != nil || len(rs) != 0 {
			t.Fatalf("unexpected values for %v", change)
		}
	}
	duplicate := filingTestAnnouncement()
	duplicate.Values["url"] = strings.ReplaceAll(filingTestURL, "1234567890", "3333333333")
	if _, ok := indexFilings([]Row{original, duplicate}, filingTestCode, filingTestEnd).match("20251231", "20260331"); ok {
		t.Fatal("ambiguous same-day filing matched")
	}
}

func TestFilingRestrictedDocumentLinks(t *testing.T) {
	link, id := filingURL(filingTestURL)
	if !strings.HasPrefix(link, "https://") || id != "static.cninfo.com.cn/1234567890" {
		t.Fatal(link, id)
	}
	for _, bad := range []string{"file:///etc/passwd", "https://127.0.0.1/a.pdf", filingTestURL + "?path=a",
		strings.Replace(filingTestURL, "static.cninfo.com.cn", "static.cninfo.com.cn@somewhere.test", 1), strings.Replace(filingTestURL, "1234567890", "../../1234567890", 1),
		strings.Replace(filingTestURL, "1234567890", "%311234567890", 1), strings.Replace(filingTestURL, ".cn/", ".cn:443/", 1)} {
		if l, _ := filingURL(bad); l != "" {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestFilingSegmentsAndMissingNumbers(t *testing.T) {
	a, _ := disclosureFrom(filingTestAnnouncement(), filingTestCode, filingTestEnd)
	rows := []Row{filingTestRow(map[string]string{"end_date": a.period, "bz_code": "P", "bz_item": "部件", "bz_sales": "45000.4", "curr_type": "CNY"}),
		filingTestRow(map[string]string{"end_date": a.period, "bz_code": "P", "bz_item": "美元业务", "bz_sales": "18", "curr_type": "USD"}),
		filingTestRow(map[string]string{"end_date": a.period, "bz_code": "D", "bz_item": "境内", "bz_sales": "90", "curr_type": "CNY"})}
	rs, err := mapSegments(rows, filingTestCode, a, "P")
	if err != nil || len(rs) != 1 || rs[0].Payload["cost"] != nil || rs[0].Payload["revenue"] != "45000.4" {
		t.Fatal(rs, err)
	}
	add := filingTestRow(map[string]string{"end_date": a.period, "bz_code": "P", "bz_item": "部件", "bz_sales": "45001", "curr_type": "CNY"})
	rs, err = mapSegments(append(rows, add), filingTestCode, a, "P")
	if err != nil || len(rs) != 0 {
		t.Fatal("conflicting segment accepted")
	}
}

func TestFilingTextEvidenceAndOwnership(t *testing.T) {
	a, _ := disclosureFrom(filingTestAnnouncement(), filingTestCode, filingTestEnd)
	pages := []string{"目录\n三、营业收入构成 …… 16\n", "2025 年度报告\n二、报告期内公司从事的主要业务\n公司生产测试设备并提供售后服务，覆盖多个地区的客户。\n第三节 公司治理\n我们的治理段落不属于主营业务章节。\n",
		"四、可能面对的风险\n原材料价格波动会增加采购成本，进而影响生产经营。\n指标     2025年      2024年\n归属于上市公司股东的扣除非经常性损益的净利润（元）  -3,600.05    4,600.12"}
	rs := filingNarrative(a, pages, "raw/pdf", "digest", filingTestEnd)
	if len(rs) != 2 || rs[0].Payload["page"] != 2 || rs[0].Payload["section"] != "business_overview" || rs[1].Payload["section"] != "risk_factors" ||
		strings.Contains(rs[0].Payload["text"].(string), "治理") || rs[0].RawRef != "raw/pdf" {
		t.Fatal(rs)
	}
	r, ok := filingDeducted(a, pages, "raw/pdf", "digest", filingTestEnd)
	if !ok || r.Payload["value"] != "-3600.05" || r.SourceTier != "L1" {
		t.Fatal(r)
	}
	for _, replacement := range [][2]string{{"归属于上市公司股东的", ""}, {"（元）", "（万元）"}, {"2025年      2024年", "2024年      2023年"}} {
		if _, ok := filingDeducted(a, []string{strings.ReplaceAll(pages[2], replacement[0], replacement[1])}, "raw/pdf", "digest", filingTestEnd); ok {
			t.Fatalf("ambiguous row accepted %+v", replacement)
		}
	}
}

type filingRoundTrip func(*http.Request) (*http.Response, error)

func (f filingRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type filingSample struct {
	doc      []byte
	archived bool
	requests []string
}

func (f *filingSample) FilingTransport() http.RoundTripper {
	return filingRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "static.cninfo.com.cn" || r.URL.Scheme != "https" || r.Header.Get("Authorization") != "" {
			return nil, fmt.Errorf("unexpected PDF request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(f.doc))}, nil
	})
}
func (f *filingSample) Archive(_ context.Context, kind string, b []byte) (string, string, error) {
	if kind != "pdf" || !bytes.Equal(b, f.doc) {
		return "", "", fmt.Errorf("archive mismatch")
	}
	f.archived = true
	return "synthetic-archive/pdf", fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func (f *filingSample) Rows(_ context.Context, api string, p map[string]string, fields []string, size int) ([]Row, error) {
	f.requests = append(f.requests, api)
	switch api {
	case "anns_d":
		if p["start_date"] <= "20260331" && p["end_date"] >= "20260331" {
			return []Row{filingTestAnnouncement()}, nil
		}
	case "income", "cashflow", "balancesheet":
		if p["report_type"] != "1" || !slices.Contains(fields, "f_ann_date") {
			return nil, fmt.Errorf("wrong query")
		}
		return []Row{filingTestRow(map[string]string{"ann_date": "20260331", "f_ann_date": "20260331", "end_date": "20251231", "comp_type": "1", "report_type": "1", "revenue": "500.12", "total_assets": "890.10", "n_cashflow_act": "32.1"})}, nil
	case "fina_mainbz":
		if size != 100 || p["period"] != "20251231" {
			return nil, fmt.Errorf("wrong mainbz query")
		}
		if p["type"] == "P" {
			return []Row{filingTestRow(map[string]string{"end_date": "20251231", "bz_item": "甲", "bz_sales": "500.12", "bz_code": "P", "curr_type": "CNY"})}, nil
		}
	default:
		return nil, fmt.Errorf("unexpected endpoint %s", api)
	}
	return nil, nil
}

// Exercise a generated Chinese PDF through the same bounded subprocess used by
// the collector. It contains no real vendor data and needs no account.
func TestCollectFilingsOfficialShapeAndPDF(t *testing.T) {
	family := canvas.NewFontFamily("synthetic filing")
	if err := family.LoadFontFile("../../reportpdf/fonts/SourceHanSansCN-Regular.otf", canvas.FontRegular); err != nil {
		t.Fatal(err)
	}
	face := family.Face(12, canvas.Black, canvas.FontRegular)
	var doc bytes.Buffer
	p := pdf.New(&doc, 210, 297, nil)
	c := canvas.New(210, 297)
	cc := canvas.NewContext(c)
	lines := []string{"合成甲公司 600001 2025年年度报告", "二、报告期内公司从事的主要业务", "公司生产合成设备并提供技术服务，本段仅用于接口测试。",
		"四、公司面临的风险", "公司披露的风险包括下游需求变化以及合成设备原材料价格变动。"}
	for i, line := range lines {
		cc.DrawText(15, 280-float64(i)*12, canvas.NewTextLine(face, line, canvas.Left))
	}
	c.RenderTo(p)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	f := filingSample{doc: doc.Bytes()}
	got, err := collectFilings(t.Context(), &f, filingTestCode, filingTestEnd, false)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, r := range got.Records {
		if err := catalog.Validate(r); err != nil {
			t.Fatal(err)
		}
		count[r.DatasetID]++
		if r.DatasetID == "doc.announcement" && r.Payload["body_ref"] != "synthetic-archive/pdf" {
			t.Fatal(r)
		}
	}
	if len(got.Notes) != 0 || !f.archived || count["fin.statement"] != 3 || count["doc.periodic_text"] != 2 || count["doc.announcement"] != 1 || count["fin.segment"] != 1 {
		pages, _ := filingText(t.Context(), doc.Bytes())
		t.Fatalf("reports %+v; limitations %v; PDF text %q", count, got.Notes, pages)
	}
}
