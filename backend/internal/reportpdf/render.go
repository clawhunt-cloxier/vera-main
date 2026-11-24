// Package reportpdf typesets a delivered report as an A4 PDF in pure Go, with the Source Han Sans fonts embedded.
package reportpdf

import (
	"bytes"
	"fmt"
	"image/color"
	"math"
	"slices"
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
	cAccent = canvas.Hex("#145EC5")
	cBrand  = canvas.Hex("#30272C")
	cInfoBg = canvas.Hex("#EAF2FD")
	cText   = canvas.Hex("#222A34")
	cMuted  = canvas.Hex("#586474")
	cLine   = canvas.Hex("#DDE3EA")
	cPanel  = canvas.Hex("#F5F7FA")
	cWarn   = canvas.Hex("#785012")
	cWarnBg = canvas.Hex("#F8EFE0")
	cBad    = canvas.Hex("#A23631")
	cBadBg  = canvas.Hex("#FBECEB")
	cOk     = canvas.Hex("#216047")
	cOkBg   = canvas.Hex("#E8F3EC")
	palette = []color.RGBA{cAccent, canvas.Hex("#A85D28"), canvas.Hex("#1B7568"), canvas.Hex("#6B5797"), canvas.Hex("#A23631")}
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
	d.fill(bg, x, top, w, h, 1)
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
	d.cur.ctx.DrawText(mL, ypos(22), canvas.NewTextLine(d.face(14, cBrand, true), "Vera", canvas.Left))
	d.cur.ctx.DrawText(pageW-mR, ypos(22), canvas.NewTextLine(d.face(9, cMuted, false), "研究报告", canvas.Right))
	d.hline(mL, 26, cw, cLine, 0.3)
	d.hline(mL, 26, 14, cAccent, 0.55)
	title := d.para(d.face(24, cText, true), d.r.Title, cw, canvas.Left)
	d.put(mL, 35, title)
	d.y = 35 + th(title) + 6

	sub := d.r.Subject.Name
	if d.r.Subject.Symbol != nil && *d.r.Subject.Symbol != "" {
		sub += " · " + *d.r.Subject.Symbol
	}

	x := mL
	w, h := d.pill(x, d.y, "AI 生成", cAccent, cInfoBg, 8)
	x += w + 2.5
	if d.r.ResultKind == "full" {
		w, _ = d.pill(x, d.y, "完整", cOk, cOkBg, 8)
	} else {
		w, _ = d.pill(x, d.y, "受限", cWarn, cWarnBg, 8)
	}
	x += w + 2.5
	if d.r.Synthetic {
		d.pill(x, d.y, "模拟数据", cBad, cBadBg, 8)
	}
	d.y += h + 8

	name := skillTitle[d.r.SkillID]
	if name == "" {
		name = d.r.SkillID
	}
	d.kv("研究对象", sub)
	d.kv("研究类型", name)
	d.kv("数据截止", d.r.DataAsOf)
	d.kv("报告版本", fmt.Sprintf("第 %d 版 · 交付于 %s（北京时间）", d.r.Version, d.fmtTime(d.r.DeliveredAt)))
	d.y += 3
	if d.r.ResultKind != "full" {
		d.notice("本报告为受限版本：部分章节因数据不足或未通过核验而受限或未生成，原因见“数据与限制”。", cWarn, cWarnBg)
	}
	if d.r.Synthetic {
		d.notice("本报告基于虚构的演示数据或模拟模型生成，不对应任何真实公司或市场，仅用于演示。", cBad, cBadBg)
	}
	d.y += 5
	d.tocHeading()
	for _, s := range d.r.Sections {
		label := s.ID + "  " + s.Title
		switch s.Status {
		case "limited":
			label += "（受限）"
		case "omitted":
			label += "（未生成）"
		}
		d.tocRow(label, s.ID, s.Status == "omitted")
	}
	d.tocRow("数据与限制", "limits", false)
}

func (d *doc) kv(k, v string) {
	t := d.para(d.face(10, cText, false), v, cw-30, canvas.Left)
	h := math.Max(th(t), 4.5) + 3.2
	d.ensure(h)
	d.put(mL, d.y, d.para(d.face(9, cMuted, false), k, 26, canvas.Left))
	d.put(mL+30, d.y, t)
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

func (d *doc) tocHeading() {
	d.ensure(20)
	d.put(mL, d.y, d.para(d.face(13, cText, true), "目录", 40, canvas.Left))
	d.hline(mL, d.y+7.5, cw, cLine, 0.25)
	d.y += 11
}

func (d *doc) tocRow(label, key string, dim bool) {
	col := cText
	if dim {
		col = cMuted
	}
	f := d.face(10, col, false)
	t := d.para(f, label, cw-14, canvas.Left)
	h := math.Max(7, th(t)+3.4)
	if d.y+h > pageH-mBot {
		d.newPage()
		d.tocHeading()
	}
	d.put(mL, d.y, t)
	pg := "—"
	if n := d.tocIn[key]; n > 0 {
		pg = strconv.Itoa(n)
	}
	d.cur.ctx.DrawText(mL+cw, ypos(d.y+3.5), canvas.NewTextLine(f, pg, canvas.Right))
	if t.Lines() == 1 {
		d.dots(mL+f.TextWidth(label)+2, mL+cw-f.TextWidth(pg)-2, d.y+3.2)
	}
	d.y += h
}

// ---- sections ----

func (d *doc) heading(key, text, status string) {
	width := cw - 4
	if status != "" {
		width -= d.pillW(status, 8) + 4
	}
	t := d.para(d.face(14, cText, true), text, width, canvas.Left)
	h := math.Max(6, th(t))
	d.ensure(h + 18)
	d.mark(text, 0)
	d.tocOut[key] = len(d.pages)
	d.fill(cAccent, mL, d.y+1, 0.9, 5, 0)
	d.put(mL+4, d.y, t)
	if status != "" {
		w := d.pillW(status, 8)
		d.pill(mL+cw-w, d.y+0.5, status, cWarn, cWarnBg, 8)
	}
	d.hline(mL, d.y+h+3, cw, cLine, 0.25)
	d.y += h + 6
}

func (d *doc) section(s Section) {
	status := ""
	switch s.Status {
	case "limited":
		status = "受限"
	case "omitted":
		status = "未生成"
	}
	d.heading(s.ID, s.ID+"  "+s.Title, status)
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
	body := d.face(10.5, cText, false)
	cite := d.face(8, cAccent, true)
	rt := canvas.NewRichText(body)
	rt.WriteFace(body, c.Text)
	if n := d.num[c.Ref]; n > 0 {
		rt.WriteFace(cite, " ["+strconv.Itoa(n)+"]")
	}
	const gut = 15.0
	t := rt.ToText(cw-gut, 0, canvas.Left, canvas.Top, &canvas.TextOptions{LineStretch: 0.35})
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
	total := math.Max(5, h) + 3.4
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
	hf := d.face(8.5, cText, true)
	bf := d.face(8.5, cText, false)
	const pad = 2.6
	rows := make([][]string, len(t.Rows))
	for i, row := range t.Rows {
		rows[i] = make([]string, nc)
		for j := range rows[i] {
			s := "—"
			if j < len(row) {
				s = cell(row[j])
			}
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
	type rowLayout struct {
		boxes   []*canvas.Text
		offsets []float64
		height  float64
	}
	layout := func(cells []string, face *canvas.FontFace) rowLayout {
		row := rowLayout{boxes: make([]*canvas.Text, nc), offsets: make([]float64, nc)}
		for j, s := range cells {
			inset := math.Min(pad, widths[j]*0.15)
			avail := widths[j] - 2*inset
			row.boxes[j] = d.para(face, s, avail, canvas.Left)
			row.offsets[j] = inset
			if w := face.TextWidth(s); numeric[j] && w <= avail {
				row.offsets[j] += avail - w
			}
			row.height = math.Max(row.height, th(row.boxes[j]))
		}
		row.height += 4.4
		return row
	}
	draw := func(row rowLayout, bg color.RGBA) {
		if bg.A > 0 {
			d.fill(bg, mL, d.y, cw, row.height, 0)
		}
		x := mL
		for j, box := range row.boxes {
			d.put(x+row.offsets[j], d.y+2.2, box)
			x += widths[j]
		}
		d.y += row.height
		d.hline(mL, d.y, cw, cLine, 0.2)
	}
	header := layout(t.Columns, hf)
	caption := func(continued bool) (*canvas.Text, float64) {
		if t.Title == "" {
			return nil, 0
		}
		title := t.Title
		col := cText
		if continued {
			title += "（续）"
			col = cMuted
		}
		text := d.para(d.face(9.5, col, true), title, cw, canvas.Left)
		return text, th(text) + 2.8
	}
	start := func(continued bool, firstRowHeight float64) {
		text, height := caption(continued)
		d.ensure(height + header.height + firstRowHeight)
		if text != nil {
			d.put(mL, d.y, text)
			d.y += height
		}
		d.hline(mL, d.y, cw, cLine, 0.25)
		draw(header, cPanel)
	}
	if len(rows) == 0 {
		start(false, 0)
	}
	for i, cells := range rows {
		row := layout(cells, bf)
		if i == 0 {
			start(false, row.height)
		} else if d.y+row.height > pageH-mBot {
			start(true, row.height)
		}
		bg := canvas.Transparent
		if i%2 == 1 {
			bg = canvas.Hex("#FAFBFC")
		}
		draw(row, bg)
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
	if dec > 6 || math.Abs(v) >= 1e9 {
		return strconv.FormatFloat(v, 'g', 6, 64)
	}
	if math.Abs(v) < step*1e-6 {
		v = 0
	}
	return strconv.FormatFloat(v, 'f', dec, 64)
}

func fmtX(kind, raw string) string {
	if kind == "report_period" && len(raw) == 10 && strings.HasSuffix(raw, "-12-31") {
		return raw[:4]
	}
	return raw
}

func alpha(c color.RGBA, a float64) color.RGBA {
	return color.RGBA{uint8(float64(c.R) * a), uint8(float64(c.G) * a), uint8(float64(c.B) * a), uint8(255 * a)}
}

func chartValues(s Series) ([]string, [][]float64, float64, float64) {
	index := make(map[string]int)
	var dates []string
	for _, line := range s.Lines {
		for _, point := range line.Points {
			if point[0] == nil || *point[0] == "" {
				continue
			}
			if _, ok := index[*point[0]]; !ok {
				index[*point[0]] = len(dates)
				dates = append(dates, *point[0])
			}
		}
	}
	slices.Sort(dates)
	for i, date := range dates {
		index[date] = i
	}
	vals := make([][]float64, len(s.Lines))
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, line := range s.Lines {
		vals[i] = make([]float64, len(dates))
		for k := range vals[i] {
			vals[i][k] = math.NaN()
		}
		for _, p := range line.Points {
			if p[0] == nil || *p[0] == "" {
				continue
			}
			iDate := index[*p[0]]
			vals[i][iDate] = math.NaN()
			if p[1] == nil {
				continue
			}
			v, err := strconv.ParseFloat(*p[1], 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			vals[i][iDate] = v
		}
		for _, v := range vals[i] {
			if !math.IsNaN(v) {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
		}
	}
	return dates, vals, lo, hi
}

func chartDashes(series int) []float64 {
	switch series % 3 {
	case 1:
		return []float64{2, 1}
	case 2:
		return []float64{0.6, 0.9}
	default:
		return nil
	}
}

func (d *doc) chart(ch Chart, s Series) {
	dates, vals, lo, hi := chartValues(s)
	bars := s.Kind == "bar"
	empty := math.IsInf(lo, 0)
	const (
		inset   = 4.0
		plotH   = 46.0
		legendW = 5.5
		innerW  = cw - 2*inset
	)
	title := s.Title
	if title == "" {
		title = ch.Title
	}
	small := d.face(8, cMuted, false)
	titleW := innerW
	var unit *canvas.Text
	unitW := 0.0
	if s.Unit != "" {
		label := "单位：" + s.Unit
		unitW = math.Min(small.TextWidth(label)+1, innerW/3)
		unit = d.para(small, label, unitW, canvas.Right)
		titleW -= unitW + 5
	}
	titleText := d.para(d.face(10, cText, true), title, titleW, canvas.Left)
	headH := math.Max(4.5, th(titleText))
	if unit != nil {
		headH = math.Max(headH, th(unit))
	}

	type legendItem struct {
		text *canvas.Text
		x, y float64
	}
	var legend []legendItem
	x, y, lineH := 0.0, 5+headH+3, 0.0
	if !empty {
		for _, line := range s.Lines {
			w := math.Min(small.TextWidth(line.Name)+1, innerW-legendW)
			t := d.para(small, line.Name, w, canvas.Left)
			if x > 0 && x+legendW+w > innerW {
				x, y, lineH = 0, y+lineH+2, 0
			}
			legend = append(legend, legendItem{t, mL + inset + x, y})
			x += legendW + w + 4
			lineH = math.Max(lineH, th(t))
		}
	}
	plotTop := y + lineH + 5
	plotBottom := plotTop + plotH
	if empty {
		plotBottom = plotTop + 6
	}
	noteTop := plotBottom + 8
	total := noteTop + 1
	var note *canvas.Text
	if s.Note != nil && *s.Note != "" {
		note = d.para(small, *s.Note, innerW, canvas.Left)
		total += th(note) + 3
	}

	d.ensure(total + 3)
	d.hline(mL, d.y, cw, cLine, 0.25)
	d.put(mL+inset, d.y+5, titleText)
	if unit != nil {
		d.put(mL+cw-inset-unitW, d.y+5, unit)
	}
	if note != nil {
		d.put(mL+inset, d.y+noteTop, note)
	}
	if empty {
		d.put(mL+inset, d.y+plotTop, d.para(small, "暂无可绘制的数据", innerW, canvas.Left))
		d.y += total
		return
	}
	for i, item := range legend {
		col := palette[i%len(palette)]
		top := d.y + item.y
		if bars {
			d.fill(col, item.x, top+1, 3.3, 2, 0)
		} else {
			ctx := d.cur.ctx
			ctx.SetStrokeColor(col)
			ctx.SetStrokeWidth(0.55)
			ctx.SetDashes(0, chartDashes(i)...)
			ctx.DrawPath(item.x, ypos(top+2), canvas.Line(4, 0))
			ctx.SetStrokeColor(canvas.Transparent)
			ctx.SetDashes(0)
		}
		d.put(item.x+legendW, top, item.text)
	}

	if bars {
		lo, hi = math.Min(lo, 0), math.Max(hi, 0)
		if hi == lo {
			hi = 1
		}
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
	tf := d.face(7, cMuted, false)
	labelW := 0.0
	for _, tick := range ticks {
		labelW = math.Max(labelW, tf.TextWidth(fmtTick(tick, step)))
	}
	px0, px1 := mL+inset+labelW+3, mL+cw-inset
	py0, py1 := d.y+plotTop, d.y+plotBottom
	yAt := func(v float64) float64 { return py1 - (v-lo)/(hi-lo)*(py1-py0) }
	n := len(dates)
	gw := (px1 - px0) / float64(n)
	xAt := func(k int) float64 {
		if bars {
			return px0 + (float64(k)+0.5)*gw
		}
		if n == 1 {
			return (px0 + px1) / 2
		}
		return px0 + float64(k)/float64(n-1)*(px1-px0)
	}
	for _, t := range ticks {
		color, weight := cLine, 0.15
		if bars && t == 0 {
			color, weight = cMuted, 0.25
		}
		d.hline(px0, yAt(t), px1-px0, color, weight)
		d.cur.ctx.DrawText(px0-1.6, ypos(yAt(t))-0.8, canvas.NewTextLine(tf, fmtTick(t, step), canvas.Right))
	}
	xLabel := func(i int) (float64, float64, string) {
		label := fmtX(s.X, dates[i])
		w := tf.TextWidth(label)
		left := math.Max(mL+inset, math.Min(xAt(i)-w/2, px1-w))
		return left, w, label
	}
	lastLabel, _, _ := xLabel(n - 1)
	lastRight := mL
	interval := max(1, (n-1+4)/5)
	for i := 0; i < n; i++ {
		if i%interval != 0 && i != n-1 {
			continue
		}
		left, w, label := xLabel(i)
		if i > 0 && i < n-1 && (left < lastRight+2 || left+w > lastLabel-2) {
			continue
		}
		d.cur.ctx.DrawText(left, ypos(py1)-4.4, canvas.NewTextLine(tf, label, canvas.Left))
		lastRight = left + w
	}

	ctx := d.cur.ctx
	if bars {
		yz := yAt(0)
		bw := gw * 0.7 / float64(len(s.Lines))
		vf := d.face(7, cText, false)
		for i, line := range s.Lines {
			col := palette[i%len(palette)]
			raw := make(map[string]string, len(line.Points))
			for _, point := range line.Points {
				if point[0] != nil && point[1] != nil {
					raw[*point[0]] = *point[1]
				}
			}
			for k, v := range vals[i] {
				if math.IsNaN(v) {
					continue
				}
				x := px0 + float64(k)*gw + gw*0.15 + float64(i)*bw
				y := yAt(v)
				if h := math.Abs(yz - y); h > 0 {
					d.fill(col, x, math.Min(yz, y), bw*0.92, h, 0)
				}
				label := raw[dates[k]]
				if n*len(s.Lines) <= 14 && vf.TextWidth(label) < bw-1.2 {
					dy := 1.0
					if v < 0 {
						dy = -3.1
					}
					ctx.DrawText(x+bw*0.46, ypos(y)+dy, canvas.NewTextLine(vf, label, canvas.Center))
				}
			}
		}
	} else {
		for i := range s.Lines {
			d.chartLine(vals[i], xAt, yAt, py1, i, s.Kind == "area" && i == 0)
		}
	}
	ctx.SetStrokeColor(canvas.Transparent)
	d.y += total + 3
}

func (d *doc) chartLine(values []float64, xAt func(int) float64, yAt func(float64) float64, bottom float64, series int, shaded bool) {
	col := palette[series%len(palette)]
	line, area := &canvas.Path{}, &canvas.Path{}
	last := -1
	for i, v := range values {
		if math.IsNaN(v) {
			continue
		}
		x, y := xAt(i), ypos(yAt(v))
		if last < 0 || i > last+1 {
			line.MoveTo(x, y)
			if shaded {
				if last >= 0 {
					area.LineTo(xAt(last), ypos(bottom))
					area.Close()
				}
				area.MoveTo(x, ypos(bottom))
			}
		} else {
			line.LineTo(x, y)
		}
		if shaded {
			area.LineTo(x, y)
		}
		last = i
	}
	if last < 0 {
		return
	}
	ctx := d.cur.ctx
	if shaded {
		area.LineTo(xAt(last), ypos(bottom))
		area.Close()
		ctx.SetFillColor(alpha(col, 0.13))
		ctx.SetStrokeColor(canvas.Transparent)
		ctx.DrawPath(0, 0, area)
	}
	ctx.SetFillColor(canvas.Transparent)
	ctx.SetStrokeColor(col)
	ctx.SetStrokeWidth(0.55)
	ctx.SetDashes(0, chartDashes(series)...)
	ctx.DrawPath(0, 0, line)
	ctx.SetDashes(0)
	ctx.SetFillColor(col)
	ctx.SetStrokeColor(canvas.Transparent)
	for i, v := range values {
		isolated := (i == 0 || math.IsNaN(values[i-1])) &&
			(i == len(values)-1 || math.IsNaN(values[i+1]))
		if !math.IsNaN(v) && (len(values) <= 10 || i == last || isolated) {
			ctx.DrawPath(xAt(i), ypos(yAt(v)), canvas.Circle(0.75))
		}
	}
}

// ---- closing pages ----

func (d *doc) limits() {
	d.heading("limits", "数据与限制", "")
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
	d.put(mL, d.y, d.para(d.face(10.5, cText, true), "阅读指引", 60, canvas.Left))
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
			ctx.DrawText(mL, ypos(12.5), canvas.NewTextLine(small, "Vera · "+trunc(d.r.Title, 24), canvas.Left))
			x := pageW - mR
			if d.r.Synthetic {
				w := d.pillW("模拟数据", 6.5)
				d.pill(x-w, 8.6, "模拟数据", cBad, cBadBg, 6.5)
				x -= w + 2
			}
			ctx.DrawText(x, ypos(12.5), canvas.NewTextLine(small, "数据截止 "+trunc(d.r.DataAsOf, 32), canvas.Right))
			d.hline(mL, 16, cw, cLine, 0.2)
		}
		d.hline(mL, pageH-15, cw, cLine, 0.2)
		ctx.DrawText(mL, 9.5, canvas.NewTextLine(small, "AI 生成内容，仅供研究参考，不构成投资建议。", canvas.Left))
		ctx.DrawText(pageW-mR, 9.5, canvas.NewTextLine(small, fmt.Sprintf("第 %d / %d 页", i+1, n), canvas.Right))
	}
}
