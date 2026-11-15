//go:build linux && cgo

package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

// Explicit opt-in: calls the real Choice SDK, writes durable S3 objects and
// incurs model fees. Use only when no other process uses this vendor account.
func TestLiveObservationBrief(t *testing.T) {
	if os.Getenv("VERA_LIVE_BRIEF") != "1" {
		t.Skip("live brief acceptance not requested")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool := dbtest.New(t)
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Synthetic() || c.DataProvider != "emquant" || !c.RealResearch {
		t.Fatal("live acceptance requires real data and real models")
	}
	evidence, verifiedBy := os.Getenv("EMQUANT_LICENSE_EVIDENCE"), os.Getenv("EMQUANT_LICENSE_VERIFIED_BY")
	until, err := time.Parse(time.RFC3339, os.Getenv("EMQUANT_LICENSE_VALID_UNTIL"))
	if err != nil || !until.After(time.Now()) || evidence == "" || verifiedBy == "" {
		t.Fatal("explicit license evidence and expiry required")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES($1,'Choice','vendor',true)`, os.Getenv("EMQUANT_PROVIDER_ID")); err != nil {
		t.Fatal(err)
	}
	code := os.Getenv("VERA_LIVE_BRIEF_CODE")
	if code == "" {
		t.Fatal("single live security must be explicit")
	}
	for _, x := range []struct {
		key, dataset, method string
		fields               []string
	}{
		{"EMQUANT_SECURITY_LICENSE", "ref.security", "CSS", []string{"NAME", "LISTDATE", "DELISTDATE"}},
		{"EMQUANT_LIQUIDITY_LICENSE", "mkt.daily_bar", "CSD", []string{"VOLUME", "AMOUNT", "TURN"}},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO source_licenses(id,provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,valid_until,allow_storage,allow_display,allow_model_processing) VALUES($1,$2,$3,$4,$5,$6,'listed',$7,$8,$9,now(),now(),$10,true,true,true)`, os.Getenv(x.key), os.Getenv("EMQUANT_PROVIDER_ID"), os.Getenv("EMQUANT_PRODUCT"), x.dataset, x.method, x.fields, []string{code}, evidence, verifiedBy, until); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{Cfg: c, Pool: pool}
	if err := a.build(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Brief.Runtime.Close()
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Engine.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}
	var user, ws uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('live-acceptance','operator') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, user).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	sub, err := a.Store.EntityByCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.Engine.BuildPlan(ctx, ws, "stock_analysis", sub, map[string]any{})
	if err != nil || !plan.Executable {
		t.Fatalf("plan unavailable %v", err)
	}
	id, err := a.Engine.CreateTask(ctx, ws, plan.ID, plan.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Engine.Run(ctx, ws, id, 1); err != nil {
		t.Fatal(err)
	}
	var body []byte
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM research_tasks WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" {
		var reason *string
		_ = pool.QueryRow(ctx, `SELECT reason_message FROM research_tasks WHERE id=$1`, id).Scan(&reason)
		t.Fatalf("live task %s: %v", status, reason)
	}
	if err := pool.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var doc harness.Document
	if err := json.Unmarshal(body, &doc); err != nil || doc.Synthetic || len(doc.Claims) == 0 {
		t.Fatal("invalid live document")
	}
	dest := os.Getenv("VERA_LIVE_BRIEF_OUTPUT")
	if dest != "" {
		if err := os.WriteFile(dest, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var calls int
	var models []string
	_ = pool.QueryRow(ctx, `SELECT count(*),array_agg(DISTINCT model_id) FROM model_calls WHERE task_id=$1`, id).Scan(&calls, &models)
	t.Logf("real brief delivered: task=%s claims=%d model_calls=%d models=%v", id, len(doc.Claims), calls, models)
}
