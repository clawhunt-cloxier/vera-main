package facts

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Fact kinds (HARNESS §5).
const (
	KindReported      = "reported"
	KindMarket        = "market"
	KindVendorDerived = "vendor_derived"
	KindComputed      = "computed"
	KindText          = "text"
)

// Fact is one entry of the FactSheet. Display is the only numeric form a model sees.
type Fact struct {
	ID       string           `json:"id"`
	Group    string           `json:"group"`
	Subject  string           `json:"subject"`
	Label    string           `json:"label"`
	Value    *decimal.Decimal `json:"-"`
	Display  string           `json:"value"`
	Unit     string           `json:"unit"`
	Period   string           `json:"period"`
	Kind     string           `json:"kind"`
	Basis    string           `json:"basis,omitempty"`
	Flags    []string         `json:"flags,omitempty"`
	Change   bool             `json:"-"` // value is a change/flow whose sign carries direction
	Evidence []uuid.UUID      `json:"-"`
	CalcID   *uuid.UUID       `json:"-"`
	Text     string           `json:"text,omitempty"`
}

// Negative reports whether the fact is a signed quantity below zero.
func (f Fact) Negative() bool { return f.Value != nil && f.Value.IsNegative() }

// Table is template-rendered from facts (never written by a model).
type Table struct {
	Title    string      `json:"title"`
	Columns  []string    `json:"columns"`
	Rows     [][]*string `json:"rows"`
	Evidence []uuid.UUID `json:"evidence_refs"`
}

// Series is chart data served via /series (front end only, never sent to models).
type Series struct {
	ID       string       `json:"series_id"`
	Title    string       `json:"title"`
	Kind     string       `json:"kind"`
	X        string       `json:"x"`
	Unit     string       `json:"unit"`
	Lines    []SeriesLine `json:"series"`
	Evidence []uuid.UUID  `json:"evidence_refs"`
	Note     *string      `json:"note"`
	Section  string       `json:"-"`
}

type SeriesLine struct {
	Name   string       `json:"name"`
	Points [][2]*string `json:"points"`
}

// Sheet accumulates facts, calculations, tables, charts and limitations for one task.
type Sheet struct {
	Task        uuid.UUID          `json:"task"`
	Epoch       int64              `json:"epoch"`
	Facts       []Fact             `json:"facts"`
	Calcs       []Calc             `json:"calcs"`
	Tables      map[string][]Table `json:"tables"`
	Charts      []Series           `json:"charts"`
	Limitations []string           `json:"limitations"`
	Unavailable map[string]string  `json:"unavailable"` // section id → reason found while computing
	Suspicious  int                `json:"suspicious"`
	Sources     map[string]string  `json:"sources"` // dataset id → latest available_at
	// Frozen rows observed by builders. Real reports identify each dataset's
	// actual contributor, even when other vendors are enabled in the catalog.
	SourceProviders map[string]map[string]string `json:"source_providers,omitempty"` // dataset → provider → latest available_at
}

func NewSheet(task uuid.UUID, epoch int64) *Sheet {
	return &Sheet{Task: task, Epoch: epoch, Tables: map[string][]Table{}, Unavailable: map[string]string{}, Sources: map[string]string{}}
}

func (s *Sheet) add(f Fact) *Fact {
	f.ID = fmt.Sprintf("F%d", len(s.Facts)+1)
	s.Facts = append(s.Facts, f)
	return &s.Facts[len(s.Facts)-1]
}

// Value adds a reported/market/vendor fact taken directly from evidence.
func (s *Sheet) Value(group, subject, label, period, kind, basis, unit string, v decimal.Decimal, ev ...uuid.UUID) *Fact {
	d, u := Display(v, unit)
	return s.add(Fact{Group: group, Subject: subject, Label: label, Value: &v, Display: d, Unit: u, Period: period, Kind: kind,
		Basis: basis, Evidence: ev})
}

// Computed records the calculation and, when applicable, adds a computed fact. Not-applicable results become
// a fact flagged not_applicable with the reason as text, so models can say "不适用" without inventing numbers.
func (s *Sheet) Computed(group, subject, label, period string, c Calc, key string) *Fact {
	c.ID = StableID(s.Task, s.Epoch, key)
	s.Calcs = append(s.Calcs, c)
	var ev []uuid.UUID
	for _, in := range c.Inputs {
		if !slices.Contains(ev, in.EvidenceID) {
			ev = append(ev, in.EvidenceID)
		}
	}
	id := c.ID
	f := Fact{Group: group, Subject: subject, Label: label, Period: period, Kind: KindComputed, Evidence: ev, CalcID: &id,
		Change: c.Unit == "pp" || strings.Contains(c.FormulaID, "change") || strings.Contains(c.FormulaID, "yoy") ||
			strings.Contains(c.FormulaID, "return") || strings.Contains(c.FormulaID, "gap") || strings.Contains(c.FormulaID, "premium") ||
			strings.Contains(c.FormulaID, "drawdown") || strings.Contains(c.FormulaID, "consecutive")}
	if !c.OK() {
		f.Display, f.Unit, f.Flags, f.Text = "不适用", "", []string{"not_applicable"}, c.Reason
		return s.add(f)
	}
	v := *c.Result
	f.Value = &v
	f.Display, f.Unit = Display(v, c.Unit)
	return s.add(f)
}

// Finalize orders facts by group (stable within a group) and renumbers IDs F1…Fn (HARNESS §5), so IDs do not
// depend on the order builders ran in. Call once after all facts are added and before any ID is used.
func (s *Sheet) Finalize() {
	slices.SortStableFunc(s.Facts, func(a, b Fact) int { return strings.Compare(a.Group, b.Group) })
	for i := range s.Facts {
		s.Facts[i].ID = fmt.Sprintf("F%d", i+1)
	}
}

// Vendor marks a computed fact as derived from vendor-defined data, which requires the basis in prose (R-07).
func (f *Fact) Vendor(basis string) *Fact { f.Kind, f.Basis = KindVendorDerived, basis; return f }

// Flag adds a flag (restated, scheduled, small_base, stale, suspicious, …).
func (f *Fact) Flag(fl string) *Fact {
	if !slices.Contains(f.Flags, fl) {
		f.Flags = append(f.Flags, fl)
	}
	return f
}

// TextFact adds a paragraph. Text facts carry no numeric value; numbers inside may only be quoted from Text.
func (s *Sheet) TextFact(group, subject, label, period, kind, basis, text string, ev uuid.UUID) *Fact {
	return s.add(Fact{Group: group, Subject: subject, Label: label, Period: period, Kind: kind, Basis: basis, Display: "",
		Text: text, Evidence: []uuid.UUID{ev}})
}

// Note adds a non-numeric categorical fact (e.g. industry name).
func (s *Sheet) Note(group, subject, label, period, kind, value string, ev ...uuid.UUID) *Fact {
	return s.add(Fact{Group: group, Subject: subject, Label: label, Period: period, Kind: kind, Display: value, Evidence: ev})
}

func (s *Sheet) Get(id string) (*Fact, bool) {
	for i := range s.Facts {
		if s.Facts[i].ID == id {
			return &s.Facts[i], true
		}
	}
	return nil, false
}

func (s *Sheet) Calc(id uuid.UUID) (Calc, bool) {
	for _, c := range s.Calcs {
		if c.ID == id {
			return c, true
		}
	}
	return Calc{}, false
}

// Select returns facts whose group matches one of the prefixes.
func (s *Sheet) Select(prefixes []string) []Fact {
	var out []Fact
	for _, f := range s.Facts {
		for _, p := range prefixes {
			if p == "*" || strings.HasPrefix(f.Group, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func (s *Sheet) Limit(msg string) {
	if !slices.Contains(s.Limitations, msg) {
		s.Limitations = append(s.Limitations, msg)
	}
}

var (
	yi    = decimal.NewFromInt(100_000_000)
	wan   = decimal.NewFromInt(10_000)
	hundr = decimal.NewFromInt(100)
)

// Display applies the fixed display rules: amounts in 亿元/万元 with 2 decimals, ratios in % with 2 decimals,
// percentage-point changes, multiples in 倍, shares in 万股/亿股. Rounding happens only here.
func Display(v decimal.Decimal, unit string) (string, string) {
	switch unit {
	case "CNY":
		a := v.Abs()
		switch {
		case a.GreaterThanOrEqual(yi):
			return v.Div(yi).StringFixed(2), "亿元"
		case a.GreaterThanOrEqual(wan):
			return v.Div(wan).StringFixed(2), "万元"
		}
		return v.StringFixed(2), "元"
	case "price":
		return v.StringFixed(2), "元"
	case "ratio":
		return v.Mul(hundr).StringFixed(2), "%"
	case "pp":
		return v.Mul(hundr).StringFixed(2), "个百分点"
	case "times":
		return v.StringFixed(2), "倍"
	case "index_point":
		return v.StringFixed(2), "点"
	case "shares":
		if v.Abs().GreaterThanOrEqual(yi) {
			return v.Div(yi).StringFixed(2), "亿股"
		}
		return v.Div(wan).StringFixed(2), "万股"
	case "count":
		return v.StringFixed(0), ""
	}
	if strings.HasPrefix(unit, "count:") {
		return v.StringFixed(0), strings.TrimPrefix(unit, "count:")
	}
	return v.String(), unit
}

func Ptr(s string) *string { return &s }

// The Fact and Series JSON tags describe what leaves the sheet (series API payload); fields bound to evidence are
// hidden there. The sheet itself is persisted in the step ledger and reloaded on redelivery, so it carries them.
type storedFact struct {
	Fact
	Num      *decimal.Decimal `json:"num,omitempty"`
	Change   bool             `json:"change,omitempty"`
	Evidence []uuid.UUID      `json:"evidence,omitempty"`
	CalcID   *uuid.UUID       `json:"calc_id,omitempty"`
}

type storedSeries struct {
	Series
	Section string `json:"section,omitempty"`
}

type sheetAlias Sheet

type storedSheet struct {
	*sheetAlias
	Facts  []storedFact   `json:"facts"`
	Charts []storedSeries `json:"charts"`
}

func (s *Sheet) MarshalJSON() ([]byte, error) {
	st := storedSheet{sheetAlias: (*sheetAlias)(s), Facts: make([]storedFact, len(s.Facts)), Charts: make([]storedSeries, len(s.Charts))}
	for i, f := range s.Facts {
		st.Facts[i] = storedFact{Fact: f, Num: f.Value, Change: f.Change, Evidence: f.Evidence, CalcID: f.CalcID}
	}
	for i, c := range s.Charts {
		st.Charts[i] = storedSeries{Series: c, Section: c.Section}
	}
	return json.Marshal(st)
}

func (s *Sheet) UnmarshalJSON(b []byte) error {
	st := storedSheet{sheetAlias: (*sheetAlias)(s)}
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	s.Facts, s.Charts = make([]Fact, len(st.Facts)), make([]Series, len(st.Charts))
	for i, f := range st.Facts {
		fact := f.Fact
		fact.Value, fact.Change, fact.Evidence, fact.CalcID = f.Num, f.Change, f.Evidence, f.CalcID
		s.Facts[i] = fact
	}
	for i, c := range st.Charts {
		c.Series.Section = c.Section
		s.Charts[i] = c.Series
	}
	return nil
}
