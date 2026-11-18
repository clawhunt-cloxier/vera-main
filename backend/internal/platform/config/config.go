// Package config loads environment configuration and enforces production guards (CONVENTIONS §6).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/modelguard"
)

type ModelProfile struct {
	Name       string
	Adapter    string // openai | record | sim | replay
	BaseURL    string
	ModelID    string
	APIKey     string
	PriceTable string
	// Thinking is "" (not sent; vendor default), "enabled" or "disabled" (sent as thinking.type). An explicit
	// "thinking" key in ExtraBody wins over this setting.
	Thinking string
	// ExtraBody is a JSON object merged into chat/completions requests (vendor switches). Keys owned by the gateway
	// (ReservedBodyKeys) are rejected because they would desynchronise hashing, budgets or validation.
	ExtraBody   map[string]any
	Temperature float64
	// MaxOutput overrides the per-call default output limit for every call on this profile; 0 keeps the defaults.
	MaxOutput int
	// TimeoutSeconds is the per-HTTP-request timeout; 0 uses policy model_call.http_timeout_seconds.
	TimeoutSeconds int
	// Concurrency limits in-flight calls on this profile; 0 means only the platform limit applies.
	Concurrency int
}

// ReservedBodyKeys are request fields the gateway owns; MODEL_<PROFILE>_EXTRA_BODY may not set them.
var ReservedBodyKeys = []string{"model", "messages", "response_format", "stream", "max_tokens", "max_completion_tokens", "temperature"}

type Config struct {
	Env                string // development | test | production
	APIAddr            string
	DatabaseURL        string
	PublicURL          string // e.g. http://localhost:8080 ; used for redirects and Origin checks
	CookieSecure       bool
	OIDCIssuer         string
	OIDCInternal       string // optional server-side base for the issuer (container network)
	OIDCClientID       string
	OIDCClientSecret   string
	DevOIDC            bool   // built-in development issuer at /dev-oidc; refused outside development/test
	ContractsDir       string // contracts/ (catalog, model schemas)
	WebDir             string // built web app to serve (optional)
	AutoMigrate        bool
	WorkerConcurrency  int
	PolicyPath         string
	PriceTablePath     string
	ReplayDir          string
	FixtureManifest    string
	DataProvider       string // fixture | <vendor id>
	Registration       bool
	RealResearch       bool
	RequireChoiceQuote bool
	DailyCostLimit     decimal.Decimal
	Profiles           map[string]ModelProfile
	WorkerQueues       int
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func intEnv(k string, d int) int {
	v, err := strconv.Atoi(env(k, strconv.Itoa(d)))
	if err != nil {
		return d
	}
	return v
}

func boolEnv(k string, d bool) bool {
	v, err := strconv.ParseBool(env(k, strconv.FormatBool(d)))
	if err != nil {
		return d
	}
	return v
}

// Load reads VERA_* variables. Unknown or unsafe combinations fail fast.
func Load() (*Config, error) {
	c := &Config{
		Env:                env("VERA_ENV", "development"),
		APIAddr:            env("VERA_API_ADDR", "127.0.0.1:8081"),
		DatabaseURL:        env("DATABASE_URL", ""),
		PublicURL:          strings.TrimRight(env("VERA_PUBLIC_URL", "http://localhost:8080"), "/"),
		CookieSecure:       boolEnv("VERA_COOKIE_SECURE", true),
		OIDCIssuer:         strings.TrimRight(env("OIDC_ISSUER", ""), "/"),
		OIDCInternal:       strings.TrimRight(env("OIDC_INTERNAL_ISSUER", ""), "/"),
		DevOIDC:            boolEnv("VERA_DEV_OIDC", false),
		ContractsDir:       env("VERA_CONTRACTS_DIR", "../contracts"),
		WebDir:             env("VERA_WEB_DIR", ""),
		AutoMigrate:        boolEnv("VERA_AUTO_MIGRATE", true),
		WorkerConcurrency:  intEnv("VERA_WORKER_CONCURRENCY", 4),
		OIDCClientID:       env("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:   env("OIDC_CLIENT_SECRET", ""),
		PolicyPath:         env("VERA_POLICY", "../contracts/examples/policy.json"),
		PriceTablePath:     env("VERA_PRICE_TABLE", "../deploy/config/prices.json"),
		ReplayDir:          env("VERA_REPLAY_DIR", "../tests/fixtures/replay"),
		FixtureManifest:    env("VERA_FIXTURE_MANIFEST", "../tests/fixtures/demo-universe.json"),
		DataProvider:       env("VERA_DATA_PROVIDER", "fixture"),
		Registration:       boolEnv("VERA_REGISTRATION_ENABLED", false),
		RealResearch:       boolEnv("VERA_REAL_RESEARCH_ENABLED", false),
		RequireChoiceQuote: boolEnv("VERA_REQUIRE_CHOICE_QUOTE", false),
		Profiles:           map[string]ModelProfile{},
	}
	limit, err := decimal.NewFromString(env("VERA_DAILY_MODEL_COST_LIMIT_USD", "30.00"))
	if err != nil {
		return nil, fmt.Errorf("VERA_DAILY_MODEL_COST_LIMIT_USD: %w", err)
	}
	c.DailyCostLimit = limit
	var problems []string
	for _, p := range []string{"writer", "verifier", "router"} {
		mp, perr := loadProfile(p)
		problems = append(problems, perr...)
		c.Profiles[p] = mp
	}
	err = c.validate(problems)
	return c, err
}

// loadProfile reads MODEL_<PROFILE>_* variables. Malformed values are reported, never silently defaulted.
func loadProfile(name string) (ModelProfile, []string) {
	u := "MODEL_" + strings.ToUpper(name) + "_"
	p := ModelProfile{
		Name:       name,
		Adapter:    env(u+"ADAPTER", "sim"),
		BaseURL:    env(u+"BASE_URL", ""),
		ModelID:    env(u+"ID", ""),
		APIKey:     env(u+"API_KEY", ""),
		PriceTable: env("MODEL_PRICE_TABLE_VERSION", ""),
	}
	var problems []string
	bad := func(k, why string) {
		problems = append(problems, fmt.Sprintf("model profile %s: %s%s %s", name, u, k, why))
	}
	if v := env(u+"THINKING", ""); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "on", "enabled":
			p.Thinking = "enabled"
		case "false", "0", "off", "disabled":
			p.Thinking = "disabled"
		default:
			bad("THINKING", "must be enabled/disabled (or true/false)")
		}
	}
	if v := env(u+"EXTRA_BODY", ""); v != "" {
		if err := json.Unmarshal([]byte(v), &p.ExtraBody); err != nil || p.ExtraBody == nil {
			bad("EXTRA_BODY", "must be a JSON object")
		}
		for _, k := range ReservedBodyKeys {
			if _, ok := p.ExtraBody[k]; ok {
				bad("EXTRA_BODY", fmt.Sprintf("may not set %q (use the dedicated MODEL_<PROFILE>_* variable)", k))
			}
		}
		if err := modelguard.ValidateExtraBody(p.ExtraBody); err != nil {
			bad("EXTRA_BODY", "must not enable model tools or search")
		}
	}
	if v := env(u+"TEMPERATURE", ""); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 2 {
			bad("TEMPERATURE", "must be a number in [0, 2]")
		}
		p.Temperature = f
	}
	if v := env(u+"MAX_OUTPUT_TOKENS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			bad("MAX_OUTPUT_TOKENS", "must be a positive integer")
		}
		p.MaxOutput = n
	}
	if v := env(u+"TIMEOUT_SECONDS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			bad("TIMEOUT_SECONDS", "must be a positive integer")
		}
		p.TimeoutSeconds = n
	}
	if v := env(u+"CONCURRENCY", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			bad("CONCURRENCY", "must be a positive integer")
		}
		p.Concurrency = n
	}
	return p, problems
}

func (c *Config) validate(problems []string) error {
	switch c.Env {
	case "development", "test", "production":
	default:
		problems = append(problems, "VERA_ENV must be development, test or production")
	}
	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if c.DevOIDC && c.Env == "production" {
		problems = append(problems, "production refuses the development OIDC issuer (VERA_DEV_OIDC)")
	}
	if !c.DevOIDC && (c.OIDCIssuer == "" || c.OIDCClientID == "" || c.OIDCClientSecret == "") {
		problems = append(problems, "OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET are required unless VERA_DEV_OIDC=true")
	}
	for name, p := range c.Profiles {
		switch p.Adapter {
		case "openai", "record":
			// record forwards cassette misses to the real vendor, so it needs everything a real call needs.
			if p.BaseURL == "" || p.ModelID == "" || p.APIKey == "" || p.PriceTable == "" {
				problems = append(problems, fmt.Sprintf("model profile %s: %s adapter needs base_url, model_id, api_key and price table version", name, p.Adapter))
			}
		case "sim", "replay":
		default:
			problems = append(problems, fmt.Sprintf("model profile %s: unknown adapter %q", name, p.Adapter))
		}
	}
	if c.Env == "production" {
		if !c.CookieSecure {
			problems = append(problems, "production requires VERA_COOKIE_SECURE=true")
		}
		if c.DataProvider == "fixture" {
			problems = append(problems, "production refuses the fixture data provider")
		}
		for name, p := range c.Profiles {
			if p.Adapter != "openai" {
				problems = append(problems, fmt.Sprintf("production refuses %s adapter for profile %s", p.Adapter, name))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("config: " + strings.Join(problems, "; "))
	}
	return nil
}

// Synthetic reports whether research output in this deployment is simulated (fixture data or sim/replay models).
func (c *Config) Synthetic() bool {
	if c.DataProvider == "fixture" {
		return true
	}
	for _, p := range c.Profiles {
		if p.Adapter != "openai" {
			return true
		}
	}
	return false
}
