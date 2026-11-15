package emquant

import (
	"errors"
	"testing"
)

func TestCubeDateMajorOrder(t *testing.T) {
	c := Cube{Codes: []string{"A", "B"}, Indicators: []string{"X", "Y"}, Dates: []string{"D1", "D2"}}
	for i := range 8 {
		c.Values = append(c.Values, Cell{Type: 6, Value: i})
	}
	for _, tt := range []struct{ code, indicator, date, want int }{
		{0, 0, 0, 0}, {1, 1, 0, 3}, {0, 0, 1, 4}, {1, 0, 1, 6}, {1, 1, 1, 7},
	} {
		got, err := c.At(tt.code, tt.indicator, tt.date)
		if err != nil || got.Value != tt.want {
			t.Fatalf("At(%d,%d,%d) = %#v, %v; want %d", tt.code, tt.indicator, tt.date, got, err, tt.want)
		}
	}
}

func TestCubeRejectsMalformedAndPreservesNull(t *testing.T) {
	c := Cube{Codes: []string{"A"}, Indicators: []string{"X"}, Dates: []string{"D"}, Values: []Cell{{Type: 0}}}
	got, err := c.At(0, 0, 0)
	if err != nil || got.Type != 0 || got.Value != nil {
		t.Fatalf("null = %#v, %v", got, err)
	}
	if _, err = c.At(-1, 0, 0); !errors.Is(err, ErrShape) {
		t.Fatalf("negative index: %v", err)
	}
	c.Values = nil
	if _, err = c.At(0, 0, 0); !errors.Is(err, ErrShape) {
		t.Fatalf("truncated values: %v", err)
	}
	c.Values = []Cell{{Type: 6, Value: 0}, {Type: 6, Value: 1}}
	if _, err = c.At(0, 0, 0); !errors.Is(err, ErrShape) {
		t.Fatalf("extra values: %v", err)
	}
}
