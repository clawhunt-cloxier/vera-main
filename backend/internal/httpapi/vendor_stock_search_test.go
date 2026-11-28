package httpapi

import (
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

func TestQualifiedStockSearchIsExactEvenWithFuzzyCache(t *testing.T) {
	e, _ := realtimeAPIEnv(t)
	c := onboardAPIClient(t, e, "stock-search")
	// The fixture contains 600000.SH. A cache miss must try the offline
	// test runtime and surface its failure instead of returning a near hit.
	for query, want := range map[string]int{
		"600000.sh": 200, "600001.SH": 503, "600000": 503,
		"API synthetic stock": 200, "API synthetic": 503,
		strings.Repeat("股", 32): 503, strings.Repeat("股", 33): 200, "股,票": 200,
	} {
		status, _, _ := c.do("GET", "/api/v1/entities?q="+url.QueryEscape(query), nil, apiHeaders(realtimeMedia))
		if status != want {
			t.Errorf("%s: got %d, want %d", query, status, want)
		}
	}
}

func TestChoiceNameSearchMergesExactAndFuzzyCandidates(t *testing.T) {
	row := func(name string) dataservice.EntityRow {
		return dataservice.EntityRow{ID: uuid.New(), Kind: "stock", Name: name}
	}
	exact := row("中国石油")
	fuzzy := row("中国石油化工")
	other := row("中国石油服务")
	cached := []dataservice.EntityRow{fuzzy, other}
	for range maxSearchEntities {
		cached = append(cached, row("石油行业公司"))
	}
	called := 0
	got, err := choiceSearchResults("中国石油", cached, func() ([]dataservice.EntityRow, error) {
		called++
		return []dataservice.EntityRow{fuzzy, exact, exact}, nil
	})
	if err != nil || called != 1 || len(got) != maxSearchEntities || got[0].ID != exact.ID || got[1].ID != fuzzy.ID || got[2].ID != other.ID {
		t.Fatalf("unexpected lookup or results: calls=%d candidates=%v err=%v", called, got, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, candidate := range got {
		if seen[candidate.ID] {
			t.Fatalf("duplicate entity %s", candidate.ID)
		}
		seen[candidate.ID] = true
	}
	// No upstream matches: preserve the usable local alternatives.
	got, err = choiceSearchResults("中国石油", cached[:2], func() ([]dataservice.EntityRow, error) { return nil, nil })
	if err != nil || len(got) != 2 || got[0].ID != fuzzy.ID || got[1].ID != other.ID {
		t.Fatalf("lost cached candidates: %v %v", got, err)
	}
}
