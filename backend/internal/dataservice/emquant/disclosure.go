package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const disclosureIndicators = "STMTACTDATE,CORRECTANNCNUM,CORRECTANNCDATE"

// DisclosureObservation retains unverified SDK cells for a requested period.
// Dates may lack a time of day and corrections may not identify the version of
// any financial values; this cannot establish a point-in-time statement.
type DisclosureObservation struct {
	Security        string
	RequestedPeriod string
	Cells           map[string]Cell
	Source          ArchiveRef
	Locators        map[string]string
}

func disclosureOptions(code, period string) (string, error) {
	if !validSecurityRequest(code) {
		return "", errors.New("emquant: invalid security")
	}
	date, err := time.Parse("2006-01-02", period)
	if err != nil || (date.Format("0102") != "0331" && date.Format("0102") != "0630" && date.Format("0102") != "0930" && date.Format("0102") != "1231") {
		return "", errors.New("emquant: invalid report period")
	}
	return "ReportDate=" + date.Format("20060102"), nil
}

// CollectDisclosureObservation archives a vendor response before returning it.
// The response is not a verified disclosure time or a version association.
func (c Collector) CollectDisclosureObservation(ctx context.Context, code, period string) (DisclosureObservation, error) {
	options, err := disclosureOptions(code, period)
	if err != nil {
		return DisclosureObservation{}, err
	}
	cube, ref, err := c.CollectCSS(ctx, code, disclosureIndicators, options)
	if err != nil {
		return DisclosureObservation{}, err
	}
	return mapDisclosureObservation(cube, ref, code, period)
}

func mapDisclosureObservation(cube Cube, ref ArchiveRef, code, period string) (DisclosureObservation, error) {
	options, err := disclosureOptions(code, period)
	if err != nil {
		return DisclosureObservation{}, err
	}
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return DisclosureObservation{}, errors.New("emquant: missing disclosure archive provenance")
	}
	req, err := json.Marshal(request{Method: "css", Codes: code, Indicators: disclosureIndicators, Options: options})
	if err != nil || ref.RequestFingerprint != digest(req) {
		return DisclosureObservation{}, errors.New("emquant: disclosure request provenance mismatch")
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v2", RequestFingerprint: ref.RequestFingerprint, ReceivedAt: ref.ReceivedAt.UTC(), Data: cube})
	if err != nil || digest(body) != ref.BodySHA256 {
		return DisclosureObservation{}, errors.New("emquant: disclosure response provenance mismatch")
	}
	if len(cube.Codes) != 1 || !strings.EqualFold(cube.Codes[0], code) || len(cube.Indicators) != 3 || len(cube.Dates) != 1 || len(cube.Values) != 3 {
		return DisclosureObservation{}, fmt.Errorf("%w: disclosure axes", ErrShape)
	}
	out := DisclosureObservation{Security: cube.Codes[0], RequestedPeriod: period, Cells: make(map[string]Cell, 3), Source: ref, Locators: make(map[string]string, 3)}
	for i, indicator := range cube.Indicators {
		name := strings.ToUpper(indicator)
		if (name != "STMTACTDATE" && name != "CORRECTANNCNUM" && name != "CORRECTANNCDATE") || out.Locators[name] != "" {
			return DisclosureObservation{}, fmt.Errorf("%w: disclosure indicators", ErrShape)
		}
		cell, err := cube.At(0, i, 0)
		if err != nil {
			return DisclosureObservation{}, err
		}
		out.Cells[name] = cell
		out.Locators[name] = fmt.Sprintf("$.data.Values[%d]", i)
	}
	return out, nil
}
