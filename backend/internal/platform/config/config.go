// Package config loads environment configuration and enforces production guards (CONVENTIONS §6).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

type ModelProfile struct {
	Name       string
	Adapter    string // openai | sim | replay
	BaseURL    string
	ModelID    string
	APIKey     string
	PriceTable string
	Thinking   bool
}

type Config struct {
	Env              string // development | test | production
	APIAddr          string
	DatabaseURL      string
	PublicURL        string // e.g. http://localhost:8080 ; used for redirects and Origin checks
	CookieSecure     bool
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	PolicyPath       string
	PriceTablePath   string
	ReplayDir        string
	FixtureManifest  string
	DataProvider     string // fixture | <vendor id>
	Registration     bool
	RealResearch     bool
	DailyCostLimit   decimal.Decimal
	Profiles         map[string]ModelProfile
	WorkerQueues     int
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
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
		Env:              env("VERA_ENV", "development"),
		APIAddr:          env("VERA_API_ADDR", "127.0.0.1:8081"),
		DatabaseURL:      env("DATABASE_URL", ""),
		PublicURL:        strings.TrimRight(env("VERA_PUBLIC_URL", "http://localhost:8080"), "/"),
		CookieSecure:     boolEnv("VERA_COOKIE_SECURE", true),
		OIDCIssuer:       env("OIDC_ISSUER", ""),
		OIDCClientID:     env("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: env("OIDC_CLIENT_SECRET", ""),
		PolicyPath:       env("VERA_POLICY", "../contracts/examples/policy.json"),
		PriceTablePath:   env("VERA_PRICE_TABLE", "../deploy/config/prices.json"),
		ReplayDir:        env("VERA_REPLAY_DIR", "../tests/fixtures/replay"),
		FixtureManifest:  env("VERA_FIXTURE_MANIFEST", "../tests/fixtures/demo-universe.json"),
		DataProvider:     env("VERA_DATA_PROVIDER", "fixture"),
		Registration:     boolEnv("VERA_REGISTRATION_ENABLED", false),
		RealResearch:     boolEnv("VERA_REAL_RESEARCH_ENABLED", false),
		Profiles:         map[string]ModelProfile{},
	}
	limit, err := decimal.NewFromString(env("VERA_DAILY_MODEL_COST_LIMIT_USD", "30.00"))
	if err != nil {
		return nil, fmt.Errorf("VERA_DAILY_MODEL_COST_LIMIT_USD: %w", err)
	}
	c.DailyCostLimit = limit
	for _, p := range []string{"writer", "verifier", "router"} {
		u := strings.ToUpper(p)
		c.Profiles[p] = ModelProfile{
			Name:       p,
			Adapter:    env("MODEL_"+u+"_ADAPTER", "sim"),
			BaseURL:    env("MODEL_"+u+"_BASE_URL", ""),
			ModelID:    env("MODEL_"+u+"_ID", ""),
			APIKey:     env("MODEL_"+u+"_API_KEY", ""),
			PriceTable: env("MODEL_PRICE_TABLE_VERSION", ""),
			Thinking:   boolEnv("MODEL_"+u+"_THINKING", false),
		}
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	var problems []string
	switch c.Env {
	case "development", "test", "production":
	default:
		problems = append(problems, "VERA_ENV must be development, test or production")
	}
	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	for name, p := range c.Profiles {
		switch p.Adapter {
		case "openai":
			if p.BaseURL == "" || p.ModelID == "" || p.APIKey == "" || p.PriceTable == "" {
				problems = append(problems, fmt.Sprintf("model profile %s: openai adapter needs base_url, model_id, api_key and price table version", name))
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
