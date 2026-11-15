package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const securityIndicators = "NAME,LISTDATE,DELISTDATE"

// SecurityBasics is a partial current vendor observation, not ref.security.
// The name is not asserted to be historically valid; a missing delisting
// date does not prove the security is listed or actively trading.
type SecurityBasics struct {
	Security         string
	ShortName        string
	FirstListingDate string
	DelistingDate    *string
	Source           ArchiveRef
	Locators         map[string]string
}

func validSecurityRequest(code string) bool {
	return code != "" && strings.TrimSpace(code) == code && !strings.ContainsAny(code, ",\x00")
}

// CollectSecurityBasics archives the vendor response before mapping. Returned
// values do not establish board, trading status, financial classification,
// margin eligibility, share counts or historical naming semantics.
func (c Collector) CollectSecurityBasics(ctx context.Context, code string) (SecurityBasics, error) {
	if !validSecurityRequest(code) {
		return SecurityBasics{}, errors.New("emquant: invalid security")
	}
	cube, ref, err := c.CollectCSS(ctx, code, securityIndicators, "")
	if err != nil {
		return SecurityBasics{}, err
	}
	return mapSecurityBasics(cube, ref, code)
}

func mapSecurityBasics(cube Cube, ref ArchiveRef, code string) (SecurityBasics, error) {
	if !validSecurityRequest(code) {
		return SecurityBasics{}, errors.New("emquant: invalid security")
	}
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return SecurityBasics{}, errors.New("emquant: missing security archive provenance")
	}
	req, err := json.Marshal(request{Method: "css", Codes: code, Indicators: securityIndicators})
	if err != nil || ref.RequestFingerprint != digest(req) {
		return SecurityBasics{}, errors.New("emquant: security request provenance mismatch")
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v2", RequestFingerprint: ref.RequestFingerprint, ReceivedAt: ref.ReceivedAt.UTC(), Data: cube})
	if err != nil || digest(body) != ref.BodySHA256 {
		return SecurityBasics{}, errors.New("emquant: security response provenance mismatch")
	}
	if len(cube.Codes) != 1 || !strings.EqualFold(cube.Codes[0], code) || len(cube.Indicators) != 3 || len(cube.Dates) != 1 || len(cube.Values) != 3 {
		return SecurityBasics{}, fmt.Errorf("%w: security axes", ErrShape)
	}
	out := SecurityBasics{Security: cube.Codes[0], Source: ref, Locators: make(map[string]string, 3)}
	for i, field := range cube.Indicators {
		field = strings.ToUpper(field)
		if (field != "NAME" && field != "LISTDATE" && field != "DELISTDATE") || out.Locators[field] != "" {
			return SecurityBasics{}, fmt.Errorf("%w: security indicators", ErrShape)
		}
		cell, err := cube.At(0, i, 0)
		if err != nil {
			return SecurityBasics{}, err
		}
		out.Locators[field] = fmt.Sprintf("$.data.Values[%d]", i)
		if field == "DELISTDATE" && cell.Type == 0 && cell.Value == nil {
			continue
		}
		value, ok := cell.Value.(string)
		if !ok || (cell.Type != 13 && cell.Type != 14) || value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return SecurityBasics{}, fmt.Errorf("%w: invalid security value", ErrShape)
		}
		if field == "NAME" {
			out.ShortName = value
			continue
		}
		date, err := parseVendorDate(value)
		if err != nil {
			return SecurityBasics{}, err
		}
		normalized := date.Format("2006-01-02")
		if field == "LISTDATE" {
			out.FirstListingDate = normalized
		} else {
			out.DelistingDate = &normalized
		}
	}
	if out.DelistingDate != nil && *out.DelistingDate < out.FirstListingDate {
		return SecurityBasics{}, fmt.Errorf("%w: delisting before listing", ErrShape)
	}
	return out, nil
}
