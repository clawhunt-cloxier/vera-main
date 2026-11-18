package harness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

func TestRealtimeBriefPlanBindsRequirements(t *testing.T) {
	subject := dataservice.EntityRow{ID: uuid.New(), Kind: "stock", Code: "600000.SH"}
	preview := time.Date(2026, 11, 13, 6, 0, 0, 123456789, time.UTC)
	plan, err := NewRealtimeBriefPlan(subject, preview, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SkillVersion != BriefDemoVersion || plan.QuoteRequirement != "realtime_trade" || plan.SnapshotMode != "collect_latest" || !plan.PreviewAt.Equal(preview.Truncate(time.Microsecond)) {
		t.Fatal("brief plan lost its confirmed realtime or cutoff semantics")
	}
	if !slices.Contains(plan.Datasets, BriefDataset{ID: "mkt.intraday_quote", Required: true}) || !slices.Contains(plan.Datasets, BriefDataset{ID: "fin.statement", Required: false}) {
		t.Fatal("brief dataset requirements are incorrect")
	}
	cat, err := dataservice.LoadCatalog("../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, dataset := range plan.Datasets {
		if _, ok := cat.Dataset(dataset.ID); !ok {
			t.Fatalf("brief requires unknown dataset %q", dataset.ID)
		}
	}
	hash, err := plan.Hash()
	if err != nil || len(hash) != 64 {
		t.Fatalf("plan hash: %v", err)
	}
	changed := plan
	changed.Datasets = slices.Clone(plan.Datasets)
	changed.Datasets[1].Required = false
	if other, _ := changed.Hash(); other == hash {
		t.Fatal("plan hash did not bind required dataset")
	}
	changed = plan
	changed.QuoteRequirement = "latest_available"
	if other, _ := changed.Hash(); other == hash {
		t.Fatal("plan hash did not bind realtime requirement")
	}
	changed = plan
	changed.PreviewAt = plan.PreviewAt.Add(time.Microsecond)
	if other, _ := changed.Hash(); other == hash {
		t.Fatal("plan hash lost subsecond preview time")
	}
	notReady, err := NewRealtimeBriefPlan(subject, preview, false)
	if err != nil || notReady.Sections[1].Avail != "unavailable" {
		t.Fatalf("unverified realtime capability was treated as available: %v", err)
	}
	if other, _ := notReady.Hash(); other == hash {
		t.Fatal("plan hash did not bind preview availability")
	}
	for _, bad := range []dataservice.EntityRow{{ID: subject.ID, Kind: "stock", Code: "600000.SH", Synthetic: true}, {ID: subject.ID, Kind: "industry", Code: "600000.SH"}, {ID: subject.ID, Kind: "stock", Code: "600000"}} {
		if _, err := NewRealtimeBriefPlan(bad, preview, true); err == nil {
			t.Fatal("invalid or synthetic subject accepted")
		}
	}
}

func TestRegisterBriefIntentBindsConfirmedPlanAtomically(t *testing.T) {
	ctx := context.Background()
	e, pool := testEngine(t)
	ws := workspace(t, pool)
	var entity uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic) VALUES('stock','600000.SH','synthetic test entity',false) RETURNING id`).Scan(&entity); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic,allow_model_processing) VALUES('brief-test','synthetic test provider','vendor',false,true)`); err != nil {
		t.Fatal(err)
	}
	source := BriefQuoteSource{ProviderID: "brief-test", Product: "synthetic-product"}
	var licenseID, policyID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
		VALUES('brief-test','synthetic-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW','TRADESTATUS'],'listed',ARRAY['600000.SH'],'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute',true,true,true) RETURNING id`).Scan(&licenseID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,latency_evidence_ref,max_age_ms,verified_by,verified_at,valid_from,status_indicator,status_evidence_ref,status_mapping)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price','real_time','synthetic-latency',60000,'synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute','TRADESTATUS','synthetic-status','{"s:OPEN":"continuous","s:HALT":"suspended"}') RETURNING id`, licenseID).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	unknownSource := BriefQuoteSource{ProviderID: "brief-test", Product: "unknown-product"}
	var unknownLicense uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
		VALUES('brief-test','unknown-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW'],'listed',ARRAY['600000.SH'],'synthetic-unknown','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute',true,true,true) RETURNING id`).Scan(&unknownLicense); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price','unknown',60000,'synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute')`, unknownLicense); err != nil {
		t.Fatal(err)
	}
	preview := time.Now().UTC().Truncate(time.Microsecond)
	definition, err := NewRealtimeBriefPlan(dataservice.EntityRow{ID: entity, Kind: "stock", Code: "600000.SH"}, preview, true)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := definition.Hash()
	if err != nil {
		t.Fatal(err)
	}
	sections, err := json.Marshal(definition.Sections)
	if err != nil {
		t.Fatal(err)
	}
	var planID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO research_plans(workspace_id,skill_id,skill_version,subject_id,params,sections,as_of,plan_hash,executable,expires_at,snapshot_mode)
		VALUES($1,'stock_analysis',$2,$3,'{}',$4,$5,$6,true,clock_timestamp()+interval '30 minutes','collect_latest') RETURNING id`, ws, BriefDemoVersion, entity, sections, preview, hash).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	create := func(tx pgx.Tx) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO research_tasks(workspace_id,plan_id,skill_id,skill_version,subject_id,params,sections,as_of,status,deadline,budget,snapshot_mode)
			VALUES($1,$2,'stock_analysis',$3,$4,'{}',$5,$6,'QUEUED',clock_timestamp()+interval '5 minutes','{}','collect_latest') RETURNING id`, ws, planID, BriefDemoVersion, entity, sections, preview).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE research_plans SET consumed_task_id=$2 WHERE id=$1`, planID, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack := create(tx)
	if err := RegisterBriefIntent(ctx, tx, ws, rolledBack, 1, strings.Repeat("0", 64), source); err == nil {
		t.Fatal("wrong confirmed hash was accepted")
	}
	_ = tx.Rollback(ctx)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_task_intents WHERE task_id=$1`, rolledBack).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed confirmation left an intent: %v", err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wrongPolicyTask := create(tx)
	if err := RegisterBriefIntent(ctx, tx, ws, wrongPolicyTask, 1, hash, unknownSource); err == nil {
		t.Fatal("unknown latency quote policy was accepted as realtime")
	}
	_ = tx.Rollback(ctx)
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='brief-test'`); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modelDeniedTask := create(tx)
	if err := RegisterBriefIntent(ctx, tx, ws, modelDeniedTask, 1, hash, source); err == nil {
		t.Fatal("model use without permission was accepted")
	}
	_ = tx.Rollback(ctx)
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=true WHERE id='brief-test'`); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := create(tx)
	if err := RegisterBriefIntent(ctx, tx, ws, id, 1, hash, source); err != nil {
		t.Fatal(err)
	}
	if err := RegisterBriefIntent(ctx, tx, ws, id, 1, hash, source); err != nil {
		t.Fatalf("confirmation retry: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var requirement string
	var ids, required []string
	if err := pool.QueryRow(ctx, `SELECT i.requirement,r.dataset_ids,r.required_ids FROM quote_task_intents i JOIN task_snapshot_requirements r USING(workspace_id,task_id,epoch) WHERE i.task_id=$1`, id).Scan(&requirement, &ids, &required); err != nil || requirement != "realtime_trade" || !slices.Equal(ids, []string{"fin.statement", "mkt.intraday_quote", "ref.security"}) || !slices.Equal(required, []string{"mkt.intraday_quote"}) {
		t.Fatalf("confirmed intent or requirements differ: %v", err)
	}
	if err := db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error { return RegisterBriefIntent(ctx, tx, ws, id, 1, hash, source) }); err == nil {
		t.Fatal("workspace role wrote a trusted intent")
	}
	if err := db.InSystem(ctx, pool, func(tx pgx.Tx) error { return RegisterBriefIntent(ctx, tx, uuid.New(), id, 1, hash, source) }); err == nil {
		t.Fatal("confirmation crossed workspaces")
	}
}
