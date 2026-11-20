package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// New 0.4 research requests use the deployment's single supported quote
// policy. The client does not choose a quote mode, while the persisted plan
// and task intent remain explicit for reproducibility and historical reads.
func TestRealtimeAPIDefaultsOmittedQuoteRequirementToLatestAvailable(t *testing.T) {
	e, entity := realtimeAPIEnv(t)
	c := onboardAPIClient(t, e, "latest-available-default")

	body := map[string]any{
		"skill_id":   "stock_analysis",
		"subject_id": entity,
		"params":     map[string]any{},
	}
	status, data, _ := c.do("POST", "/api/v1/research-plans", body, apiHeaders(realtimeMedia))
	if status != 200 {
		t.Fatalf("create plan without quote requirement = %d, want 200: %s", status, data)
	}
	var result PlanResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Plan == nil || result.Plan.QuoteRequirement == nil || *result.Plan.QuoteRequirement != "latest_available" {
		t.Fatalf("plan quote requirement = %v, want latest_available", result.Plan)
	}

	confirm := map[string]any{
		"plan_id":           result.Plan.ID,
		"plan_hash":         result.Plan.PlanHash,
		"disabled_sections": []string{},
	}
	status, data, _ = c.do("POST", "/api/v1/research-tasks", confirm, apiHeaders(realtimeMedia))
	if status != 202 {
		t.Fatalf("confirm default latest plan = %d, want 202: %s", status, data)
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	if task.QuoteRequirement == nil || *task.QuoteRequirement != "latest_available" {
		t.Fatalf("task quote requirement = %v, want latest_available", task.QuoteRequirement)
	}

	var intent string
	if err := e.pool.QueryRow(context.Background(), `SELECT requirement FROM quote_task_intents WHERE task_id=$1`, task.ID).Scan(&intent); err != nil {
		t.Fatal(err)
	}
	if intent != "latest_available" {
		t.Fatalf("stored quote intent = %q, want latest_available", intent)
	}

	// Keep the UUID assertion here so a malformed response cannot accidentally
	// pass this test while still producing a usable-looking task payload.
	if task.ID == uuid.Nil {
		t.Fatal("confirmed task has empty id")
	}
}
