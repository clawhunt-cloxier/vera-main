package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const industryIndicators = "HISSW2021,HISSW2021CODE"

// IndustryObservation is a vendor response for one requested classification
// level and cutoff date, not a canonical industry interval. Effective dates,
// historical revisions, code hierarchy and product permissions still require
// verification before it can be used as ref.industry in a research snapshot.
type IndustryObservation struct {
	Security       string
	RequestedAsOf  string
	RequestedLevel int
	IndustryName   string
	IndustryCode   string
	Source         ArchiveRef
	Locators       map[string]string
}

func industryOptions(code, asOf string, level int) (string, error) {
	if code == "" || strings.TrimSpace(code) != code || strings.ContainsAny(code, ",\x00") {
		return "", errors.New("emquant: invalid security")
	}
	date, err := time.Parse("2006-01-02", asOf)
	if err != nil || level < 1 || level > 3 {
		return "", errors.New("emquant: invalid industry cutoff or level")
	}
	return fmt.Sprintf("EndDate=%s,ClassiFication=%d", date.Format("20060102"), level), nil
}

// CollectIndustryLevel requests one level, archives the decoded response and
// checks the exact request before returning it. Empty or ambiguous data is not
// inferred from another level. The SDK's response has not yet been verified
// with a real account for these fields.
func (c Collector) CollectIndustryLevel(ctx context.Context, code, asOf string, level int) (IndustryObservation, error) {
	options, err := industryOptions(code, asOf, level)
	if err != nil {
		return IndustryObservation{}, err
	}
	cube, ref, err := c.CollectCSS(ctx, code, industryIndicators, options)
	if err != nil {
		return IndustryObservation{}, err
	}
	return mapIndustryLevel(cube, ref, code, asOf, level)
}

func mapIndustryLevel(cube Cube, ref ArchiveRef, code, asOf string, level int) (IndustryObservation, error) {
	options, err := industryOptions(code, asOf, level)
	if err != nil {
		return IndustryObservation{}, err
	}
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return IndustryObservation{}, errors.New("emquant: missing industry archive provenance")
	}
	req, err := json.Marshal(request{Method: "css", Codes: code, Indicators: industryIndicators, Options: options})
	if err != nil || ref.RequestFingerprint != digest(req) {
		return IndustryObservation{}, errors.New("emquant: industry request provenance mismatch")
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v2", RequestFingerprint: ref.RequestFingerprint, ReceivedAt: ref.ReceivedAt.UTC(), Data: cube})
	if err != nil || digest(body) != ref.BodySHA256 {
		return IndustryObservation{}, errors.New("emquant: industry response provenance mismatch")
	}
	if len(cube.Codes) != 1 || !strings.EqualFold(cube.Codes[0], code) || len(cube.Indicators) != 2 || len(cube.Dates) != 1 || len(cube.Values) != 2 {
		return IndustryObservation{}, fmt.Errorf("%w: industry axes", ErrShape)
	}
	out := IndustryObservation{Security: cube.Codes[0], RequestedAsOf: asOf, RequestedLevel: level, Source: ref, Locators: make(map[string]string, 2)}
	for i, field := range cube.Indicators {
		field = strings.ToUpper(field)
		if (field != "HISSW2021" && field != "HISSW2021CODE") || out.Locators[field] != "" {
			return IndustryObservation{}, fmt.Errorf("%w: industry indicators", ErrShape)
		}
		cell, err := cube.At(0, i, 0)
		if err != nil {
			return IndustryObservation{}, err
		}
		value, ok := cell.Value.(string)
		if !ok || (cell.Type != 13 && cell.Type != 14) || value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return IndustryObservation{}, fmt.Errorf("%w: invalid industry value", ErrShape)
		}
		out.Locators[field] = fmt.Sprintf("$.data.Values[%d]", i)
		if field == "HISSW2021" {
			out.IndustryName = value
		} else {
			out.IndustryCode = value
		}
	}
	return out, nil
}
