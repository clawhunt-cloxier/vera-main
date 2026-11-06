package facts

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func n(v string) Input {
	return Input{EvidenceID: uuid.New(), Value: decimal.RequireFromString(v), Unit: "CNY"}
}

func pts(vals ...string) []Point {
	var out []Point
	for i, v := range vals {
		if v == "" {
			out = append(out, Point{Date: string(rune('a' + i)), Missing: true})
			continue
		}
		out = append(out, Point{Date: string(rune('a' + i)), EvidenceID: uuid.New(), Value: decimal.RequireFromString(v)})
	}
	return out
}

func TestFormulas(t *testing.T) {
	if c := YoY(n("120"), n("90")); !c.OK() || c.Result.StringFixed(4) != "0.3333" {
		t.Fatalf("yoy %+v", c)
	}
	if c := YoY(n("-5"), n("-10")); c.OK() {
		t.Fatal("negative base must be NOT_APPLICABLE")
	}
	if c := YoY(n("-5"), n("10")); !c.OK() {
		t.Fatal("positive base turning negative is computable")
	}
	if c := ROE(n("10"), n("-50"), n("-30")); c.OK() {
		t.Fatal("negative equity ROE must be NOT_APPLICABLE")
	}
	if c := PeriodReturn(pts("10", "", "", "11"), 4); c.OK() {
		t.Fatal("50% coverage must be NOT_APPLICABLE")
	}
	if c := PeriodReturn(pts("10", "10.5", "", "11", "12"), 5); !c.OK() || c.Result.String() != "0.2" {
		t.Fatalf("return %+v", c)
	}
	c, peak, trough := MaxDrawdown(pts("10", "12", "9", "11"), 4)
	if !c.OK() || c.Result.String() != "-0.25" || peak != "b" || trough != "c" {
		t.Fatalf("drawdown %+v %s %s", c, peak, trough)
	}
	if c := Percentile(Point{Value: decimal.RequireFromString("15")}, pts("10", "12", "20", "30", "14"), 5); !c.OK() || c.Result.String() != "0.6" {
		t.Fatalf("percentile %+v", c)
	}
	if c := Percentile(Point{Value: decimal.RequireFromString("-3")}, pts("10"), 1); c.OK() {
		t.Fatal("negative PE percentile must be NOT_APPLICABLE")
	}
	var peers []PeerValue
	for _, v := range []string{"10", "20", "30", "40", "-5", "50"} {
		peers = append(peers, PeerValue{Input: n(v)})
	}
	m, rank, total := PeerMedianRank(n("35"), peers)
	if !m.OK() || m.Result.String() != "30" || rank != 3 || total != 6 {
		t.Fatalf("peer %+v rank %d/%d", m, rank, total)
	}
	if c := ConsecutiveDays(pts("1", "2", "3", "2", "1", "0.5")); c.Result.String() != "-3" {
		t.Fatalf("consecutive %+v", c)
	}
	if c := SumWindow("F21.sum", pts("1", "2", "", "4", "5", "6", "7", "8", "9", "10"), 10, "CNY", false); !c.OK() || c.Result.String() != "52" {
		t.Fatalf("sum %+v", c)
	}
}

func TestDisplay(t *testing.T) {
	for _, tc := range []struct{ v, unit, d, u string }{
		{"12000000000", "CNY", "120.00", "亿元"}, {"-80000000", "CNY", "-8000.00", "万元"}, {"0.33333", "ratio", "33.33", "%"},
		{"-0.0123", "pp", "-1.23", "个百分点"}, {"25.456", "times", "25.46", "倍"}, {"1234567", "shares", "123.46", "万股"},
	} {
		d, u := Display(decimal.RequireFromString(tc.v), tc.unit)
		if d != tc.d || u != tc.u {
			t.Errorf("Display(%s,%s)=%s %s", tc.v, tc.unit, d, u)
		}
	}
}
