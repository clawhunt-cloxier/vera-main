package harness

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

var sectionNames = map[string]string{"business_overview": "主营业务", "mdna": "经营情况讨论与分析", "risk_factors": "风险因素",
	"core_competence": "核心竞争力", "outlook_plan": "未来发展规划", "other": "其他"}

// New periodic announcements can supersede an archived PDF, including when
// parsing the replacement failed. Select from disclosure metadata first.
func periodicDocuments(rows []dataservice.StoredRecord) map[string]dataservice.StoredRecord {
	byPeriod := map[string]dataservice.StoredRecord{}
	for _, r := range rows {
		if str(r, "category") != "periodic_report" || r.Period == nil || r.PublishedAt == nil {
			continue
		}
		p := r.Period.Format("2006-01-02")
		old, ok := byPeriod[p]
		if !ok || r.PublishedAt.After(*old.PublishedAt) {
			byPeriod[p] = r
		} else if r.PublishedAt.Equal(*old.PublishedAt) && str(old, "announcement_id") != str(r, "announcement_id") {
			// Multiple different versions with indistinguishable disclosure times.
			old.Payload = nil
			byPeriod[p] = old
		}
	}
	return byPeriod
}

// periodicText adds paragraphs of the latest annual report (and older outlook paragraphs when history > 1).
// Paragraphs that look like instructions are flagged suspicious and never offered to models.
func (b *builder) periodicText(e uuid.UUID, sections []string, maxPer, clipLen, history int) int {
	rs := b.B.Rows("doc.periodic_text", e)
	if len(rs) == 0 {
		return 0
	}
	b.sources(rs)
	var periods []string
	docs := periodicDocuments(b.B.Rows("doc.announcement", e))
	if !b.B.Subject.Synthetic {
		for period := range docs {
			if strings.HasSuffix(period, "-12-31") {
				periods = append(periods, period)
			}
		}
	} else {
		for _, r := range rs {
			if p := str(r, "report_period"); strings.HasSuffix(p, "-12-31") && !slices.Contains(periods, p) {
				periods = append(periods, p)
			}
		}
	}
	slices.Sort(periods)
	if len(periods) > history {
		periods = periods[len(periods)-history:]
	}
	subj := b.name(e)
	n := 0
	counts := map[string]int{}
	for _, r := range rs {
		p := str(r, "report_period")
		sec := str(r, "section")
		if !slices.Contains(periods, p) || !slices.Contains(sections, sec) {
			continue
		}
		if !b.B.Subject.Synthetic && (str(docs[p], "announcement_id") == "" || str(docs[p], "announcement_id") != str(r, "announcement_id")) {
			continue
		}
		if sec == "outlook_plan" && p != periods[len(periods)-1] && history == 1 {
			continue
		}
		ck := p + sec
		if counts[ck] >= maxPer {
			continue
		}
		text := str(r, "text")
		f := b.S.TextFact("text."+sec, subj, p[:4]+" 年年度报告·"+sectionNames[sec], p[:4]+" 年度（"+p+"）", facts.KindText,
			"公司定期报告原文（"+str(r, "announcement_id")+" "+str(r, "heading_path")+"）", clip(text, clipLen), r.ID)
		if check.Suspicious(text) {
			f.Flag("suspicious")
			f.Text = ""
			b.S.Suspicious++
			continue
		}
		counts[ck]++
		n++
	}
	return n
}

// news adds authorised news and announcements of the last 30 days (L3/L1 background, max 5).
func (b *builder) news(e uuid.UUID, group string) int {
	from := b.B.AsOf.AddDate(0, 0, -30)
	subj := b.name(e)
	n := 0
	for _, ds := range []string{"doc.announcement", "news.item"} {
		rs := b.B.Rows(ds, e)
		for i := len(rs) - 1; i >= 0 && n < 5; i-- {
			r := rs[i]
			if r.PublishedAt == nil || r.PublishedAt.Before(from) || r.PublishedAt.After(b.B.AsOf) {
				continue
			}
			b.source(r)
			when := r.PublishedAt.In(cst).Format("2006-01-02")
			var text, label, basis string
			if ds == "news.item" {
				text = str(r, "title") + "。" + str(r, "body")
				label, basis = "新闻："+str(r, "title"), "授权媒体 "+str(r, "source")+"（L3，非公司公告）"
			} else {
				text = str(r, "title")
				label, basis = "公告："+str(r, "title"), "公司公告 "+str(r, "announcement_id")
			}
			f := b.S.TextFact(group, subj, label, when, facts.KindText, basis, clip(text, 300), r.ID)
			if check.Suspicious(text) {
				f.Flag("suspicious")
				f.Text = ""
				b.S.Suspicious++
				continue
			}
			n++
		}
	}
	return n
}

// profile adds classification and listing facts.
func (b *builder) profile(e uuid.UUID, group string) {
	subj := b.name(e)
	if rs := b.B.Rows("ref.security", e); len(rs) > 0 {
		r := rs[len(rs)-1]
		b.sources(rs)
		b.S.Note(group, subj, "上市日期", str(r, "list_date"), facts.KindReported, str(r, "list_date"), r.ID)
		if ts, ok := input(r, "total_shares", "shares"); ok {
			b.S.Value(group, subj, "总股本", "截至 "+b.B.AsOf.Format("2006-01-02"), facts.KindReported, "", "shares", ts.Value, r.ID)
		}
	}
	if r, ok := activeIndustry(b.B.Rows("ref.industry", e), b.B.AsOf); ok {
		b.source(r)
		b.S.Note(group, subj, "所属行业（"+str(r, "taxonomy")+"）", "截至 "+b.B.AsOf.Format("2006-01-02"), facts.KindReported,
			str(r, "level1_name")+" > "+str(r, "level2_name"), r.ID)
	}
}

func annualSegmentRows(b *Bundle, e uuid.UUID) (string, []dataservice.StoredRecord) {
	rows := b.Rows("fin.segment", e)
	latest := ""
	filing := ""
	if b.Subject.Synthetic {
		for _, r := range rows {
			if p := str(r, "report_period"); strings.HasSuffix(p, "-12-31") {
				latest = max(latest, p)
			}
		}
	} else {
		docs := periodicDocuments(b.Rows("doc.announcement", e))
		for p := range docs {
			if strings.HasSuffix(p, "-12-31") {
				latest = max(latest, p)
			}
		}
		filing = str(docs[latest], "announcement_id")
		if filing == "" {
			return "", nil
		}
	}
	var selected []dataservice.StoredRecord
	for _, r := range rows {
		if latest != "" && str(r, "report_period") == latest && (b.Subject.Synthetic || str(r, "filing_id") == filing) {
			selected = append(selected, r)
		}
	}
	return latest, selected
}

// segments adds latest-year segment revenue, share and gross margin.
func (b *builder) segments(e uuid.UUID, group, section string) bool {
	latest, rs := annualSegmentRows(b.B, e)
	if len(rs) == 0 {
		return false
	}
	b.sources(rs)
	subj := b.name(e)
	revs := map[string][]facts.Input{}
	incomplete := map[string]bool{}
	var cur []int
	for i, r := range rs {
		dim := str(r, "dimension")
		in, ok := input(r, "revenue", "CNY")
		if !ok {
			incomplete[dim] = true
			continue
		}
		revs[dim] = append(revs[dim], in)
		cur = append(cur, i)
	}
	pl := latest[:4] + " 年度（" + latest + "）"
	t := facts.Table{Title: subj + " " + latest[:4] + " 年分业务收入", Columns: []string{"维度", "业务", "收入（亿元）", "毛利率"}}
	for _, i := range cur {
		r := rs[i]
		name := str(r, "segment_name")
		dim := str(r, "dimension")
		dimLabel := map[string]string{"product": "产品", "industry": "行业", "region": "地区"}[dim]
		in, _ := input(r, "revenue", "CNY")
		b.S.Value(group, subj, name+"收入", pl, facts.KindReported, "公司定期报告分部信息", "CNY", in.Value, r.ID)
		b.S.Computed(group, subj, name+"占已披露"+dimLabel+"分项收入比例", pl, facts.Derived("F08.segment_share", "ratio", append([]facts.Input{in}, revs[dim]...),
			func() (*decimal.Decimal, string) {
				t := decimal.Zero
				if incomplete[dim] {
					return nil, "本维度存在缺失的分部收入"
				}
				for _, x := range revs[dim] {
					t = t.Add(x.Value)
				}
				if !t.IsPositive() {
					return nil, "分部收入合计不为正"
				}
				v := in.Value.Div(t)
				return &v, ""
			}), fmt.Sprintf("%s|seg|%s|%s", b.B.Entities[e].Code, dim, name))
		gm := "—"
		if v, ok := dec(r, "gross_margin"); ok {
			b.S.Value(group, subj, name+"毛利率", pl, facts.KindReported, "公司定期报告分部信息", "ratio", v, r.ID)
			gm = v.Mul(decimal.NewFromInt(100)).StringFixed(2) + "%"
		}
		t.Rows = append(t.Rows, []*string{facts.Ptr(dimLabel), facts.Ptr(name),
			facts.Ptr(in.Value.Div(decimal.NewFromInt(100_000_000)).StringFixed(2)), facts.Ptr(gm)})
		t.Evidence = append(t.Evidence, r.ID)
	}
	b.S.Tables[section] = append(b.S.Tables[section], t)
	return len(cur) > 0
}
