package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const marginIndicators = "TRADINGBALANCEM,PURCHASEWITHBORROWEDMONEYM,REPAYMENTTOBROKERM,SECLENDINGBALANCEVOLUMEM,SECLENDINGBALANCEM"
const marginOptions = "Period=1,FillData=0"

// MarginObservation preserves unverified vendor values, not a flow.margin
// record. Units, source publication time and missing-value semantics require
// product-specific verification before any value may enter research.
type MarginObservation struct {
	Security   string
	VendorDate string
	Cells      map[string]Cell
	Locators   map[string]string
}

type MarginObservations struct {
	Source ArchiveRef
	Rows   []MarginObservation
}

func marginRange(code, from, to string) (time.Time, time.Time, error) {
	if !validSecurityRequest(code) {
		return time.Time{}, time.Time{}, errors.New("emquant: invalid security")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("emquant: invalid margin start date")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Before(start) || end.Sub(start) > 3660*24*time.Hour {
		return time.Time{}, time.Time{}, errors.New("emquant: invalid margin end date")
	}
	return start, end, nil
}

func (c Collector) CollectMarginObservations(ctx context.Context, code, from, to string) (MarginObservations, error) {
	start, end, err := marginRange(code, from, to)
	if err != nil {
		return MarginObservations{}, err
	}
	cube, ref, err := c.CollectCSD(ctx, code, marginIndicators, from, to, marginOptions)
	if err != nil {
		return MarginObservations{}, err
	}
	return mapMarginObservations(cube, ref, code, start, end)
}

func mapMarginObservations(c Cube, ref ArchiveRef, code string, start, end time.Time) (MarginObservations, error) {
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return MarginObservations{}, errors.New("emquant: missing margin archive provenance")
	}
	req, err := json.Marshal(request{Method: "csd", Codes: code, Indicators: marginIndicators, From: start.Format("2006-01-02"), To: end.Format("2006-01-02"), Options: marginOptions})
	if err != nil || ref.RequestFingerprint != digest(req) {
		return MarginObservations{}, errors.New("emquant: margin request provenance mismatch")
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v2", RequestFingerprint: ref.RequestFingerprint, ReceivedAt: ref.ReceivedAt.UTC(), Data: c})
	if err != nil || digest(body) != ref.BodySHA256 {
		return MarginObservations{}, errors.New("emquant: margin response provenance mismatch")
	}
	if len(c.Codes) != 1 || !strings.EqualFold(c.Codes[0], code) || len(c.Indicators) != 5 || len(c.Values) != len(c.Dates)*5 {
		return MarginObservations{}, fmt.Errorf("%w: margin axes", ErrShape)
	}
	indexes := map[string]int{}
	for i, field := range c.Indicators {
		field = strings.ToUpper(field)
		switch field {
		case "TRADINGBALANCEM", "PURCHASEWITHBORROWEDMONEYM", "REPAYMENTTOBROKERM", "SECLENDINGBALANCEVOLUMEM", "SECLENDINGBALANCEM":
		default:
			return MarginObservations{}, fmt.Errorf("%w: margin indicators", ErrShape)
		}
		if _, ok := indexes[field]; ok {
			return MarginObservations{}, fmt.Errorf("%w: duplicate margin indicator", ErrShape)
		}
		indexes[field] = i
	}
	out := MarginObservations{Source: ref, Rows: make([]MarginObservation, 0, len(c.Dates))}
	var previous time.Time
	for dateIndex, text := range c.Dates {
		day, err := parseVendorDate(text)
		if err != nil || day.Before(start) || day.After(end) || (!previous.IsZero() && !day.After(previous)) {
			return MarginObservations{}, fmt.Errorf("%w: margin date or order", ErrShape)
		}
		row := MarginObservation{Security: c.Codes[0], VendorDate: day.Format("2006-01-02"), Cells: make(map[string]Cell, 5), Locators: make(map[string]string, 5)}
		for field, i := range indexes {
			cell, err := c.At(0, i, dateIndex)
			if err != nil {
				return MarginObservations{}, err
			}
			row.Cells[field] = cell
			row.Locators[field] = fmt.Sprintf("$.data.Values[%d]", dateIndex*len(c.Indicators)+i)
		}
		out.Rows = append(out.Rows, row)
		previous = day
	}
	return out, nil
}
