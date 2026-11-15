package emquant

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
	StatusIndicator string
	StatusEvidence  string
	StatusMapping   map[string]string // exact typed keys: s:<text>, i:<signed>, u:<unsigned>
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
	MarketStatus        string
}

var quoteStatusIndicator = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// QuotePolicyFields validates the configured status mapping and returns the
// exact ordered vendor fields; it does not certify the policy's evidence.
func QuotePolicyFields(policy QuoteMappingPolicy) ([]string, error) {
	fields := []string{"TIME", "NOW"}
	if policy.StatusIndicator == "" {
		if policy.StatusEvidence != "" || len(policy.StatusMapping) != 0 {
			return nil, errors.New("emquant: incomplete quote status policy")
		}
		return fields, nil
	}
	if !quoteStatusIndicator.MatchString(policy.StatusIndicator) || policy.StatusIndicator == "TIME" || policy.StatusIndicator == "NOW" || strings.TrimSpace(policy.StatusEvidence) == "" || len(policy.StatusMapping) == 0 || len(policy.StatusMapping) > 64 {
		return nil, errors.New("emquant: invalid quote status policy")
	}
	for key, value := range policy.StatusMapping {
		if len(key) < 3 || len(key) > 256 || !utf8.ValidString(key) || strings.ContainsFunc(key, unicode.IsControl) || (value != "continuous" && value != "suspended" && value != "closed") {
			return nil, errors.New("emquant: invalid quote status mapping")
		}
		switch {
		case strings.HasPrefix(key, "s:"):
			if strings.TrimPrefix(key, "s:") == "" {
				return nil, errors.New("emquant: invalid quote status mapping")
			}
		case strings.HasPrefix(key, "i:"):
			raw := strings.TrimPrefix(key, "i:")
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || strconv.FormatInt(n, 10) != raw {
				return nil, errors.New("emquant: invalid quote status mapping")
			}
		case strings.HasPrefix(key, "u:"):
			raw := strings.TrimPrefix(key, "u:")
			n, err := strconv.ParseUint(raw, 10, 64)
			if err != nil || strconv.FormatUint(n, 10) != raw {
				return nil, errors.New("emquant: invalid quote status mapping")
			}
		default:
			return nil, errors.New("emquant: invalid quote status mapping")
		}
	}
	return append(fields, policy.StatusIndicator), nil
}

func quoteStatusKey(cell Cell) string {
	switch cell.Type {
	case 13, 14:
		if s, ok := cell.Value.(string); ok && s != "" {
			return "s:" + s
		}
	case 1, 3, 4, 6, 8:
		if n, ok := cell.Value.(int64); ok {
			return "i:" + strconv.FormatInt(n, 10)
		}
	case 2, 5, 7, 9:
		if n, ok := cell.Value.(uint64); ok {
			return "u:" + strconv.FormatUint(n, 10)
		}
	}
	return ""
}

// MapArchivedQuote maps only the exact one-security configured request from its
// hash-verified archive. It never infers real-time status from observed delay.
func MapArchivedQuote(body []byte, ref ArchiveRef, code string, cutoff time.Time, policy QuoteMappingPolicy) (MappedQuote, error) {
	if code == "" || strings.TrimSpace(code) != code || strings.ContainsAny(code, ",\x00") {
		return MappedQuote{}, ErrInvalidQuote
	}
	if policy.TimeConvention != "hhmmss-asia-shanghai" || policy.PriceUnit != "CNY" || strings.TrimSpace(policy.TimeEvidence) == "" || strings.TrimSpace(policy.PriceEvidence) == "" || strings.TrimSpace(policy.LicenseRef) == "" || policy.MaxAge <= 0 {
		return MappedQuote{}, errors.New("emquant: incomplete quote mapping policy references")
	}
	fields, err := QuotePolicyFields(policy)
	if err != nil {
		return MappedQuote{}, err
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
	req, err := json.Marshal(request{Method: "csqsnapshot", Codes: code, Indicators: strings.Join(fields, ",")})
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
	if len(cube.Codes) != 1 || !strings.EqualFold(cube.Codes[0], code) || len(cube.Indicators) != len(fields) || len(cube.Dates) != 1 {
		return MappedQuote{}, fmt.Errorf("%w: quote axes", ErrShape)
	}
	locators := make(map[string]string, len(fields))
	status := "unknown"
	for i, field := range cube.Indicators {
		field = strings.ToUpper(field)
		if (field != "TIME" && field != "NOW" && field != policy.StatusIndicator) || locators[field] != "" {
			return MappedQuote{}, fmt.Errorf("%w: quote indicators", ErrShape)
		}
		locators[field] = fmt.Sprintf("$.data.Values[%d]", i)
		if policy.StatusIndicator != "" && field == policy.StatusIndicator {
			cell, err := cube.At(0, i, 0)
			if err != nil {
				return MappedQuote{}, err
			}
			if mapped, ok := policy.StatusMapping[quoteStatusKey(cell)]; ok {
				status = mapped
			}
		}
	}
	price, err := (Quote{Data: cube, ObservedAt: observed}).LastPriceAt(code, cutoff, policy.MaxAge)
	if err != nil {
		return MappedQuote{}, err
	}
	return MappedQuote{Price: price, DeclaredLatencyMode: policy.LatencyMode, Source: ref, Locators: locators, MappingVersion: "emquant-quote-v1", Policy: policy, MarketStatus: status}, nil
}
