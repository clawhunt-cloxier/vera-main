package config

import "testing"

func TestProductionRefusesFixtureAndSimulatedModels(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
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
	t.Setenv("MODEL_WRITER_ADAPTER", "openai")
	t.Setenv("MODEL_WRITER_BASE_URL", "https://api.example")
	t.Setenv("MODEL_WRITER_ID", "m")
	t.Setenv("MODEL_WRITER_API_KEY", "k")
	if _, err := Load(); err == nil {
		t.Fatal("missing price table must fail")
	}
}
