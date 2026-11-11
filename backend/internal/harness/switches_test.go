package harness

import (
	"context"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

// Real data or real models must not run until VERA_REAL_RESEARCH_ENABLED is on; synthetic demos are exempt.
func TestRealResearchSwitch(t *testing.T) {
	e, pool := testEngine(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	sub, err := e.Store.EntityByCode(ctx, "DM0001.DEMO")
	if err != nil {
		t.Fatal(err)
	}
	p := plan(t, e, ws, "capital_flow", "DM0001.DEMO") // synthetic deployment: allowed

	e.Synthetic, e.RealResearch = false, false
	if _, err := e.BuildPlan(ctx, ws, "capital_flow", sub, nil); err == nil || errs.As(err).Code != "REAL_RESEARCH_DISABLED" {
		t.Fatalf("BuildPlan with the switch off: want REAL_RESEARCH_DISABLED, got %v", err)
	}
	if _, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil); err == nil || errs.As(err).Code != "REAL_RESEARCH_DISABLED" {
		t.Fatalf("CreateTask with the switch off: want REAL_RESEARCH_DISABLED, got %v", err)
	}

	e.RealResearch = true
	if _, err := e.BuildPlan(ctx, ws, "capital_flow", sub, nil); err != nil {
		t.Fatalf("BuildPlan with the switch on: %v", err)
	}
	if _, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil); err != nil {
		t.Fatalf("CreateTask with the switch on: %v", err)
	}
}
