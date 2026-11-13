package emquant

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// QuoteMappingPolicy refers to separately verified source and license records;
// constructing it does not verify either. The production registry must resolve
// and enforce these references before any mapped observation is used.
type QuoteMappingPolicy struct {
	TimeConvention  string // currently only hhmmss-asia-shanghai
	TimeEvidence    string
	PriceUnit       string // currently only CNY
	PriceEvidence   string
	LatencyMode     string // real_time, delayed, unknown
	LatencyEvidence string // required unless latency is unknown
	LicenseRef      string
	MaxAge          time.Duration
}

// MappedQuote is not a canonical or published record. The eventual data
// service must persist the archive lineage, resolve policy evidence and use
// the actual database publication time before including it in a snapshot.
type MappedQuote struct {
	Price               LastPrice
	DeclaredLatencyMode string
	Source              ArchiveRef
	Locators            map[string]string
	MappingVersion      string
	Policy              QuoteMappingPolicy
}

// MapArchivedQuote maps only the exact one-security TIME,NOW request from its
// hash-verified archive. It never infers real-time status from observed delay.
func MapArchivedQuote(body []byte, ref ArchiveRef, code string, cutoff time.Time, policy QuoteMappingPolicy) (MappedQuote, error) {
	if code == "" || strings.TrimSpace(code) != code || strings.ContainsAny(code, ",\x00") {
		return MappedQuote{}, ErrInvalidQuote
	}
	if policy.TimeConvention != "hhmmss-asia-shanghai" || policy.PriceUnit != "CNY" || strings.TrimSpace(policy.TimeEvidence) == "" || strings.TrimSpace(policy.PriceEvidence) == "" || strings.TrimSpace(policy.LicenseRef) == "" || policy.MaxAge <= 0 {
		return MappedQuote{}, errors.New("emquant: incomplete quote mapping policy references")
	}
	switch policy.LatencyMode {
	case "real_time", "delayed":
		if strings.TrimSpace(policy.LatencyEvidence) == "" {
			return MappedQuote{}, errors.New("emquant: missing latency evidence reference")
		}
	case "unknown":
	default:
		return MappedQuote{}, errors.New("emquant: invalid latency mode")
	}
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) || cutoff.IsZero() || cutoff.Before(ref.ArchivedAt) {
		return MappedQuote{}, errors.New("emquant: missing or late quote archive")
	}
	req, err := json.Marshal(request{Method: "csqsnapshot", Codes: code, Indicators: "TIME,NOW"})
	if err != nil || digest(req) != ref.RequestFingerprint {
		return MappedQuote{}, errors.New("emquant: quote request provenance mismatch")
	}
	cube, observed, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return MappedQuote{}, err
	}
	var envelope archiveBody
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Format != "emquant-sdk-decoded-v2" || envelope.RequestFingerprint != ref.RequestFingerprint || !observed.Equal(ref.ReceivedAt) {
		return MappedQuote{}, errors.New("emquant: quote archive is not bound to request")
	}
	if len(cube.Codes) != 1 || !strings.EqualFold(cube.Codes[0], code) || len(cube.Indicators) != 2 || len(cube.Dates) != 1 {
		return MappedQuote{}, fmt.Errorf("%w: quote axes", ErrShape)
	}
	locators := make(map[string]string, 2)
	for i, field := range cube.Indicators {
		field = strings.ToUpper(field)
		if (field != "TIME" && field != "NOW") || locators[field] != "" {
			return MappedQuote{}, fmt.Errorf("%w: quote indicators", ErrShape)
		}
		locators[field] = fmt.Sprintf("$.data.Values[%d]", i)
	}
	price, err := (Quote{Data: cube, ObservedAt: observed}).LastPriceAt(code, cutoff, policy.MaxAge)
	if err != nil {
		return MappedQuote{}, err
	}
	return MappedQuote{Price: price, DeclaredLatencyMode: policy.LatencyMode, Source: ref, Locators: locators, MappingVersion: "emquant-quote-v1", Policy: policy}, nil
}
