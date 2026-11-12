// Package emquant contains preparation for a Choice data adapter. It is not a
// registered provider and does not make data available to the application.
package emquant

import (
	"errors"
	"fmt"
)

// Cell is an unnormalized SDK value. Null is explicit: an absent value is not
// the same thing as zero. Non-null values retain their original SDK type.
type Cell struct {
	Type  int
	Value any
}

// Cube is the result shape used by the SDK's csd interface. Values are in
// date-major, code-major, indicator-minor order, as in the SDK wrapper.
type Cube struct {
	Codes      []string
	Indicators []string
	Dates      []string
	Values     []Cell
}

var ErrShape = errors.New("emquant: invalid result shape")

// At rejects malformed upstream arrays instead of silently converting missing
// elements to NULL; that would hide schema drift or a truncated response.
func (c Cube) At(code, indicator, date int) (Cell, error) {
	if code < 0 || code >= len(c.Codes) || indicator < 0 || indicator >= len(c.Indicators) || date < 0 || date >= len(c.Dates) {
		return Cell{}, fmt.Errorf("%w: index out of range", ErrShape)
	}
	// Avoid integer overflow before calculating the expected number of cells.
	maxInt := int(^uint(0) >> 1)
	if len(c.Codes) > maxInt/len(c.Indicators) || len(c.Codes)*len(c.Indicators) > maxInt/len(c.Dates) {
		return Cell{}, fmt.Errorf("%w: dimensions overflow", ErrShape)
	}
	expected := len(c.Codes) * len(c.Indicators) * len(c.Dates)
	if len(c.Values) != expected {
		return Cell{}, fmt.Errorf("%w: got %d cells, expected %d", ErrShape, len(c.Values), expected)
	}
	index := date*len(c.Codes)*len(c.Indicators) + code*len(c.Indicators) + indicator
	return c.Values[index], nil
}
