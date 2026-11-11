// Package policy loads the versioned budget/quota policy (contracts/schemas/policy.schema.json).
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/shopspring/decimal"
)

type Budget struct {
	DeadlineSeconds int             `json:"deadline_seconds"`
	MaxModelCalls   int             `json:"max_model_calls"`
	InputTokens     int             `json:"input_tokens"`
	OutputTokens    int             `json:"output_tokens"`
	CostLimitUSD    decimal.Decimal `json:"cost_limit_usd"`
}

func (b Budget) Deadline() time.Duration { return time.Duration(b.DeadlineSeconds) * time.Second }

type Quota struct {
	UserActiveTasks           int             `json:"user_active_tasks"`
	UserQueuedTasks           int             `json:"user_queued_tasks"`
	UserDailyTasks            int             `json:"user_daily_tasks"`
	UserDailyDeepResearch     int             `json:"user_daily_deep_research"`
	PlatformInFlightTasks     int             `json:"platform_in_flight_tasks"`
	PlatformModelConcurrency  int             `json:"platform_model_concurrency"`
	PerTaskModelConcurrency   int             `json:"per_task_model_concurrency"`
	PlatformDailyCostLimitUSD decimal.Decimal `json:"platform_daily_cost_limit_usd"`
	PlanParsePerUserPerMinute int             `json:"plan_parse_per_user_per_minute"`
}

type ModelCall struct {
	HTTPTimeoutSeconds     int `json:"http_timeout_seconds"`
	TransientRetries       int `json:"transient_retries"`
	FormatRepairs          int `json:"format_repairs"`
	UnknownResultHoldHours int `json:"unknown_result_hold_hours"`
}

type Policy struct {
	Version   string            `json:"version"`
	Budgets   map[string]Budget `json:"budgets"`
	Quota     Quota             `json:"quota"`
	ModelCall ModelCall         `json:"model_call"`
}

func Load(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	for _, k := range []string{"stock_analysis", "capital_flow", "deep_research", "plan_parse"} {
		if _, ok := p.Budgets[k]; !ok {
			return nil, fmt.Errorf("policy %s missing budget %s", p.Version, k)
		}
	}
	return &p, nil
}
