package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func syntheticEmquantEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://example", "EMQUANT_LIBRARY": "/synthetic/missing.so", "EMQUANT_USER": "synthetic-user", "EMQUANT_PASSWORD": "synthetic-password",
		"EMQUANT_ACCOUNT_SCOPE": "synthetic-scope", "EMQUANT_S3_ENDPOINT": "http://127.0.0.1:9000", "EMQUANT_S3_REGION": "test",
		"EMQUANT_S3_ACCESS_KEY": "synthetic-access", "EMQUANT_S3_SECRET_KEY": "synthetic-secret", "EMQUANT_S3_BUCKET": "synthetic-bucket",
		"EMQUANT_PROVIDER_ID": "synthetic-provider", "EMQUANT_PRODUCT": "synthetic-product", "VERA_CONTRACTS_DIR": filepath.Join(root, "contracts"),
		"EMQUANT_MIN_INTERVAL": "100ms", "EMQUANT_RATE_COOLDOWN": "1s",
	}
}

func TestEmquantCommandConfigAndAudit(t *testing.T) {
	env := syntheticEmquantEnv()
	get := func(k string) string { return env[k] }
	c, err := readEmquantEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprintf("%v %#v", c, c); strings.Contains(s, "synthetic-password") || strings.Contains(s, "synthetic-secret") {
		t.Fatal("configuration exposed credentials")
	}
	env["EMQUANT_S3_ENDPOINT"] = "https://synthetic-user:synthetic-password@example.invalid"
	if _, err := readEmquantEnv(get); err == nil || strings.Contains(err.Error(), "synthetic-password") {
		t.Fatal("endpoint credentials accepted or leaked")
	}
	env["EMQUANT_S3_ENDPOINT"] = "http://127.0.0.1:9000"
	env["EMQUANT_MIN_INTERVAL"] = "1ms"
	if _, err := readEmquantEnv(get); err == nil {
		t.Fatal("unsafe rate interval accepted")
	}
	env["EMQUANT_MIN_INTERVAL"] = "100ms"
	pool := dbtest.New(t)
	env["DATABASE_URL"] = pool.Config().ConnString()
	var out bytes.Buffer
	args := []string{"quote", "--entity", uuid.NewString(), "--code", "A", "--license", uuid.NewString(), "--policy", uuid.NewString()}
	err = emquantCommand(context.Background(), args, &out, get)
	if err == nil || out.Len() != 0 {
		t.Fatal("missing SDK was reported as successful ingestion")
	}
	var result, detail string
	if err := pool.QueryRow(context.Background(), `SELECT result,detail::text FROM audit_events WHERE action='emquant.quote.collect'`).Scan(&result, &detail); err != nil || result != "failure" {
		t.Fatalf("missing failure audit: %v", err)
	}
	for _, secret := range []string{env["EMQUANT_PASSWORD"], env["EMQUANT_S3_SECRET_KEY"], env["EMQUANT_USER"]} {
		if strings.Contains(out.String(), secret) || strings.Contains(err.Error(), secret) || strings.Contains(detail, secret) {
			t.Fatal("credentials exposed")
		}
	}
}
