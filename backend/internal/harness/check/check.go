// Package check implements the deterministic claim rules R-01..R-09 (HARNESS §7.1).
// The rule tables are versioned with RulesVersion and frozen into reports.
package check

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

const RulesVersion = "1.0.0"

// Claim mirrors one item of section-output.schema.json.
type Claim struct {
	Kind     string   `json:"kind"`
	Text     string   `json:"text"`
	Refs     []string `json:"refs"`
	Numbers  []Number `json:"numbers"`
	Scenario Scenario `json:"scenario"`
}

type Number struct {
	Text string `json:"text"`
	Fact string `json:"fact"`
}

type Scenario struct {
	Trigger      string   `json:"trigger"`
	Watch        []string `json:"watch"`
	Invalidation string   `json:"invalidation"`
}

// Context is everything a rule may consult. Allowed is the set of fact IDs given to the section.
type Context struct {
	Sheet    *facts.Sheet
	Allowed  map[string]bool
	Kinds    []string
	AsOf     time.Time
	MaxChars int
}

// Failure names the first violated rule.
type Failure struct {
	Rule   string
	Reason string
}

func (f *Failure) Error() string { return f.Rule + ": " + f.Reason }

func fail(rule, format string, a ...any) *Failure { return &Failure{rule, fmt.Sprintf(format, a...)} }

// Check runs all claim-level rules. A nil result means the claim may be kept.
func Check(c Claim, cx Context) *Failure {
	if cx.MaxChars > 0 && utf8.RuneCountInString(c.Text) > cx.MaxChars {
		return fail("R-09", "结论超过 %d 字", cx.MaxChars)
	}
	if strings.TrimSpace(c.Text) == "" {
		return fail("R-05", "结论为空")
	}
	cited, err := resolve(c.Refs, cx)
	if err != nil {
		return err
	}
	if f := checkKind(c, cited, cx); f != nil {
		return f
	}
	full := c.Text
	scopeFacts := cited
	if c.Kind == "scenario" {
		watch, err := resolve(c.Scenario.Watch, cx)
		if err != nil {
			return err
		}
		scopeFacts = append(slices.Clone(cited), watch...)
		full = c.Text + "；" + c.Scenario.Trigger + "；" + c.Scenario.Invalidation
	}
	if f := Forbidden(full); f != nil {
		return f
	}
	if f := checkTime(full, scopeFacts, cx.AsOf); f != nil {
		return f
	}
	if f := checkNumbers(c, full, scopeFacts); f != nil {
		return f
	}
	for _, f := range cited {
		if f.Kind == facts.KindVendorDerived && !strings.Contains(c.Text, "口径") && !strings.Contains(c.Text, "厂商") {
			return fail("R-07", "引用厂商口径数据 %s 但未说明口径", f.ID)
		}
	}
	return nil
}

func resolve(ids []string, cx Context) ([]facts.Fact, *Failure) {
	var out []facts.Fact
	for _, id := range ids {
		f, ok := cx.Sheet.Get(id)
		if !ok {
			return nil, fail("R-01", "引用 %s 不存在", id)
		}
		if cx.Allowed != nil && !cx.Allowed[id] {
			return nil, fail("R-01", "引用 %s 不属于本章节可用事实", id)
		}
		if slices.Contains(f.Flags, "suspicious") {
			return nil, fail("R-01", "引用 %s 被标记为可疑内容", id)
		}
		out = append(out, *f)
	}
	return out, nil
}

func checkKind(c Claim, cited []facts.Fact, cx Context) *Failure {
	if !slices.Contains(cx.Kinds, c.Kind) {
		return fail("R-05", "本章节不允许 %s 类结论", c.Kind)
	}
	if len(cited) == 0 {
		return fail("R-05", "%s 结论必须引用事实", c.Kind)
	}
	sc := c.Scenario
	isScenario := c.Kind == "scenario"
	if isScenario {
		if strings.TrimSpace(sc.Trigger) == "" || strings.TrimSpace(sc.Invalidation) == "" || len(sc.Watch) == 0 {
			return fail("R-05", "情景结论必须包含触发条件、观察指标与失效条件")
		}
		if !strings.Contains(c.Text, "若") && !strings.Contains(c.Text, "如果") {
			return fail("R-05", "情景结论必须以条件形式表述")
		}
	} else if sc.Trigger != "" || sc.Invalidation != "" || len(sc.Watch) > 0 {
		return fail("R-05", "非情景结论不得填写情景字段")
	}
	has := func(kinds ...string) bool {
		return slices.ContainsFunc(cited, func(f facts.Fact) bool { return slices.Contains(kinds, f.Kind) })
	}
	switch c.Kind {
	case "computed":
		if !has(facts.KindComputed) {
			return fail("R-05", "computed 结论必须引用计算事实")
		}
	case "attributed":
		if !has(facts.KindText) {
			return fail("R-05", "归属陈述必须引用原文段落")
		}
	case "fact":
		if !has(facts.KindReported, facts.KindMarket, facts.KindVendorDerived, facts.KindText, facts.KindComputed) {
			return fail("R-05", "fact 结论必须引用事实")
		}
	}
	for _, f := range cited {
		if slices.Contains(f.Flags, "not_applicable") && numberPat.MatchString(stripNonValues(c.Text)) && len(cited) == 1 {
			return fail("R-02", "引用的计算 %s 不适用，不得给出数值", f.ID)
		}
	}
	return nil
}

// ---- R-06 forbidden expressions ----

var exempt = []string{"融资买入额", "融资买入", "净买入额", "买入额", "卖出额", "买入金额", "卖出金额", "买入席位", "卖出席位",
	"买方", "卖方", "增持计划", "减持计划"}

var forbidden = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`评级|目标价|目标价格`), "评级或目标价"},
	{regexp.MustCompile(`买入|卖出|建议持有|推荐|加仓|减仓|建仓|清仓|仓位|止损|止盈|抄底|逃顶`), "操作建议"},
	{regexp.MustCompile(`低估|高估|估值合理|便宜|昂贵|上涨空间|下跌空间|安全边际`), "估值判断"},
	{regexp.MustCompile(`概率|可能性为|[0-9]+(\.[0-9]+)?%的(可能|把握)|胜率`), "概率表述"},
	{regexp.MustCompile(`必将|必然|一定会|肯定会|毫无疑问|确定性|无风险|稳赚|翻倍`), "绝对化表述"},
	{regexp.MustCompile(`看好|看空|看多|值得(买|关注)|将(大幅)?(上涨|下跌)|有望(上涨|下跌|突破)`), "方向性判断"},
	{regexp.MustCompile(`主力|游资|聪明钱|抱团|出逃|国家队|庄家`), "未定义的资金标签"},
	{regexp.MustCompile(`北向资金.{0,6}(净|流入|流出|买|卖)|外资.{0,4}(净流入|净流出)`), "无官方来源的北向资金流向"},
	{regexp.MustCompile(`未来\s*[0-9一二三四五六七八九十]+\s*(个)?(交易日|天|周|个月|月|年)内?.{0,6}(将|会|可能)`), "时间预测"},
}

var quoted = regexp.MustCompile(`“[^”]{1,20}”|「[^」]{1,20}」`)

// Forbidden returns the first forbidden expression found (R-06). Vendor-defined names in quotes are allowed
// only when the sentence names the vendor's definition.
func Forbidden(text string) *Failure {
	t := text
	if strings.Contains(t, "厂商") || strings.Contains(t, "口径") {
		t = quoted.ReplaceAllString(t, "")
	}
	for _, e := range exempt {
		t = strings.ReplaceAll(t, e, "")
	}
	for _, f := range forbidden {
		if m := f.re.FindString(t); m != "" {
			return fail("R-06", "命中禁用表达（%s）：%s", f.name, m)
		}
	}
	return nil
}

// Suspicious detects instruction-like content in untrusted text facts (prompt injection, ratings).
var suspiciousPat = regexp.MustCompile(`(?i)忽略.{0,6}(规则|指令|要求|以上)|以上所有规则|你现在是|system\s*prompt|系统提示|指令[:：]|ignore (all|previous)|评级|目标价`)

func Suspicious(text string) bool { return suspiciousPat.MatchString(text) }

// ---- R-04 time ----

var (
	fullDate  = regexp.MustCompile(`(\d{4})[-年/.](\d{1,2})[-月/.](\d{1,2})日?`)
	yearMonth = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月`)
	yearOnly  = regexp.MustCompile(`(\d{4})\s*(年|年度|财年|FY|Q[1-4]|H[12])`)
)

func checkTime(text string, cited []facts.Fact, asOf time.Time) *Failure {
	periods := ""
	for _, f := range cited {
		periods += " " + f.Period + " " + f.Label + " " + f.Text
	}
	for _, m := range fullDate.FindAllStringSubmatch(text, -1) {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, asOf.Location())
		if t.After(asOf) {
			return fail("R-04", "日期 %s 晚于数据截止时点", m[0])
		}
		iso := t.Format("2006-01-02")
		if !strings.Contains(periods, iso) && !strings.Contains(periods, m[0]) {
			return fail("R-04", "日期 %s 不属于被引用事实的期间", m[0])
		}
	}
	for _, m := range append(yearMonth.FindAllStringSubmatch(text, -1), yearOnly.FindAllStringSubmatch(text, -1)...) {
		y, _ := strconv.Atoi(m[1])
		if y > asOf.Year() {
			return fail("R-04", "年份 %d 晚于数据截止时点", y)
		}
		if !strings.Contains(periods, m[1]) {
			return fail("R-04", "年份 %s 不属于被引用事实的期间", m[1])
		}
	}
	return nil
}

// ---- R-02 / R-03 / R-08 numbers ----

var (
	numberPat = regexp.MustCompile(`[-−]?\d+(?:\.\d+)?`)
	// Tokens that are identifiers or time expressions, not values. They are removed before value matching;
	// time expressions are validated by R-04 and window lengths must appear in cited periods/labels.
	codePat   = regexp.MustCompile(`[A-Z]{2}\d{3,4}(\.[A-Z]+)?|\d{6}\.(SH|SZ|BJ)|IDX-[a-z-]+|\[\d+\]|\bF\d+\b|\bS[0-9X]{1,2}(\.\d+)?\b`)
	timePat   = regexp.MustCompile(`\d{4}[-年/.]\d{1,2}[-月/.]\d{1,2}日?|\d{4}\s*年\s*\d{1,2}\s*月|\d{4}\s*(年度|年|财年|FY|Q[1-4]|H[12])|\d{1,2}\s*月\s*\d{1,2}\s*日|(第)?[1-4]\s*季度|Q[1-4]|H[12]`)
	windowPat = regexp.MustCompile(`(近|过去|最近|前)\s*(\d+)\s*(个)?\s*(交易日|日|天|个月|月|年|季度|个季度|家)`)
)

var units = []string{"个百分点", "百分点", "亿元", "万元", "亿股", "万股", "%", "倍", "元", "股", "点", "家", "次", "户", "天", "个交易日", "只"}

var up = []string{"增长", "增加", "上升", "上涨", "提升", "提高", "回升", "流入", "净买入", "扩大", "走高", "改善", "涨幅", "增幅", "净增", "多增", "溢价"}
var down = []string{"下降", "减少", "下滑", "下跌", "降低", "回落", "流出", "净卖出", "收窄", "走低", "恶化", "萎缩", "跌幅", "降幅", "减幅", "净减", "回撤", "折价", "亏损"}

func stripNonValues(text string) string {
	t := codePat.ReplaceAllString(text, " ")
	t = timePat.ReplaceAllString(t, " ")
	return t
}

type token struct {
	val      decimal.Decimal
	raw      string
	unit     string
	before   string // clause text before the number
	negative bool
}

func tokens(text string) []token {
	t := codePat.ReplaceAllStringFunc(text, func(s string) string { return strings.Repeat(" ", len(s)) })
	t = timePat.ReplaceAllStringFunc(t, func(s string) string { return strings.Repeat(" ", len(s)) })
	t = windowPat.ReplaceAllStringFunc(t, func(s string) string { return strings.Repeat(" ", len(s)) })
	var out []token
	for _, loc := range numberPat.FindAllStringIndex(t, -1) {
		raw := strings.ReplaceAll(t[loc[0]:loc[1]], "−", "-")
		v, err := decimal.NewFromString(raw)
		if err != nil {
			continue
		}
		rest := strings.TrimLeft(t[loc[1]:], " ")
		u := ""
		for _, cand := range units {
			if strings.HasPrefix(rest, cand) {
				u = cand
				break
			}
		}
		if u == "百分点" {
			u = "个百分点"
		}
		before := t[:loc[0]]
		if i := strings.LastIndexAny(before, "，,；;。：:（("); i >= 0 {
			before = before[i:]
		}
		neg := v.IsNegative() || strings.HasSuffix(strings.TrimRight(before, " "), "负")
		out = append(out, token{val: v.Abs(), raw: raw, unit: u, before: before, negative: neg})
	}
	return out
}

func windows(text string) []string {
	var out []string
	for _, m := range windowPat.FindAllStringSubmatch(text, -1) {
		out = append(out, m[2])
	}
	return out
}

func direction(before string) int {
	if strings.Contains(before, "涨跌幅") || strings.Contains(before, "变化") || strings.Contains(before, "变动") {
		// neutral words: sign must be explicit
		before = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(before, "涨跌幅", ""), "变化", ""), "变动", "")
	}
	best, dir := -1, 0
	for _, w := range up {
		if i := strings.LastIndex(before, w); i > best {
			best, dir = i, 1
		}
	}
	for _, w := range down {
		if i := strings.LastIndex(before, w); i > best {
			best, dir = i, -1
		}
	}
	return dir
}

// candidates returns the numeric forms a cited fact allows in prose.
func candidates(f facts.Fact) []decimal.Decimal {
	var out []decimal.Decimal
	if v, err := decimal.NewFromString(f.Display); err == nil {
		out = append(out, v.Abs())
	}
	if f.Kind == facts.KindText {
		for _, tk := range tokens(f.Text) {
			out = append(out, tk.val)
		}
	}
	return out
}

func checkNumbers(c Claim, text string, cited []facts.Fact) *Failure {
	context := ""
	for _, f := range cited {
		context += " " + f.Period + " " + f.Label
	}
	for _, w := range windows(text) {
		if !slices.Contains(numberPat.FindAllString(context, -1), w) {
			return fail("R-04", "时间窗“%s”不属于被引用事实的期间", w)
		}
	}
	declared := map[string]string{}
	for _, n := range c.Numbers {
		if !slices.Contains(c.Refs, n.Fact) && !slices.Contains(c.Scenario.Watch, n.Fact) {
			return fail("R-02", "声明的数字 %s 指向未引用的事实 %s", n.Text, n.Fact)
		}
		declared[n.Text] = n.Fact
	}
	for _, tk := range tokens(text) {
		var match *facts.Fact
		for i := range cited {
			for _, cand := range candidates(cited[i]) {
				if cand.Equal(tk.val) {
					match = &cited[i]
					break
				}
			}
			if match != nil {
				break
			}
		}
		if match == nil {
			// label numbers such as “前十大” are allowed when they appear in a cited label
			if slices.Contains(numberPat.FindAllString(context, -1), tk.raw) {
				continue
			}
			return fail("R-02", "数字 %s 无法匹配到被引用的事实", tk.raw)
		}
		if f := checkUnit(tk, *match); f != nil {
			return f
		}
		if f := checkDirection(tk, *match); f != nil {
			return f
		}
		if f := checkScope(tk, *match); f != nil {
			return f
		}
	}
	return nil
}

func checkUnit(tk token, f facts.Fact) *Failure {
	if f.Kind == facts.KindText || f.Unit == "" {
		return nil
	}
	want := f.Unit
	if tk.unit == "" {
		if slices.Contains([]string{"%", "个百分点", "亿元", "万元", "倍", "亿股", "万股"}, want) {
			return fail("R-03", "数字 %s 缺少单位（应为%s）", tk.raw, want)
		}
		return nil
	}
	if tk.unit != want && !(want == "个交易日" && tk.unit == "天") {
		return fail("R-03", "数字 %s 的单位“%s”与事实单位“%s”不一致", tk.raw, tk.unit, want)
	}
	return nil
}

func checkDirection(tk token, f facts.Fact) *Failure {
	if f.Value == nil || f.Kind == facts.KindText {
		return nil
	}
	neg := f.Value.IsNegative()
	if !f.Change {
		if tk.negative != neg {
			return fail("R-08", "数字 %s 的符号与事实不一致", tk.raw)
		}
		return nil
	}
	switch direction(tk.before) {
	case 1:
		if neg || tk.negative {
			return fail("R-08", "方向词与事实 %s 的符号不一致", f.ID)
		}
	case -1:
		if !neg && !f.Value.IsZero() || tk.negative {
			return fail("R-08", "方向词与事实 %s 的符号不一致", f.ID)
		}
	default:
		if tk.negative != neg {
			return fail("R-08", "未写方向词时数字 %s 必须带正确符号", tk.raw)
		}
	}
	return nil
}

func checkScope(tk token, f facts.Fact) *Failure {
	b := tk.before
	parent := strings.Contains(f.Label, "归母") || strings.Contains(f.Label, "归属于母公司")
	if strings.Contains(f.Label, "净利") {
		if (strings.Contains(b, "归母") || strings.Contains(b, "归属于母公司")) && !parent {
			return fail("R-03", "“归母”表述引用了非归母口径的事实 %s", f.ID)
		}
		if strings.Contains(b, "净利") && !strings.Contains(b, "归母") && !strings.Contains(b, "归属于母公司") && parent {
			return fail("R-03", "归母口径事实 %s 必须写明“归母”", f.ID)
		}
	}
	return nil
}
