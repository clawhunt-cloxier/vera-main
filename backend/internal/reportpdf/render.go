// Package reportpdf typesets a delivered report as an A4 PDF in pure Go, with the Source Han Sans fonts embedded.
package reportpdf

import (
	"bytes"
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
)

type Scenario struct {
	Trigger      string `json:"trigger"`
	Invalidation string `json:"invalidation"`
}

type Claim struct {
	Ref      string    `json:"ref"`
	Kind     string    `json:"kind"`
	Text     string    `json:"text"`
	Scenario *Scenario `json:"scenario"`
}

type Table struct {
	Title   string      `json:"title"`
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
}

type Chart struct {
	SeriesID string `json:"series_id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
}

type Section struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	ClaimRefs []string `json:"claim_refs"`
	Tables    []Table  `json:"tables"`
	Charts    []Chart  `json:"charts"`
	Note      *string  `json:"note"`
}

type Source struct {
	Provider  string `json:"provider"`
	DatasetID string `json:"dataset_id"`
	UpdatedAt string `json:"updated_at"`
}

type SourceFreshness struct {
	DatasetID   string  `json:"dataset_id"`
	Provider    string  `json:"provider"`
	EventAt     *string `json:"event_at"`
	PublishedAt *string `json:"published_at"`
	ReceivedAt  *string `json:"received_at"`
	AvailableAt *string `json:"available_at"`
	Period      *string `json:"period"`
	LatencyMode string  `json:"latency_mode"`
	Status      string  `json:"status"`
	Note        string  `json:"note"`
}

type Subject struct {
	Name   string  `json:"name"`
	Symbol *string `json:"symbol"`
}

// Report mirrors the API report document; the HTTP layer converts into it through JSON.
type Report struct {
	Title           string            `json:"title"`
	SkillID         string            `json:"skill_id"`
	Subject         Subject           `json:"subject"`
	DataAsOf        string            `json:"data_as_of"`
	ResultKind      string            `json:"result_kind"`
	Synthetic       bool              `json:"synthetic"`
	Version         int               `json:"version"`
	DeliveredAt     string            `json:"delivered_at"`
	Sections        []Section         `json:"sections"`
	Claims          []Claim           `json:"claims"`
	Limitations     []string          `json:"limitations"`
	Sources         []Source          `json:"sources"`
	SourceFreshness []SourceFreshness `json:"source_freshness"`
	Disclaimer      string            `json:"disclaimer"`
}

type SeriesLine struct {
	Name   string       `json:"name"`
	Points [][2]*string `json:"points"`
}

type Series struct {
	ID    string       `json:"series_id"`
	Title string       `json:"title"`
	Kind  string       `json:"kind"`
	X     string       `json:"x"`
	Unit  string       `json:"unit"`
	Lines []SeriesLine `json:"series"`
	Note  *string      `json:"note"`
}

const (
	pageW = 210.0
	pageH = 297.0
	mL    = 20.0
	mR    = 20.0
	mTop  = 24.0
	mBot  = 20.0
	cw    = pageW - mL - mR
)

var (
	cNavy   = canvas.Hex("#0B3D5C")
	cAccent = canvas.Hex("#1F7A8C")
	cText   = canvas.Hex("#1F2933")
	cMuted  = canvas.Hex("#6B7785")
	cSoft   = canvas.Hex("#BFD3E0")
	cLine   = canvas.Hex("#D5DCE3")
	cPanel  = canvas.Hex("#F3F6F9")
	cWhite  = canvas.Hex("#FFFFFF")
	cWarn   = canvas.Hex("#B54708")
	cWarnBg = canvas.Hex("#FEF3E2")
	cBad    = canvas.Hex("#B42318")
	cBadBg  = canvas.Hex("#FDE8E6")
	cOk     = canvas.Hex("#067647")
	cOkBg   = canvas.Hex("#DCF5E7")
	palette = []color.RGBA{canvas.Hex("#0B3D5C"), canvas.Hex("#E07A1F"), canvas.Hex("#2A9D8F"), canvas.Hex("#7B61FF"), canvas.Hex("#C2410C")}
)

type kindStyle struct {
	label string
	fg    color.RGBA
	bg    color.RGBA
}

var kinds = map[string]kindStyle{
	"fact":       {"事实", canvas.Hex("#334155"), canvas.Hex("#E8EDF3")},
	"computed":   {"计算", canvas.Hex("#0F766E"), canvas.Hex("#DDF3F0")},
	"inference":  {"推断", canvas.Hex("#4338CA"), canvas.Hex("#E6E7FB")},
	"attributed": {"引述", canvas.Hex("#92400E"), canvas.Hex("#FBEBCB")},
	"scenario":   {"情景", canvas.Hex("#6D28D9"), canvas.Hex("#EDE5FB")},
}

var kindOrder = []string{"fact", "computed", "inference", "attributed", "scenario"}

var skillTitle = map[string]string{"stock_analysis": "个股分析", "capital_flow": "资金动向", "deep_research": "深度研究"}

type outline struct {
	name  string
	level int
	y     float64
}

type page struct {
	c        *canvas.Canvas
	ctx      *canvas.Context
	outlines []outline
}

type doc struct {
	r      Report
	series map[string]Series
	fam    *canvas.FontFamily
	pages  []*page
	cur    *page
	y      float64
	num    map[string]int
	claims map[string]Claim
	tocIn  map[string]int
	tocOut map[string]int
	loc    *time.Location
}

var mu sync.Mutex

// Render typesets the report. Rendering is serialised because the embedded fonts are shared.
func Render(r Report, series map[string]Series) (out []byte, err error) {
	mu.Lock()
	defer mu.Unlock()
	defer func() {
		if v := recover(); v != nil {
			out, err = nil, fmt.Errorf("pdf render: %v", v)
		}
	}()
	f, err := family()
	if err != nil {
		return nil, err
	}
	d := &doc{r: r, series: series, fam: f, num: map[string]int{}, claims: map[string]Claim{}, tocIn: map[string]int{}, loc: time.FixedZone("CST", 8*3600)}
	for i, c := range r.Claims {
		d.num[c.Ref] = i + 1
		d.claims[c.Ref] = c
	}
	d.build() // first pass finds the page of every section
	d.tocIn = d.tocOut
	d.build() // second pass prints the table of contents with real page numbers
	d.chrome()
	var buf bytes.Buffer
	p := pdf.New(&buf, pageW, pageH, nil)
	p.SetInfo(r.Title, skillTitle[r.SkillID], "Vera,研究报告", "Vera", "Vera")
	p.SetLang("zh-CN")
	for i, pg := range d.pages {
		if i > 0 {
			p.NewPage(pageW, pageH)
		}
		pg.c.RenderTo(p)
		for _, o := range pg.outlines {
			p.AddOutline(o.name, o.level, o.y)
		}
	}
	if err := p.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *doc) build() {
	d.pages, d.cur = nil, nil
	d.tocOut = map[string]int{}
	d.cover()
	d.newPage()
	for _, s := range d.r.Sections {
		d.section(s)
	}
	d.limits()
}

func ypos(top float64) float64 { return pageH - top }

func (d *doc) face(pt float64, col color.RGBA, bold bool) *canvas.FontFace {
	style := canvas.FontRegular
	if bold {
		style = canvas.FontBold
	}
	return d.fam.Face(pt, col, style, canvas.FontNormal)
}

func (d *doc) newPage() {
	c := canvas.New(pageW, pageH)
	p := &page{c: c, ctx: canvas.NewContext(c)}
	d.pages = append(d.pages, p)
	d.cur = p
	d.y = mTop
}

func (d *doc) ensure(h float64) {
	if d.y+h > pageH-mBot {
		d.newPage()
	}
}

func (d *doc) mark(name string, level int) {
	d.cur.outlines = append(d.cur.outlines, outline{name, level, ypos(d.y)})
}

func (d *doc) para(face *canvas.FontFace, s string, w float64, al canvas.TextAlign) *canvas.Text {
	return canvas.NewTextBox(face, s, w, 0, al, canvas.Top, &canvas.TextOptions{LineStretch: 0.35})
}

func th(t *canvas.Text) float64 {
	b := t.Bounds()
	return b.Y1 - b.Y0
}

func (d *doc) put(x, top float64, t *canvas.Text) { d.cur.ctx.DrawText(x, ypos(top), t) }

func (d *doc) fill(col color.RGBA, x, top, w, h, r float64) {
	ctx := d.cur.ctx
	ctx.SetFillColor(col)
	ctx.SetStrokeColor(canvas.Transparent)
	var p *canvas.Path
	if r > 0 {
		p = canvas.RoundedRectangle(w, h, r)
	} else {
		p = canvas.Rectangle(w, h)
	}
	ctx.DrawPath(x, ypos(top)-h, p)
}

func (d *doc) hline(x, top, w float64, col color.RGBA, width float64) {
	ctx := d.cur.ctx
	ctx.SetFillColor(canvas.Transparent)
	ctx.SetStrokeColor(col)
	ctx.SetStrokeWidth(width)
	ctx.SetDashes(0)
	ctx.DrawPath(x, ypos(top), canvas.Line(w, 0))
	ctx.SetStrokeColor(canvas.Transparent)
}

func (d *doc) dots(x0, x1, top float64) {
	if x1-x0 < 4 {
		return
	}
	ctx := d.cur.ctx
	ctx.SetFillColor(canvas.Transparent)
	ctx.SetStrokeColor(cMuted)
	ctx.SetStrokeWidth(0.25)
	ctx.SetDashes(0, 0.25, 1.1)
	ctx.DrawPath(x0, ypos(top), canvas.Line(x1-x0, 0))
	ctx.SetDashes(0)
	ctx.SetStrokeColor(canvas.Transparent)
}

func (d *doc) pillW(label string, pt float64) float64 {
	return d.face(pt, cText, true).TextWidth(label) + 3.4
}

func (d *doc) pill(x, top float64, label string, fg, bg color.RGBA, pt float64) (float64, float64) {
	f := d.face(pt, fg, true)
	w := f.TextWidth(label) + 3.4
	h := pt*0.3528 + 2.4
	d.fill(bg, x, top, w, h, h/2)
	d.cur.ctx.DrawText(x+1.7, ypos(top+h/2)-0.134*pt, canvas.NewTextLine(f, label, canvas.Left))
	return w, h
}

func (d *doc) fmtTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.In(d.loc).Format("2006-01-02 15:04:05")
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func sp(s string) *string { return &s }

func cell(s *string) string {
	if s == nil {
		return "—"
	}
	return *s
}

func isNumber(s string) bool {
	s = strings.NewReplacer(",", "", "%", "", "+", "").Replace(strings.TrimSpace(s))
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// ---- cover ----

func (d *doc) cover() {
	d.newPage()
	d.mark("封面与目录", 0)
	d.fill(cNavy, 0, 0, pageW, 78, 0)
	d.fill(cAccent, 0, 78, pageW, 1.6, 0)
	d.put(mL, 17, d.para(d.face(12, cWhite, true), "VERA", 60, canvas.Left))
	d.put(mL, 25, d.para(d.face(8, cSoft, false), "可信金融研究 · AI 生成研究报告", 120, canvas.Left))
	title := d.para(d.face(24, cWhite, true), d.r.Title, cw, canvas.Left)
	d.put(mL, 38, title)
	sub := d.r.Subject.Name
	if d.r.Subject.Symbol != nil && *d.r.Subject.Symbol != "" {
		sub += " · " + *d.r.Subject.Symbol
	}
	d.put(mL, 38+th(title)+3, d.para(d.face(11, cSoft, false), sub, cw, canvas.Left))

	x := mL
	w, _ := d.pill(x, 86, "AI 生成", cWhite, cNavy, 8)
	x += w + 2.5
	if d.r.ResultKind == "full" {
		w, _ = d.pill(x, 86, "完整", cOk, cOkBg, 8)
	} else {
		w, _ = d.pill(x, 86, "受限", cWarn, cWarnBg, 8)
	}
	x += w + 2.5
	if d.r.Synthetic {
		d.pill(x, 86, "模拟数据", cBad, cBadBg, 8)
	}
	d.y = 100

	name := skillTitle[d.r.SkillID]
	if name == "" {
		name = d.r.SkillID
	}
	d.kv("研究对象", sub)
	d.kv("研究类型", name)
	d.kv("数据截止", d.r.DataAsOf)
	d.kv("报告版本", fmt.Sprintf("第 %d 版 · 交付于 %s（北京时间）", d.r.Version, d.fmtTime(d.r.DeliveredAt)))
	d.kv("结论条数", fmt.Sprintf("%d 条（含核心要点）", len(d.r.Claims)))
	d.y += 3
	if d.r.ResultKind != "full" {
		d.notice("本报告为受限版本：部分章节因数据不足或未通过核验而受限或未生成，原因见“数据与限制”。", cWarn, cWarnBg)
	}
	if d.r.Synthetic {
		d.notice("本报告基于虚构的演示数据或模拟模型生成，不对应任何真实公司或市场，仅用于演示。", cBad, cBadBg)
	}
	d.y += 3
	d.put(mL, d.y, d.para(d.face(12, cNavy, true), "目 录", 40, canvas.Left))
	d.y += 9
	for _, s := range d.r.Sections {
		label := s.ID + "  " + s.Title
		if s.Status == "omitted" {
			label += "（未生成）"
		}
		d.tocRow(label, s.ID, s.Status == "omitted")
	}
	d.tocRow("数据与限制", "limits", false)
}

func (d *doc) kv(k, v string) {
	t := d.para(d.face(10.5, cText, false), v, cw-30, canvas.Left)
	d.put(mL, d.y, d.para(d.face(9, cMuted, false), k, 26, canvas.Left))
	d.put(mL+30, d.y-0.3, t)
	h := math.Max(th(t), 4.5) + 3.2
	d.hline(mL, d.y+h-1.2, cw, cLine, 0.2)
	d.y += h
}

func (d *doc) notice(s string, accent, bg color.RGBA) {
	if strings.TrimSpace(s) == "" {
		return
	}
	t := d.para(d.face(8.5, cText, false), s, cw-8, canvas.Left)
	h := th(t) + 5
	d.ensure(h + 3)
	d.fill(bg, mL, d.y, cw, h, 1)
	d.fill(accent, mL, d.y, 1.2, h, 0)
	d.put(mL+4.5, d.y+2.5, t)
	d.y += h + 3
}

func (d *doc) tocRow(label, key string, dim bool) {
	col := cText
	if dim {
		col = cMuted
	}
	f := d.face(10, col, false)
	d.put(mL, d.y, d.para(f, label, cw-14, canvas.Left))
	pg := "—"
	if n := d.tocIn[key]; n > 0 {
		pg = strconv.Itoa(n)
	}
	d.cur.ctx.DrawText(mL+cw, ypos(d.y+3.5), canvas.NewTextLine(f, pg, canvas.Right))
	d.dots(mL+f.TextWidth(label)+2, mL+cw-f.TextWidth(pg)-2, d.y+3.2)
	d.y += 6.2
}

// ---- sections ----

func (d *doc) heading(text, status string) {
	d.fill(cAccent, mL, d.y+0.8, 1.4, 7, 0)
	d.cur.ctx.DrawText(mL+4, ypos(d.y+6.2), canvas.NewTextLine(d.face(14, cNavy, true), text, canvas.Left))
	if status != "" {
		w := d.pillW(status, 8)
		d.pill(mL+cw-w, d.y+1.8, status, cWarn, cWarnBg, 8)
	}
	d.hline(mL, d.y+10.4, cw, cLine, 0.25)
	d.y += 14
}

func (d *doc) section(s Section) {
	d.ensure(46)
	d.mark(s.ID+" "+s.Title, 0)
	d.tocOut[s.ID] = len(d.pages)
	status := ""
	switch s.Status {
	case "limited":
		status = "受限"
	case "omitted":
		status = "未生成"
	}
	d.heading(s.ID+"  "+s.Title, status)
	if s.Note != nil && *s.Note != "" {
		d.notice(*s.Note, cWarn, cWarnBg)
	}
	for _, ref := range s.ClaimRefs {
		if c, ok := d.claims[ref]; ok {
			d.claim(c)
		}
	}
	for _, t := range s.Tables {
		d.y += 1
		d.table(t)
		d.y += 4
	}
	for _, ch := range s.Charts {
		if ser, ok := d.series[ch.SeriesID]; ok {
			d.chart(ch, ser)
		}
	}
	d.y += 5
}

func (d *doc) claim(c Claim) {
	ks, ok := kinds[c.Kind]
	if !ok {
		ks = kinds["fact"]
	}
	body := d.face(10, cText, false)
	cite := d.face(8, cAccent, true)
	rt := canvas.NewRichText(body)
	rt.WriteFace(body, c.Text)
	if n := d.num[c.Ref]; n > 0 {
		rt.WriteFace(cite, " ["+strconv.Itoa(n)+"]")
	}
	const gut = 15.0
	t := rt.ToText(cw-gut, 0, canvas.Justify, canvas.Top, &canvas.TextOptions{LineStretch: 0.35, PunctuationInMargins: true})
	h := th(t)
	type row struct{ l, v *canvas.Text }
	var rows []row
	panel := 0.0
	if c.Kind == "scenario" && c.Scenario != nil {
		for _, kv := range [][2]string{{"触发条件", c.Scenario.Trigger}, {"失效条件", c.Scenario.Invalidation}} {
			if kv[1] == "" {
				continue
			}
			r := row{d.para(d.face(8, cMuted, true), kv[0], 16, canvas.Left), d.para(d.face(9, cText, false), kv[1], cw-gut-26, canvas.Left)}
			rows = append(rows, r)
			panel += math.Max(th(r.l), th(r.v)) + 1.4
		}
		if panel > 0 {
			panel += 2.6
		}
	}
	total := h + 3.4
	if panel > 0 {
		total += panel + 1.6
	}
	d.ensure(total)
	d.pill(mL, d.y+0.5, ks.label, ks.fg, ks.bg, 7.5)
	d.put(mL+gut, d.y, t)
	if panel > 0 {
		py := d.y + h + 1.6
		d.fill(cPanel, mL+gut, py, cw-gut, panel, 1)
		d.fill(ks.fg, mL+gut, py, 0.8, panel, 0)
		ry := py + 1.8
		for _, r := range rows {
			d.put(mL+gut+4, ry, r.l)
			d.put(mL+gut+22, ry, r.v)
			ry += math.Max(th(r.l), th(r.v)) + 1.4
		}
	}
	d.y += total
}

// ---- tables ----

// cellText turns enum values stored in the data into reader-facing labels.
var cellText = map[string]string{"institution": "机构专用", "branch": "营业部", "connect": "沪深股通", "product": "按产品", "region": "按地区"}

func (d *doc) table(t Table) {
	nc := len(t.Columns)
	if nc == 0 {
		return
	}
	hf := d.face(8.5, cWhite, true)
	bf := d.face(8.5, cText, false)
	capT := d.para(d.face(9, cNavy, true), t.Title, cw, canvas.Left)
	d.ensure(th(capT) + 22)
	d.put(mL, d.y, capT)
	d.y += th(capT) + 2
	const pad = 2.6
	rows := make([][]string, len(t.Rows))
	for i, row := range t.Rows {
		rows[i] = make([]string, nc)
		for j := 0; j < nc && j < len(row); j++ {
			s := cell(row[j])
			if v, ok := cellText[s]; ok {
				s = v
			}
			rows[i][j] = s
		}
	}
	one := bf.TextWidth("国")
	nat := make([]float64, nc)
	minW := make([]float64, nc)
	numeric := make([]bool, nc)
	for j := range nat {
		hw := hf.TextWidth(t.Columns[j])
		nat[j] = hw
		minW[j] = 4 * one
		if len([]rune(t.Columns[j])) <= 5 {
			minW[j] = hw
		}
		numeric[j] = true
	}
	for _, row := range rows {
		for j, s := range row {
			w := bf.TextWidth(s)
			nat[j] = math.Max(nat[j], w)
			if len([]rune(s)) <= 16 {
				minW[j] = math.Max(minW[j], w)
			}
			if s != "—" && !isNumber(s) {
				numeric[j] = false
			}
		}
	}
	numeric[0] = false
	totalNat, totalMin, spare := 0.0, 0.0, 0.0
	for j := range nat {
		minW[j] += 2*pad + 0.4
		nat[j] = math.Max(math.Min(nat[j], 60)+2*pad, minW[j])
		totalNat += nat[j]
		totalMin += minW[j]
		spare += nat[j] - minW[j]
	}
	widths := make([]float64, nc)
	for j := range widths {
		switch {
		case totalNat <= cw:
			widths[j] = nat[j] * cw / totalNat
		case totalMin >= cw || spare <= 0:
			widths[j] = minW[j] * cw / totalMin
		default:
			widths[j] = minW[j] + (nat[j]-minW[j])*(cw-totalMin)/spare
		}
	}
	var draw func(cells []string, face *canvas.FontFace, bg *color.RGBA, header bool)
	draw = func(cells []string, face *canvas.FontFace, bg *color.RGBA, header bool) {
		boxes := make([]*canvas.Text, nc)
		dx := make([]float64, nc)
		h := 0.0
		for j := 0; j < nc; j++ {
			s := ""
			if j < len(cells) {
				s = cells[j]
			}
			avail := widths[j] - 2*pad
			boxes[j] = d.para(face, s, avail, canvas.Left)
			if w := face.TextWidth(s); numeric[j] && w <= avail {
				dx[j] = avail - w
			}
			h = math.Max(h, th(boxes[j]))
		}
		h += 3.4
		if d.y+h > pageH-mBot {
			d.newPage()
			if !header {
				navy := cNavy
				draw(t.Columns, hf, &navy, true)
			}
		}
		if bg != nil {
			d.fill(*bg, mL, d.y, cw, h, 0)
		}
		x := mL
		for j := 0; j < nc; j++ {
			d.put(x+pad+dx[j], d.y+1.7, boxes[j])
			x += widths[j]
		}
		if !header {
			d.hline(mL, d.y+h, cw, cLine, 0.2)
		}
		d.y += h
	}
	navy := cNavy
	draw(t.Columns, hf, &navy, true)
	for i, row := range rows {
		var bg *color.RGBA
		if i%2 == 1 {
			p := cPanel
			bg = &p
		}
		draw(row, bf, bg, false)
	}
}

// ---- charts ----

func niceTicks(lo, hi float64, n int) []float64 {
	span := hi - lo
	if span <= 0 {
		span = 1
	}
	raw := span / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	f := raw / mag
	step := 10 * mag
	switch {
	case f <= 1:
		step = mag
	case f <= 2:
		step = 2 * mag
	case f <= 5:
		step = 5 * mag
	}
	start := math.Floor(lo/step+1e-9) * step
	var out []float64
	for i := 0; i < 40; i++ {
		v := start + float64(i)*step
		out = append(out, v)
		if v >= hi-1e-9*step {
			break
		}
	}
	return out
}

func fmtTick(v, step float64) string {
	dec := 0
	if step < 1 {
		dec = int(math.Ceil(-math.Log10(step) - 1e-9))
	}
	if dec > 3 {
		dec = 3
	}
	if math.Abs(v) < step*1e-6 {
		v = 0
	}
	return strconv.FormatFloat(v, 'f', dec, 64)
}

func fmtX(kind, raw string) string {
	if kind == "report_period" && len(raw) >= 4 && strings.HasSuffix(raw, "-12-31") {
		return raw[:4]
	}
	return raw
}

func alpha(c color.RGBA, a float64) color.RGBA {
	return color.RGBA{uint8(float64(c.R) * a), uint8(float64(c.G) * a), uint8(float64(c.B) * a), uint8(255 * a)}
}

func (d *doc) chart(ch Chart, s Series) {
	const h = 68.0
	d.ensure(h + 4)
	title := s.Title
	if title == "" {
		title = ch.Title
	}
	d.put(mL, d.y, d.para(d.face(9.5, cNavy, true), title, cw-45, canvas.Left))
	if s.Unit != "" {
		d.cur.ctx.DrawText(mL+cw, ypos(d.y+3.4), canvas.NewTextLine(d.face(7.5, cMuted, false), "单位："+s.Unit, canvas.Right))
	}
	lf := d.face(7.5, cText, false)
	lx := mL
	for i, ln := range s.Lines {
		label := ln.Name
		if k := len(ln.Points); k > 0 && ln.Points[k-1][1] != nil {
			label += " " + *ln.Points[k-1][1]
		}
		d.fill(palette[i%len(palette)], lx, d.y+7.2, 3.2, 1.4, 0.3)
		d.cur.ctx.DrawText(lx+4.4, ypos(d.y+8.6), canvas.NewTextLine(lf, label, canvas.Left))
		lx += 4.4 + lf.TextWidth(label) + 5
	}
	px0, px1 := mL+11.0, mL+cw-1.0
	py0, py1 := d.y+13.0, d.y+h-7.0

	n, longest := 0, 0
	for i, ln := range s.Lines {
		if len(ln.Points) > n {
			n, longest = len(ln.Points), i
		}
	}
	vals := make([][]float64, len(s.Lines))
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, ln := range s.Lines {
		vals[i] = make([]float64, n)
		for k := range vals[i] {
			vals[i][k] = math.NaN()
		}
		for k, p := range ln.Points {
			if p[1] == nil {
				continue
			}
			v, err := strconv.ParseFloat(*p[1], 64)
			if err != nil {
				continue
			}
			vals[i][k] = v
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if n == 0 || math.IsInf(lo, 0) {
		d.put(mL, d.y+h/2, d.para(d.face(9, cMuted, false), "暂无可绘制的数据", cw, canvas.Left))
		d.y += h
		return
	}
	isBar := s.Kind == "bar"
	if isBar {
		lo, hi = math.Min(lo, 0), math.Max(hi, 0)
	} else {
		span := hi - lo
		if span == 0 {
			span = math.Abs(hi) * 0.1
			if span == 0 {
				span = 1
			}
		}
		lo -= span * 0.08
		hi += span * 0.08
	}
	ticks := niceTicks(lo, hi, 4)
	lo, hi = ticks[0], ticks[len(ticks)-1]
	step := 1.0
	if len(ticks) > 1 {
		step = ticks[1] - ticks[0]
	}
	yAt := func(v float64) float64 { return py1 - (v-lo)/(hi-lo)*(py1-py0) }
	gw := (px1 - px0) / float64(n)
	xAt := func(k int) float64 {
		if isBar {
			return px0 + (float64(k)+0.5)*gw
		}
		if n == 1 {
			return (px0 + px1) / 2
		}
		return px0 + float64(k)/float64(n-1)*(px1-px0)
	}
	tf := d.face(6.5, cMuted, false)
	for _, t := range ticks {
		d.hline(px0, yAt(t), px1-px0, cLine, 0.15)
		d.cur.ctx.DrawText(px0-1.6, ypos(yAt(t))-0.8, canvas.NewTextLine(tf, fmtTick(t, step), canvas.Right))
	}
	d.hline(px0, py1, px1-px0, cMuted, 0.25)

	var idx []int
	if isBar && n <= 10 {
		for i := 0; i < n; i++ {
			idx = append(idx, i)
		}
	} else {
		k := 6
		if n < k {
			k = n
		}
		for j := 0; j < k; j++ {
			if k == 1 {
				idx = append(idx, 0)
			} else {
				idx = append(idx, int(math.Round(float64(j)*float64(n-1)/float64(k-1))))
			}
		}
	}
	for _, i := range idx {
		if i < len(s.Lines[longest].Points) && s.Lines[longest].Points[i][0] != nil {
			d.cur.ctx.DrawText(xAt(i), ypos(py1)-4.0, canvas.NewTextLine(tf, fmtX(s.X, *s.Lines[longest].Points[i][0]), canvas.Center))
		}
	}

	ctx := d.cur.ctx
	if isBar {
		yz := yAt(0)
		bw := gw * 0.7 / float64(len(s.Lines))
		vf := d.face(6.5, cText, false)
		for i := range s.Lines {
			col := palette[i%len(palette)]
			for k := 0; k < n; k++ {
				v := vals[i][k]
				if math.IsNaN(v) {
					continue
				}
				x := px0 + float64(k)*gw + gw*0.15 + float64(i)*bw
				ty := math.Min(yz, yAt(v))
				d.fill(col, x, ty, bw*0.92, math.Max(math.Abs(yz-yAt(v)), 0.15), 0)
				if n*len(s.Lines) <= 14 {
					ctx.DrawText(x+bw*0.46, ypos(ty)+1.0, canvas.NewTextLine(vf, *s.Lines[i].Points[k][1], canvas.Center))
				}
			}
		}
	} else {
		for i := range s.Lines {
			col := palette[i%len(palette)]
			line := &canvas.Path{}
			area := &canvas.Path{}
			started := false
			lastX, lastY := 0.0, 0.0
			for k := 0; k < n; k++ {
				v := vals[i][k]
				if math.IsNaN(v) {
					started = false
					continue
				}
				x, y := xAt(k), ypos(yAt(v))
				if !started {
					line.MoveTo(x, y)
					if s.Kind == "area" && i == 0 && area.Empty() {
						area.MoveTo(x, ypos(py1))
						area.LineTo(x, y)
					}
					started = true
				} else {
					line.LineTo(x, y)
					if s.Kind == "area" && i == 0 {
						area.LineTo(x, y)
					}
				}
				lastX, lastY = x, y
			}
			if s.Kind == "area" && i == 0 && !area.Empty() {
				area.LineTo(lastX, ypos(py1))
				area.Close()
				ctx.SetFillColor(alpha(col, 0.14))
				ctx.SetStrokeColor(canvas.Transparent)
				ctx.DrawPath(0, 0, area)
			}
			ctx.SetFillColor(canvas.Transparent)
			ctx.SetStrokeColor(col)
			ctx.SetStrokeWidth(0.55)
			ctx.SetDashes(0)
			ctx.DrawPath(0, 0, line)
			ctx.SetFillColor(col)
			ctx.SetStrokeColor(canvas.Transparent)
			ctx.DrawPath(lastX, lastY, canvas.Circle(0.7))
		}
	}
	ctx.SetStrokeColor(canvas.Transparent)
	if s.Note != nil && *s.Note != "" {
		d.put(mL, d.y+h-3, d.para(d.face(7, cMuted, false), *s.Note, cw, canvas.Left))
	}
	d.y += h + 2
}

// ---- closing pages ----

func (d *doc) limits() {
	d.ensure(46)
	d.mark("数据与限制", 0)
	d.tocOut["limits"] = len(d.pages)
	d.heading("数据与限制", "")
	bf := d.face(9.5, cText, false)
	for _, l := range d.r.Limitations {
		t := d.para(bf, l, cw-5, canvas.Left)
		d.ensure(th(t) + 2)
		d.put(mL, d.y, d.para(bf, "•", 4, canvas.Left))
		d.put(mL+5, d.y, t)
		d.y += th(t) + 2
	}
	d.y += 3
	if len(d.r.Sources) > 0 {
		rows := make([][]*string, 0, len(d.r.Sources))
		for _, s := range d.r.Sources {
			rows = append(rows, []*string{sp(s.Provider), sp(s.DatasetID), sp(d.fmtTime(s.UpdatedAt))})
		}
		d.table(Table{Title: "数据来源", Columns: []string{"数据提供方", "数据集", "更新至"}, Rows: rows})
	}
	for _, source := range d.r.SourceFreshness {
		value := func(v *string) *string {
			if v == nil {
				return sp("未确认 / 不适用")
			}
			return sp(d.fmtTime(*v))
		}
		latency := map[string]string{"real_time": "实时", "delayed": "延迟", "unknown": "未知"}[source.LatencyMode]
		status := map[string]string{"fresh": "新鲜", "stale": "过期", "unavailable": "不可用", "unknown": "未知"}[source.Status]
		period := "未确认 / 不适用"
		if source.Period != nil {
			period = *source.Period
		}
		d.table(Table{Title: source.Provider + " · " + source.DatasetID + " 来源时点", Columns: []string{"字段", "实际时点 / 状态"}, Rows: [][]*string{
			{sp("行情事件"), value(source.EventAt)}, {sp("披露 / 发布"), value(source.PublishedAt)},
			{sp("系统接收"), value(source.ReceivedAt)}, {sp("系统可用"), value(source.AvailableAt)},
			{sp("期间"), sp(period)}, {sp("延迟性质"), sp(latency)}, {sp("状态"), sp(status)}, {sp("说明"), sp(source.Note)},
		}})
	}
	d.y += 5
	d.legend()
	d.notice(d.r.Disclaimer, cMuted, cPanel)
}

func (d *doc) legend() {
	d.ensure(72)
	d.put(mL, d.y, d.para(d.face(10.5, cNavy, true), "阅读指引", 60, canvas.Left))
	d.y += 8
	desc := map[string]string{
		"fact":       "披露数据的直接陈述，可对应到来源记录。",
		"computed":   "由固定公式依据披露数据计算得出，输入与公式版本可追溯。",
		"inference":  "基于事实与计算的归纳判断，经独立核验，不构成预测。",
		"attributed": "公司或第三方的表述，仅作引述，不是本系统的判断。",
		"scenario":   "条件化情景，给出触发与失效条件，不是对价格或时间的预测。",
	}
	f := d.face(8.5, cText, false)
	for _, k := range kindOrder {
		ks := kinds[k]
		t := d.para(f, desc[k], cw-16, canvas.Left)
		d.pill(mL, d.y+0.3, ks.label, ks.fg, ks.bg, 7.5)
		d.put(mL+16, d.y, t)
		d.y += math.Max(th(t)+1.8, 6.4)
	}
	t := d.para(d.face(8.5, cMuted, false), "正文中的 [n] 为结论编号，对应网页版报告里的证据与计算过程。", cw, canvas.Left)
	d.put(mL, d.y+1, t)
	d.y += th(t) + 6
}

// chrome draws headers and footers once the page count is known.
func (d *doc) chrome() {
	n := len(d.pages)
	small := d.face(7.5, cMuted, false)
	for i, pg := range d.pages {
		d.cur = pg
		ctx := pg.ctx
		if i > 0 {
			ctx.DrawText(mL, ypos(12.5), canvas.NewTextLine(small, "VERA · "+trunc(d.r.Title, 30), canvas.Left))
			x := pageW - mR
			if d.r.Synthetic {
				w := d.pillW("模拟数据", 6.5)
				d.pill(x-w, 8.6, "模拟数据", cBad, cBadBg, 6.5)
				x -= w + 2
			}
			ctx.DrawText(x, ypos(12.5), canvas.NewTextLine(small, "数据截止 "+d.r.DataAsOf, canvas.Right))
			d.hline(mL, 16, cw, cLine, 0.2)
		}
		d.hline(mL, pageH-15, cw, cLine, 0.2)
		ctx.DrawText(mL, 9.5, canvas.NewTextLine(small, "AI 生成内容，仅供研究参考，不构成投资建议。", canvas.Left))
		ctx.DrawText(pageW-mR, 9.5, canvas.NewTextLine(small, fmt.Sprintf("第 %d / %d 页", i+1, n), canvas.Right))
	}
}
