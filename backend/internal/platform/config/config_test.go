package config

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRealtimeSourceConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_DEV_OIDC", "true")
	t.Setenv("VERA_DATA_PROVIDER", "emquant")
	t.Setenv("EMQUANT_QUOTE_LICENSE_ID", "")
	t.Setenv("EMQUANT_QUOTE_POLICY_ID", "")
	c, err := Load()
	if err != nil || c.QuoteLicenseID != uuid.Nil || c.QuotePolicyID != uuid.Nil {
		t.Fatalf("unset quote config should keep observation mode: %v", err)
	}
	t.Setenv("EMQUANT_QUOTE_LICENSE_ID", "11111111-1111-1111-1111-111111111111")
	if _, err := Load(); err == nil {
		t.Fatal("partial quote source must fail")
	}
	t.Setenv("EMQUANT_QUOTE_POLICY_ID", "00000000-0000-0000-0000-000000000000")
	if _, err := Load(); err == nil {
		t.Fatal("zero policy must fail")
	}
	t.Setenv("EMQUANT_QUOTE_POLICY_ID", "22222222-2222-2222-2222-222222222222")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERA_DATA_PROVIDER", "fixture")
	if _, err := Load(); err == nil {
		t.Fatal("fixture must not enable a live quote source")
	}
}

func TestProductionRefusesFixtureAndSimulatedModels(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_DEV_OIDC", "true")
	t.Setenv("VERA_ENV", "production")
	if _, err := Load(); err == nil {
		t.Fatal("production with fixture provider and sim models must fail")
	}
	t.Setenv("VERA_ENV", "development")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Synthetic() {
		t.Fatal("fixture + sim must be reported synthetic")
	}
}

func TestOpenAIProfileRequiresPriceTable(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_DEV_OIDC", "true")
	t.Setenv("MODEL_WRITER_ADAPTER", "openai")
	t.Setenv("MODEL_WRITER_BASE_URL", "https://api.example")
	t.Setenv("MODEL_WRITER_ID", "m")
	t.Setenv("MODEL_WRITER_API_KEY", "k")
	if _, err := Load(); err == nil {
		t.Fatal("missing price table must fail")
	}
}

func TestDevOIDCRefusedInProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_ENV", "production")
	t.Setenv("VERA_DEV_OIDC", "true")
	t.Setenv("VERA_DATA_PROVIDER", "vendor-x")
	for _, p := range []string{"WRITER", "VERIFIER", "ROUTER"} {
		t.Setenv("MODEL_"+p+"_ADAPTER", "openai")
		t.Setenv("MODEL_"+p+"_BASE_URL", "https://api.example")
		t.Setenv("MODEL_"+p+"_ID", "m")
		t.Setenv("MODEL_"+p+"_API_KEY", "k")
	}
	t.Setenv("MODEL_PRICE_TABLE_VERSION", "v1")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "VERA_DEV_OIDC") {
		t.Fatalf("want dev oidc refusal, got %v", err)
	}
	t.Setenv("VERA_DEV_OIDC", "false")
	t.Setenv("OIDC_ISSUER", "https://id.example/realms/vera")
	t.Setenv("OIDC_CLIENT_ID", "vera-web")
	t.Setenv("OIDC_CLIENT_SECRET", "s")
	if _, err := Load(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestProfileSwitches(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_DEV_OIDC", "true")
	t.Setenv("MODEL_WRITER_THINKING", "off")
	t.Setenv("MODEL_WRITER_TEMPERATURE", "0.2")
	t.Setenv("MODEL_WRITER_MAX_OUTPUT_TOKENS", "6000")
	t.Setenv("MODEL_WRITER_TIMEOUT_SECONDS", "60")
	t.Setenv("MODEL_WRITER_CONCURRENCY", "3")
	t.Setenv("MODEL_WRITER_EXTRA_BODY", `{"top_p":0.9}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := c.Profiles["writer"]
	if p.Thinking != "disabled" || p.Temperature != 0.2 || p.MaxOutput != 6000 || p.TimeoutSeconds != 60 || p.Concurrency != 3 || p.ExtraBody["top_p"] != 0.9 {
		t.Fatalf("profile %+v", p)
	}
	if c.Profiles["verifier"].Thinking != "" {
		t.Fatal("unset thinking must stay vendor default")
	}
	for k, v := range map[string]string{
		"MODEL_WRITER_THINKING": "maybe", "MODEL_WRITER_TEMPERATURE": "3", "MODEL_WRITER_MAX_OUTPUT_TOKENS": "-1",
		"MODEL_WRITER_CONCURRENCY": "x", "MODEL_WRITER_EXTRA_BODY": `{"max_tokens":99999}`,
	} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, v)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), strings.TrimPrefix(k, "MODEL_WRITER_")) {
				t.Fatalf("%s=%s accepted: %v", k, v, err)
			}
		})
	}
}

func TestProfileRejectsModelTools(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_DEV_OIDC", "true")
	for _, body := range []string{`{"tools":[{"type":"deepseek_web_search"}]}`, `{"ToOlS":[]}`, `{"tool_choice":"auto"}`, `{"functions":[]}`, `{"web_search_options":{}}`} {
		t.Run(body, func(t *testing.T) {
			t.Setenv("MODEL_WRITER_EXTRA_BODY", body)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "EXTRA_BODY") {
				t.Fatalf("tool configuration was accepted: %v", err)
			}
		})
	}
}

func TestRecordAdapterNeedsRealCredentialsAndIsRefusedInProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("VERA_DEV_OIDC", "true")
	t.Setenv("MODEL_WRITER_ADAPTER", "record")
	if _, err := Load(); err == nil {
		t.Fatal("record without credentials must fail")
	}
	t.Setenv("MODEL_WRITER_BASE_URL", "https://api.example")
	t.Setenv("MODEL_WRITER_ID", "m")
	t.Setenv("MODEL_WRITER_API_KEY", "k")
	t.Setenv("MODEL_PRICE_TABLE_VERSION", "v1")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERA_ENV", "production")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "record adapter") {
		t.Fatalf("production must refuse record: %v", err)
	}
}
