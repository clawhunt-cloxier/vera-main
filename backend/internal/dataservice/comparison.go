package dataservice

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	ComparisonSubjectLimit      = 5
	ComparisonPeerLimit         = 3
	ComparisonDefaultBroadIndex = "000300.SH"
	ComparisonIndexLimit        = 4 // one industry index and three broad market indices
	ComparisonLineLimit         = ComparisonSubjectLimit + ComparisonIndexLimit
	ComparisonSampleDataset     = "ref.industry_peer_sample"
)

// ComparisonSample describes the selection produced by a governed publication.
// It contains no prices or user text. Codes exclude the comparison anchor.
type ComparisonSample struct {
	Codes        []string `json:"codes,omitempty"`
	Date         string   `json:"date,omitempty"`
	IndustryCode string   `json:"industry_code,omitempty"`
	ProofSHA256  string   `json:"proof_sha256,omitempty"`
}

// ComparisonPreparation contains only registered codes and provider options.
type ComparisonPreparation struct {
	AnchorCode string   `json:"anchor"`
	OtherCodes []string `json:"others"`
	ComparisonBenchmarks
	SampleIndustry string `json:"sample_industry,omitempty"`
	SingleStock    bool   `json:"single_stock,omitempty"`
	Window         int    `json:"window,omitempty"`
	CloseCutoff    string `json:"close_cutoff,omitempty"`
}

type ComparisonBenchmarks struct {
	IndustryIndex bool     `json:"industry_index,omitempty"`
	BroadIndexes  []string `json:"broad_indexes,omitempty"`
}

func (b ComparisonBenchmarks) HasIndexes() bool {
	return b.IndustryIndex || len(b.BroadIndexes) != 0
}

func (b ComparisonBenchmarks) Valid() bool {
	if len(b.BroadIndexes) >= ComparisonIndexLimit {
		return false
	}
	for i, code := range b.BroadIndexes {
		if ComparisonBroadIndexName(code) == "" || slices.Contains(b.BroadIndexes[:i], code) {
			return false
		}
	}
	return true
}

var comparisonBroadIndexes = []struct{ code, name string }{
	{ComparisonDefaultBroadIndex, "沪深300"},
	{"000905.SH", "中证500"},
	{"000852.SH", "中证1000"},
}

func ComparisonBroadIndexName(code string) string {
	for _, index := range comparisonBroadIndexes {
		if code == index.code {
			return index.name
		}
	}
	return ""
}

func ResolveComparisonBroadIndex(mention string) string {
	mention = strings.Join(strings.Fields(strings.ToUpper(mention)), "")
	mention = strings.TrimSuffix(mention, "指数")
	for _, index := range comparisonBroadIndexes {
		if mention == index.name || mention == index.code {
			return index.code
		}
	}
	return ""
}

var comparisonIndustryCode = regexp.MustCompile(`^[0-9]{6}\.SI$`)

func IsComparisonIndustryCode(code string) bool {
	return comparisonIndustryCode.MatchString(code)
}

// ComparisonCloseDay is the latest local date eligible for a completed session.
func ComparisonCloseDay(at time.Time) time.Time {
	local := at.In(time.FixedZone("Asia/Shanghai", 8*3600))
	if local.Hour() < 15 {
		local = local.AddDate(0, 0, -1)
	}
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

func ComparisonWindows() []int { return []int{5, 10, 20, 60} }
