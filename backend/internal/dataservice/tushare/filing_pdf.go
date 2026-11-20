package tushare

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// Only original documents returned by anns_d on these registered paths are
// eligible. Upgrade historical CNINFO http links before making any request.
var filingPaths = map[string]*regexp.Regexp{
	"static.cninfo.com.cn": regexp.MustCompile(`(?i)^/finalpage/[0-9]{4}-[0-9]{2}-[0-9]{2}/([0-9]{5,20})\.pdf$`),
	"www.sse.com.cn":       regexp.MustCompile(`(?i)^/disclosure/listedinfo/announcement/(?:c/)?[a-z0-9_/-]+/([a-z0-9_-]+)\.pdf$`),
	"static.sse.com.cn":    regexp.MustCompile(`(?i)^/disclosure/listedinfo/announcement/(?:c/)?[a-z0-9_/-]+/([a-z0-9_-]+)\.pdf$`),
	"disc.static.szse.cn":  regexp.MustCompile(`(?i)^/download/disc/disk[0-9]+/finalpage/[0-9]{4}-[0-9]{2}-[0-9]{2}/([a-z0-9_-]+)\.pdf$`),
}

func filingURL(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || path.Clean(u.Path) != u.Path {
		return "", ""
	}
	pat := filingPaths[u.Host]
	if pat == nil {
		return "", ""
	}
	m := pat.FindStringSubmatch(u.Path)
	if m == nil {
		return "", ""
	}
	u.Scheme = "https"
	return u.String(), u.Host + "/" + m[1]
}

const filingPDFMax = 64 << 20
const filingTextMax = 12 << 20

func filingDownload(ctx context.Context, src Source, raw string) ([]byte, error) {
	link, id := filingURL(raw)
	if id == "" {
		return nil, errors.New("公告下载地址未登记")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if s, ok := src.(interface{ FilingTransport() http.RoundTripper }); ok {
		client.Transport = s.FilingTransport()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("公告下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("公告下载 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, filingPDFMax+1))
	if err != nil || len(b) > filingPDFMax || !bytes.HasPrefix(b, []byte("%PDF-")) {
		return nil, errors.New("公告原件无效或超出大小限制")
	}
	return b, nil
}

type filingTextBuffer struct{ buf bytes.Buffer }

func (b *filingTextBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > filingTextMax {
		return 0, errors.New("定期报告文本过长")
	}
	return b.buf.Write(p)
}

func filingText(ctx context.Context, pdf []byte) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The parser receives only bytes, no paths or credentials from the vendor
	// or Vera. The fixed executable, resource bounds and deadline apply even
	// if a PDF has malformed object streams or pathological page layouts.
	dir, err := os.MkdirTemp("", "vera-filing-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "/usr/bin/prlimit", "--as=536870912", "--cpu=20", "--fsize=0", "--nofile=64", "--",
		"/usr/bin/pdftotext", "-layout", "-enc", "UTF-8", "-", "-")
	cmd.Dir = dir
	cmd.Env = []string{"LANG=C.UTF-8", "PATH=/usr/bin:/bin", "HOME=" + dir}
	cmd.Stdin = bytes.NewReader(pdf)
	var text filingTextBuffer
	cmd.Stdout = &text
	if err := cmd.Run(); err != nil {
		return nil, errors.New("PDF 文字提取失败")
	}
	if !utf8.Valid(text.buf.Bytes()) || len(strings.TrimSpace(text.buf.String())) < 20 {
		return nil, errors.New("PDF 无可提取文字层")
	}
	return strings.Split(text.buf.String(), "\f"), nil
}

var filingHeadings = []struct {
	section string
	words   []string
}{
	{"risk_factors", []string{"公司面临的风险", "可能面对的风险", "可能面临的风险", "可能存在的风险", "风险因素", "风险与应对", "主要风险"}},
	{"outlook_plan", []string{"公司未来发展的展望", "未来发展展望", "未来发展的讨论", "经营计划", "公司发展战略", "未来发展战略"}},
	{"core_competence", []string{"核心竞争力", "竞争优势"}},
	{"business_overview", []string{"主要业务", "主营业务情况", "从事的业务", "主营业务简介", "业务概要", "主要产品", "公司所处行业"}},
	{"mdna", []string{"管理层讨论与分析", "经营情况讨论与分析", "主营业务分析", "经营情况的讨论", "经营情况回顾", "经营情况分析", "营业收入构成"}},
}

var numberedHeading = regexp.MustCompile(`^(?:第[一二三四五六七八九十百0-9]+[节章]|[一二三四五六七八九十]+[、.．]|[（(][一二三四五六七八九十0-9]+[）)]|[1-9][0-9]?[、．.])`)
var tocLine = regexp.MustCompile(`(?:\.{3,}|…{2,}|·{3,}|\s{3,})\s*[0-9]{1,4}\s*$`)
var pageFooter = regexp.MustCompile(`^(?:第)?[0-9]{1,4}(?:页)?(?:[/／共][0-9]{1,4}(?:页)?)?$`)

func headingType(line string) (section string, heading bool) {
	// Some embedded CJK fonts map 风 to U+2EDB (CJK radical C-simplified
	// wind), which has no Unicode compatibility decomposition.
	clean := strings.ReplaceAll(norm.NFKC.String(strings.Join(strings.Fields(line), "")), "⻛", "风")
	if utf8.RuneCountInString(clean) > 65 || strings.ContainsAny(clean, "。；：:，,") || tocLine.MatchString(line) {
		return "", false
	}
	for _, h := range filingHeadings {
		for _, w := range h.words {
			if strings.Contains(clean, w) && (numberedHeading.MatchString(clean) || strings.HasPrefix(clean, w) || strings.HasSuffix(clean, w)) {
				return h.section, true
			}
		}
	}
	// New top-level sections terminate the preceding topic; numbered
	// subparagraphs remain inside it (e.g. "（一）市场竞争加剧风险").
	return "", strings.HasPrefix(clean, "第") && numberedHeading.MatchString(clean) ||
		regexpMajorSub.MatchString(clean)
}

var regexpMajorSub = regexp.MustCompile(`^[一二三四五六七八九十]+、`)

func filingNarrative(a disclosure, pages []string, ref, sha string, received time.Time) []dataservice.Record {
	var records []dataservice.Record
	var section, heading string
	count := map[string]int{}
	for page, body := range pages {
		var para []string
		firstLine := 0
		flush := func() {
			t := strings.TrimSpace(strings.Join(para, ""))
			para = nil
			if section == "" || count[section] >= 24 || utf8.RuneCountInString(t) < 16 {
				return
			}
			// Require a sentence rather than a table row, page header or checklist.
			if !strings.ContainsAny(t, "。；！？") {
				return
			}
			count[section]++
			pid := fmt.Sprintf("p%04d.l%04d", page+1, firstLine)
			row := Row{RawRef: ref, RawSHA256: sha, RawLocator: fmt.Sprintf("PDF page=%d line=%d", page+1, firstLine), ReceivedAt: received}
			d := a.periodDate()
			r := a.record("doc.periodic_text", a.id+"|"+pid, "公司年度报告原文，PDF 物理页码", "L1", map[string]any{
				"report_period": d.Format(time.DateOnly), "announcement_id": a.id, "section": section, "paragraph_id": pid,
				"heading_path": heading, "text": t, "page": page + 1}, row)
			r.Period = &d
			records = append(records, r)
		}
		for n, raw := range strings.Split(body, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || tocLine.MatchString(line) || pageFooter.MatchString(strings.Join(strings.Fields(line), "")) {
				flush()
				continue
			}
			if strings.Contains(line, "年度报告") && utf8.RuneCountInString(line) < 45 && !strings.ContainsAny(line, "。；") {
				flush()
				continue
			}
			if sec, isHeading := headingType(line); isHeading {
				flush()
				section = sec
				heading = line
				continue
			}
			if section == "" {
				continue
			}
			if len(para) == 0 {
				firstLine = n + 1
			}
			para = append(para, line)
			if strings.HasSuffix(line, "。") || strings.HasSuffix(line, "；") || len(para) >= 12 {
				flush()
			}
		}
		flush()
	}
	return records
}

const deductedName = `归属于(?:上市公司股东|母公司(?:股东|所有者))的?扣除非经常性损益(?:后)?的?净利润`

var deductedLabel = regexp.MustCompile(deductedName + `[（(]元[）)]`)
var wrappedDeductedLabel = regexp.MustCompile("^" + deductedName + "$")
var firstYear = regexp.MustCompile(`(?:19|20)[0-9]{2}年`)
var deductionAmount = regexp.MustCompile(`^\s*([-−]?[0-9]+(?:,[0-9]{3})*(?:\.[0-9]+)?)(?:\s|$)`)

func compactFilingLine(line string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, norm.NFKC.String(line))
}

// Some annual tables put the currency and unit above the table. Poppler
// centers the four numeric cells between two lines of the ownership label.
func wrappedDeduction(lines []string, n int, year string) string {
	if n < 2 || n+1 >= len(lines) || !wrappedDeductedLabel.MatchString(compactFilingLine(lines[n-1]+lines[n+1])) {
		return ""
	}
	values := strings.Fields(lines[n])
	if len(values) != 4 {
		return ""
	}
	for _, v := range values {
		if !deductionAmount.MatchString(v) {
			return ""
		}
	}
	// The header and unit must belong to this same table on this page.
	for h := n - 2; h >= max(0, n-14); h-- {
		header := compactFilingLine(lines[h])
		years := firstYear.FindAllString(header, -1)
		if len(years) == 0 && !strings.Contains(header, "单位") && !strings.Contains(header, "币种") {
			continue
		}
		if len(years) != 3 || years[0] != year || years[1] >= years[0] || years[2] >= years[1] ||
			header != "主要会计数据"+strings.Join(years, "") {
			return ""
		}
		// Require the published percentage column in addition to the three
		// named years. Without it four numbers are ambiguous.
		before := compactFilingLine(strings.Join(lines[max(0, h-4):h], ""))
		after := compactFilingLine(strings.Join(lines[h+1:min(n-1, h+4)], ""))
		if strings.HasSuffix(before, "单位:元币种:人民币本期比上年同") && strings.HasPrefix(after, "期增减(%)") {
			return values[0]
		}
		return ""
	}
	return ""
}

// The generic indicator field "扣非净利润" does not establish ownership.
// Read only a labelled yuan row with a current-year first data column. Layouts
// without an unambiguous unit, label and year remain missing.
func filingDeducted(a disclosure, pages []string, ref, sha string, received time.Time) (dataservice.Record, bool) {
	var found dataservice.Record
	for p, body := range pages {
		var year string
		lines := strings.Split(body, "\n")
		for n, line := range lines {
			compact := compactFilingLine(line)
			if years := firstYear.FindAllString(compact, -1); len(years) >= 2 {
				year = years[0]
			}
			if year != a.period[:4]+"年" {
				continue
			}
			// Only strip split typography inside the label, preserving column
			// separators in the values. Look up the actual end of the unit.
			var raw string
			mappingVersion := 1
			if deductedLabel.MatchString(compact) {
				if unit := strings.IndexAny(line, "）)"); unit >= 0 {
					_, width := utf8.DecodeRuneInString(line[unit:])
					if m := deductionAmount.FindStringSubmatch(line[unit+width:]); m != nil {
						raw = m[1]
					}
				}
			} else if deductionAmount.MatchString(line) {
				raw = wrappedDeduction(lines, n, year)
				mappingVersion = 2
			}
			if raw == "" {
				continue
			}
			val, err := filingDecimal(strings.NewReplacer(",", "", "−", "-").Replace(raw))
			if err != nil {
				continue
			}
			if found.RecordKey != "" && found.Payload["value"] != val {
				return dataservice.Record{}, false
			}
			row := Row{RawRef: ref, RawSHA256: sha, RawLocator: fmt.Sprintf("PDF page=%d line=%d", p+1, n+1), ReceivedAt: received}
			found = a.statement(row, "IS.net_profit_parent_deducted", val, "年度报告主要会计数据：扣除非经常性损益后归母净利润（元）", "")
			found.SourceTier = "L1"
			found.MappingVersion = mappingVersion
		}
	}
	return found, found.RecordKey != ""
}

func filingDocument(ctx context.Context, src Source, a disclosure, withText bool) ([]dataservice.Record, string, string, error) {
	pdf, err := filingDownload(ctx, src, a.url)
	if err != nil {
		return nil, "", "", err
	}
	ref, sha, err := src.Archive(ctx, "pdf", pdf)
	if err != nil {
		return nil, "", "", errors.New("公告原件归档失败")
	}
	pages, err := filingText(ctx, pdf)
	if err != nil {
		return nil, ref, sha, err
	}
	cover := norm.NFKC.String(strings.Join(strings.Fields(strings.Join(pages[:min(len(pages), 3)], "")), ""))
	code, _, _ := strings.Cut(a.code, ".")
	if !strings.Contains(cover, a.period[:4]+"年") || !strings.Contains(cover, "年度报告") ||
		!strings.Contains(cover, code) && (a.row.Values["name"] == "" || !strings.Contains(cover, a.row.Values["name"])) {
		return nil, ref, sha, errors.New("PDF 标的、年度或报告身份未能核对")
	}
	received := time.Now().UTC()
	var records []dataservice.Record
	if withText {
		records = filingNarrative(a, pages, ref, sha, received)
		if len(records) == 0 {
			return nil, ref, sha, errors.New("年度报告未识别到带章节的原文")
		}
	}
	if r, ok := filingDeducted(a, pages, ref, sha, received); ok {
		records = append(records, r)
	}
	return records, ref, sha, nil
}
