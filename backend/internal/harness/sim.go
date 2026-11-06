package harness

// Deterministic simulators for the sim model adapter. They turn facts into template sentences so the whole
// pipeline (rules, verification, assembly, delivery) runs without a model key. Output is synthetic: reports
// produced with the sim adapter are labelled synthetic and the adapter is refused in production.

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

var cst = time.FixedZone("CST", 8*3600)

func noScenario() check.Scenario { return check.Scenario{Watch: []string{}} }

func unitSuffix(f facts.Fact) string {
	if f.Unit == "" {
		return ""
	}
	if f.Unit == "%" {
		return "%"
	}
	return " " + f.Unit
}

func absDisplay(f facts.Fact) string { return strings.TrimPrefix(f.Display, "-") }

// sentence renders one fact; ok=false when no safe template applies.
func sentence(f facts.Fact) (check.Claim, bool) {
	c := check.Claim{Refs: []string{f.ID}, Scenario: noScenario(), Numbers: []check.Number{}}
	c.Kind = KFact
	if f.Kind == facts.KindComputed {
		c.Kind = KComp
	}
	prefix := f.Subject + "在" + f.Period
	vendor := ""
	if f.Kind == facts.KindVendorDerived {
		vendor = "按厂商口径，"
	}
	switch {
	case f.Kind == facts.KindText:
		return check.Claim{}, false
	case slices.Contains(f.Flags, "not_applicable"):
		c.Text = fmt.Sprintf("%s%s的%s不适用：%s。", vendor, prefix, f.Label, f.Text)
	case f.Value == nil && f.Display == f.Period:
		c.Text = fmt.Sprintf("%s%s的%s为 %s。", vendor, f.Subject, f.Label, f.Display)
	case f.Value == nil:
		c.Text = fmt.Sprintf("%s%s的%s：%s。", vendor, prefix, f.Label, f.Display)
	case f.Change:
		labelDir := check.Direction(f.Label)
		neg := f.Negative()
		switch {
		case labelDir != 0 && (labelDir < 0) != neg && !f.Value.IsZero():
			return check.Claim{}, false // label wording conflicts with the sign; skip rather than mislead
		case labelDir != 0:
			c.Text = fmt.Sprintf("%s%s的%s为 %s%s。", vendor, prefix, f.Label, absDisplay(f), unitSuffix(f))
		case strings.Contains(f.Label, "收益") || strings.Contains(f.Label, "涨跌幅") || strings.Contains(f.Label, "相对"):
			c.Text = fmt.Sprintf("%s%s的%s为 %s%s。", vendor, prefix, f.Label, f.Display, unitSuffix(f))
		default:
			word := "增加"
			if f.Unit == "%" || f.Unit == "个百分点" {
				word = "上升"
			}
			if neg {
				word = map[string]string{"增加": "减少", "上升": "下降"}[word]
			}
			c.Text = fmt.Sprintf("%s%s的%s%s %s%s。", vendor, prefix, strings.TrimSuffix(f.Label, "变化"), word, absDisplay(f), unitSuffix(f))
		}
	default:
		c.Text = fmt.Sprintf("%s%s的%s为 %s%s。", vendor, prefix, f.Label, f.Display, unitSuffix(f))
	}
	if c.Kind == KComp && f.Kind != facts.KindComputed {
		c.Kind = KFact
	}
	return c, true
}

func excerpt(text string) string {
	s := text
	if i := strings.IndexAny(s, "。；"); i > 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > 70 {
		r := []rune(s)[:70]
		for len(r) > 0 && (r[len(r)-1] >= '0' && r[len(r)-1] <= '9' || r[len(r)-1] == '.') {
			r = r[:len(r)-1]
		}
		s = string(r)
	}
	return strings.TrimSpace(s)
}

func attributed(f facts.Fact) check.Claim {
	text := fmt.Sprintf("公司在%s中表示：%s。", f.Label, excerpt(f.Text))
	if strings.HasPrefix(f.Label, "新闻：") {
		text = fmt.Sprintf("据授权媒体于 %s 报道：%s。", f.Period, strings.TrimPrefix(f.Label, "新闻："))
	} else if strings.HasPrefix(f.Label, "公告：") {
		text = fmt.Sprintf("公司于 %s 发布公告：%s。", f.Period, strings.TrimPrefix(f.Label, "公告："))
	}
	return check.Claim{Kind: KAttr, Text: text, Refs: []string{f.ID}, Numbers: []check.Number{}, Scenario: noScenario()}
}

// simulateSection writes up to MaxClaims claims from the section's facts.
func simulateSection(sheet *facts.Sheet, s Section, subject dataservice.EntityRow) map[string]any {
	fs := allowed(sheet, s)
	claims := []check.Claim{}
	wantInf := slices.Contains(s.Kinds, KInf)
	wantScen := slices.Contains(s.Kinds, KScen)
	onlyJudgement := !slices.Contains(s.Kinds, KFact) && !slices.Contains(s.Kinds, KComp)
	if !onlyJudgement {
		for _, f := range fs {
			if len(claims) >= s.MaxClaims {
				break
			}
			if f.Kind == facts.KindText {
				if slices.Contains(s.Kinds, KAttr) {
					claims = append(claims, attributed(f))
				}
				continue
			}
			if c, ok := sentence(f); ok && utf8.RuneCountInString(c.Text) <= s.MaxChars {
				claims = append(claims, c)
			}
		}
	} else if slices.Contains(s.Kinds, KAttr) {
		for _, f := range fs {
			if f.Kind == facts.KindText && len(claims) < s.MaxClaims {
				claims = append(claims, attributed(f))
			}
		}
	}
	// judgement claims on applicable change facts
	var movers []facts.Fact
	for _, f := range fs {
		if f.Change && f.Value != nil && !f.Value.IsZero() && f.Kind != facts.KindVendorDerived {
			movers = append(movers, f)
		}
	}
	if wantInf && len(movers) > 0 && len(claims) < s.MaxClaims {
		f := movers[0]
		dir := "上升"
		if f.Negative() {
			dir = "下降"
		}
		claims = append(claims, check.Claim{Kind: KInf, Refs: []string{f.ID}, Numbers: []check.Number{}, Scenario: noScenario(),
			Text: fmt.Sprintf("%s的%s呈%s方向，后续披露中需继续跟踪该指标的变化。", f.Subject, f.Label, dir)})
	}
	if wantScen {
		for i, f := range movers {
			if i >= 2 || len(claims) >= s.MaxClaims {
				break
			}
			cont, rev := "继续上升", "转为下降"
			if f.Negative() {
				cont, rev = "继续下降", "转为上升"
			}
			claims = append(claims, check.Claim{Kind: KScen, Refs: []string{f.ID}, Numbers: []check.Number{},
				Text:     fmt.Sprintf("若%s的%s%s，则相关经营或交易指标可能延续当前方向。", f.Subject, f.Label, cont),
				Scenario: check.Scenario{Trigger: f.Label + cont, Watch: []string{f.ID}, Invalidation: f.Label + rev}})
		}
	}
	return map[string]any{"section_id": s.ID, "claims": claims, "omitted": []any{}}
}

func simulateVerify(s Section, claims []check.Claim) map[string]any {
	var vs []map[string]string
	for i := range claims {
		vs = append(vs, map[string]string{"claim_ref": provisionalRef(s.ID, i), "verdict": "supported", "reason": "", "rewrite": "", "suggested_kind": "none"})
	}
	if vs == nil {
		vs = []map[string]string{}
	}
	return map[string]any{"verdicts": vs}
}

type finalClaim struct {
	Ref     string      `json:"ref"`
	Section string      `json:"section"`
	Claim   check.Claim `json:"claim"`
}

// fallbackSummary takes one claim per required section (HARNESS §8), preferring computed/inference/scenario claims
// over plain catalogue facts and skipping repeated texts, then fills to 3.
func fallbackSummary(claims []finalClaim, required []string) []summaryPoint {
	rank := map[string]int{KComp: 0, KInf: 1, KScen: 2, KAttr: 3, KFact: 4}
	var out []summaryPoint
	used, texts := map[string]bool{}, map[string]bool{}
	take := func(c finalClaim) {
		out = append(out, summaryPoint{Text: c.Claim.Text, ClaimRefs: []string{c.Ref}})
		used[c.Ref], texts[c.Claim.Text] = true, true
	}
	for _, sid := range required {
		var best *finalClaim
		for i, c := range claims {
			if c.Section != sid || texts[c.Claim.Text] {
				continue
			}
			if best == nil || rank[c.Claim.Kind] < rank[best.Claim.Kind] {
				best = &claims[i]
			}
		}
		if best != nil && len(out) < 5 {
			take(*best)
		}
	}
	for _, c := range claims {
		if len(out) >= 3 {
			break
		}
		if !used[c.Ref] && !texts[c.Claim.Text] {
			take(c)
		}
	}
	return out
}

func simulateSummary(claims []finalClaim, required []string) map[string]any {
	return map[string]any{"points": fallbackSummary(claims, required)}
}
