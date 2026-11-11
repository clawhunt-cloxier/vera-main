package config

import (
	"strings"
	"testing"
)

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
