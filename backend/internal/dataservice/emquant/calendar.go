package emquant

import (
	"fmt"
	"strings"
	"time"
)

func parseVendorDate(s string) (time.Time, error) {
	for _, layout := range []string{"2006/1/2", "2006-01-02"} {
		if date, err := time.Parse(layout, s); err == nil {
			return date, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: invalid vendor date", ErrShape)
}

func openDates(c Cube, start, end time.Time) ([]string, error) {
	if len(c.Codes) != 1 || c.Codes[0] != "" || len(c.Indicators) != 1 || !strings.EqualFold(c.Indicators[0], "TRADEDATE") || len(c.Values) != len(c.Dates) {
		return nil, fmt.Errorf("%w: calendar axes", ErrShape)
	}
	dates := make([]string, 0, len(c.Dates))
	var previous time.Time
	for i, axis := range c.Dates {
		cell, err := c.At(0, 0, i)
		if err != nil {
			return nil, err
		}
		value, ok := cell.Value.(string)
		if !ok || (cell.Type != 13 && cell.Type != 14) {
			return nil, fmt.Errorf("%w: calendar value", ErrShape)
		}
		day, err := parseVendorDate(axis)
		if err != nil {
			return nil, err
		}
		vday, err := parseVendorDate(value)
		if err != nil || !day.Equal(vday) {
			return nil, fmt.Errorf("%w: calendar date mismatch", ErrShape)
		}
		if day.Before(start) || day.After(end) || (!previous.IsZero() && !day.After(previous)) {
			return nil, fmt.Errorf("%w: calendar date range or order", ErrShape)
		}
		dates = append(dates, day.Format("2006-01-02"))
		previous = day
	}
	return dates, nil
}
