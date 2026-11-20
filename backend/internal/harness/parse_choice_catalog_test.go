package harness

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

// choiceCatalogParseEngine models the production split between the broad
// Choice security directory and the TuShare research allowlist. It does not
// enable any vendor runtime calls. The ObservationBrief plan only needs the
// verified A-share code shape at plan time, so this test remains deterministic.
func choiceCatalogParseEngine(t *testing.T) (*Engine, *pgxpool.Pool, *dataservice.Store) {
	t.Helper()
	pool := dbtest.New(t)
	cat, err := dataservice.LoadCatalog(root + "contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	choice := dataservice.NewStore(pool, cat)
	// The research store deliberately contains only the configured TuShare
	// subject. Choice-discovered securities remain searchable through choice.
	research := dataservice.NewResearchStore(pool, cat, "tushare", []string{"600519.SH"}, true)
	pol, err := policy.Load(root + "contracts/examples/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Pool:          pool,
		Store:         research,
		ChoiceCatalog: choice,
		Brief:         &emquant.BriefService{Catalog: cat, ProviderID: "choice-test", Product: "daily"},
		Policy:        pol,
		Skills:        UnifiedCloseSkills(),
		RealResearch:  true,
		Now:           func() time.Time { return time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC) },
		CloseResearchCutoff: func(context.Context) (time.Time, error) {
			return time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC), nil
		},
	}
	return e, pool, choice
}

func TestParsePlanResolvesChoiceOnlyAshareNames(t *testing.T) {
	e, pool, choice := choiceCatalogParseEngine(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		code string
	}{
		{name: "中国移动", code: "600941.SH"},
		{name: "中国石油", code: "601857.SH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := choice.UpsertSearchEntity(ctx, "stock", tc.code, tc.name, nil); err != nil {
				t.Fatal(err)
			}
			ws := workspace(t, pool)
			got, err := e.ParsePlan(ctx, ws, "分析"+tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != "plan" || got.Plan == nil {
				t.Fatalf("ParsePlan(%q) = kind %q, candidates=%v, want plan", tc.name, got.Kind, got.Candidates)
			}
			if got.Plan.Subject.Code != tc.code || got.Plan.Subject.Name != tc.name {
				t.Fatalf("resolved subject = %s %s, want %s %s", got.Plan.Subject.Code, got.Plan.Subject.Name, tc.code, tc.name)
			}
			if got.Plan.SkillVersion != emquant.ObservationBriefVersion {
				t.Fatalf("unregistered Choice subject used skill version %q, want %q", got.Plan.SkillVersion, emquant.ObservationBriefVersion)
			}
		})
	}
}

func TestParsePlanKeepsRegisteredSubjectOnCloseResearch(t *testing.T) {
	e, pool, choice := choiceCatalogParseEngine(t)
	ctx := context.Background()
	if _, err := choice.UpsertSearchEntity(ctx, "stock", "600519.SH", "贵州茅台", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE entities SET attrs='{"tushare_research_subject":true}'::jsonb WHERE code='600519.SH'`); err != nil {
		t.Fatal(err)
	}
	ws := workspace(t, pool)
	got, err := e.ParsePlan(ctx, ws, "分析贵州茅台")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "plan" || got.Plan == nil {
		t.Fatalf("registered subject returned %q, want plan", got.Kind)
	}
	if got.Plan.Subject.Code != "600519.SH" {
		t.Fatalf("resolved subject = %s, want 600519.SH", got.Plan.Subject.Code)
	}
	if got.Plan.SkillVersion != CloseUnifiedVersion {
		t.Fatalf("registered subject used skill version %q, want %q", got.Plan.SkillVersion, CloseUnifiedVersion)
	}
}
