package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func quoteInitEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"DATABASE_URL":                      "",
		"EMQUANT_PROVIDER_ID":               "choice-production",
		"EMQUANT_PRODUCT":                   "choice-realtime",
		"EMQUANT_QUOTE_FIELDS":              "TIME,NOW,TRADE_STATUS",
		"EMQUANT_QUOTE_STATUS_INDICATOR":    "TRADE_STATUS",
		"EMQUANT_QUOTE_STATUS_EVIDENCE":     "choice-terminal-status-check-2026-11-18",
		"EMQUANT_QUOTE_STATUS_MAPPING":      `{"s:OPEN":"continuous","s:CLOSED":"closed"}`,
		"EMQUANT_QUOTE_TIME_EVIDENCE":       "choice-manual-time-check-2026-11-18",
		"EMQUANT_QUOTE_PRICE_EVIDENCE":      "choice-manual-price-check-2026-11-18",
		"EMQUANT_QUOTE_LATENCY_MODE":        "real_time",
		"EMQUANT_QUOTE_LATENCY_EVIDENCE":    "choice-live-latency-check-2026-11-18",
		"EMQUANT_QUOTE_MAX_AGE_MS":          "60000",
		"EMQUANT_QUOTE_LICENSE_EVIDENCE":    "choice-contract-record-2026-11-18",
		"EMQUANT_QUOTE_LICENSE_VERIFIED_BY": "operator-placeholder",
		"EMQUANT_QUOTE_LICENSE_VALID_UNTIL": "2099-01-01T00:00:00Z",
	}
}

func TestReadQuoteInitConfigRequiresRealtimeProof(t *testing.T) {
	env := quoteInitEnv(t)
	env["EMQUANT_QUOTE_FIELDS"] = "TIME,NOW"
	env["EMQUANT_QUOTE_STATUS_INDICATOR"] = ""
	env["EMQUANT_QUOTE_STATUS_EVIDENCE"] = ""
	env["EMQUANT_QUOTE_STATUS_MAPPING"] = ""
	_, err := readQuoteInitConfig(func(key string) string { return env[key] })
	if err == nil || !strings.Contains(err.Error(), "continuous mapping") {
		t.Fatalf("expected realtime proof rejection, got %v", err)
	}
}

func TestReadQuoteInitConfigAllowsUnknownLatestAvailable(t *testing.T) {
	env := quoteInitEnv(t)
	env["EMQUANT_QUOTE_FIELDS"] = "TIME,NOW"
	env["EMQUANT_QUOTE_STATUS_INDICATOR"] = ""
	env["EMQUANT_QUOTE_STATUS_EVIDENCE"] = ""
	env["EMQUANT_QUOTE_STATUS_MAPPING"] = ""
	delete(env, "EMQUANT_QUOTE_LATENCY_MODE")
	env["EMQUANT_QUOTE_LATENCY_EVIDENCE"] = ""
	config, err := readQuoteInitConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("unknown latency should be usable for latest_available: %v", err)
	}
	if config.LatencyMode != "unknown" || config.StatusIndicator != "" {
		t.Fatalf("unexpected unknown policy: %+v", config)
	}
}

func TestEmquantInitQuoteRegistersIdempotently(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	env := quoteInitEnv(t)
	env["DATABASE_URL"] = pool.Config().ConnString()
	var first bytes.Buffer
	if err := emquantInitQuote(ctx, &first, func(key string) string { return env[key] }); err != nil {
		t.Fatalf("initial quote registration: %v", err)
	}
	if !strings.Contains(first.String(), "latency_mode=real_time") {
		t.Fatalf("missing registration output: %s", first.String())
	}
	var licenses, policies, scoped int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_licenses WHERE provider_id=$1 AND product_code=$2 AND dataset_id='mkt.intraday_quote'`, env["EMQUANT_PROVIDER_ID"], env["EMQUANT_PRODUCT"]).Scan(&licenses); err != nil || licenses != 1 {
		t.Fatalf("license registration count=%d err=%v", licenses, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_licenses WHERE provider_id=$1 AND product_code=$2 AND instrument_scope='all' AND cardinality(instrument_codes)=0`, env["EMQUANT_PROVIDER_ID"], env["EMQUANT_PRODUCT"]).Scan(&scoped); err != nil || scoped != 1 {
		t.Fatalf("license must authorize all stocks: count=%d err=%v", scoped, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_mapping_policies q JOIN source_licenses l ON l.id=q.source_license_id WHERE l.provider_id=$1 AND l.product_code=$2`, env["EMQUANT_PROVIDER_ID"], env["EMQUANT_PRODUCT"]).Scan(&policies); err != nil || policies != 1 {
		t.Fatalf("policy registration count=%d err=%v", policies, err)
	}
	var second bytes.Buffer
	if err := emquantInitQuote(ctx, &second, func(key string) string { return env[key] }); err != nil {
		t.Fatalf("idempotent quote registration: %v", err)
	}
	env["EMQUANT_PRODUCT"] = "different-product"
	if err := emquantInitQuote(ctx, &bytes.Buffer{}, func(key string) string { return env[key] }); err != nil {
		t.Fatalf("new product should receive a new governed identity: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_licenses WHERE provider_id=$1`, env["EMQUANT_PROVIDER_ID"]).Scan(&licenses); err != nil || licenses != 2 {
		t.Fatalf("expected one all-scope grant per reviewed product, count=%d err=%v", licenses, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='emquant.quote.init'`).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatalf("expected three audited attempts, count=%d err=%v", auditCount, err)
	}
}

func TestQuoteStatusSeparatesDatabaseFromRegistrationInputs(t *testing.T) {
	ctx := t.Context()
	pool := dbtest.New(t)
	env := quoteInitEnv(t)
	env["DATABASE_URL"] = pool.Config().ConnString()
	get := func(key string) string { return env[key] }
	check := func(ready bool, inputs string) {
		t.Helper()
		var out bytes.Buffer
		if err := emquantCommand(ctx, []string{"quote-status"}, &out, get); err != nil {
			t.Fatal(err)
		}
		var status quoteStatus
		if err := json.Unmarshal(out.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.Latest != ready || status.Realtime != ready || status.Inputs != inputs || strings.Contains(out.String(), env["EMQUANT_QUOTE_LICENSE_EVIDENCE"]) {
			t.Fatalf("unexpected or unsafe status")
		}
		if inputs == "incomplete" && (len(status.Missing) != 1 || status.Missing[0] != "EMQUANT_QUOTE_TIME_EVIDENCE") {
			t.Fatal("missing input was not identified")
		}
	}
	check(false, "valid")
	if err := emquantInitQuote(ctx, &bytes.Buffer{}, get); err != nil {
		t.Fatal(err)
	}
	delete(env, "EMQUANT_QUOTE_TIME_EVIDENCE")
	check(true, "incomplete")
}

func TestQuoteDatabaseSelection(t *testing.T) {
	env := map[string]string{"VERA_ENV": "test", "VERA_DEMO_PG_PORT": "55445"}
	get := func(k string) string { return env[k] }
	if _, err := quoteDatabaseEnv(nil, get); err == nil {
		t.Fatal("implicit fallback")
	}
	selected, err := quoteDatabaseEnv([]string{"--demo-db"}, get)
	if err != nil || selected("DATABASE_URL") != demoDatabaseURL(55445) {
		t.Fatal("wrong embedded DB")
	}
	env["DATABASE_URL"] = "configured-database"
	selected, err = quoteDatabaseEnv([]string{"--demo-db"}, get)
	if err != nil || selected("DATABASE_URL") != env["DATABASE_URL"] {
		t.Fatal("explicit database lost")
	}
	env["VERA_ENV"] = "production"
	if _, err := quoteDatabaseEnv([]string{"--demo-db"}, get); err == nil {
		t.Fatal("demo flag in production")
	}
}
