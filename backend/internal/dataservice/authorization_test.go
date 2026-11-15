package dataservice_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestSourceUseRequiresCurrentScopedGrant(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES('license-test','Synthetic license test','vendor',true)`); err != nil {
		t.Fatal(err)
	}
	var licenseID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses
		(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,valid_until,allow_storage,allow_display,allow_model_processing)
		VALUES ('license-test','synthetic-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['NOW','TIME'],'listed',ARRAY['A.SZ'],
		'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 day',clock_timestamp()+interval '1 day',true,true,true)
		RETURNING id`).Scan(&licenseID); err != nil {
		t.Fatal(err)
	}
	base := dataservice.SourceUseRequest{LicenseID: licenseID, ProviderID: "license-test", Product: "synthetic-product", DatasetID: "mkt.intraday_quote", Method: "CSQSNAPSHOT", Fields: []string{"now", "TIME"}, Codes: []string{"a.sz"}, Use: dataservice.SourceStorage}
	check := func(req dataservice.SourceUseRequest) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		return dataservice.RequireSourceUse(ctx, tx, req)
	}
	for _, use := range []dataservice.SourceUse{dataservice.SourceStorage, dataservice.SourceDisplay, dataservice.SourceModel} {
		req := base
		req.Use = use
		if err := check(req); err != nil {
			t.Fatalf("granted %s rejected: %v", use, err)
		}
	}
	for _, mutate := range []func(*dataservice.SourceUseRequest){
		func(r *dataservice.SourceUseRequest) { r.Product = "another-product" },
		func(r *dataservice.SourceUseRequest) { r.ProviderID = "another-provider" },
		func(r *dataservice.SourceUseRequest) { r.DatasetID = "mkt.daily_bar" },
		func(r *dataservice.SourceUseRequest) { r.Method = "CSD" },
		func(r *dataservice.SourceUseRequest) { r.Fields = []string{"TIME", "UNKNOWN"} },
		func(r *dataservice.SourceUseRequest) { r.Fields = []string{"NOW", "now"} },
		func(r *dataservice.SourceUseRequest) { r.Codes = []string{"OTHER.SZ"} },
		func(r *dataservice.SourceUseRequest) { r.Codes = []string{"A.SZ", "a.sz"} },
		func(r *dataservice.SourceUseRequest) { r.LicenseID = uuid.Nil },
		func(r *dataservice.SourceUseRequest) { r.Use = "arbitrary" },
	} {
		req := base
		mutate(&req)
		if err := check(req); err == nil {
			t.Fatal("mismatched or invalid source scope was authorized")
		}
	}
	var broadID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses
		(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage)
		VALUES ('license-test','synthetic-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['NOW','TIME'],'all','{}',
		'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 day',true) RETURNING id`).Scan(&broadID); err != nil {
		t.Fatal(err)
	}
	broad := base
	broad.LicenseID = broadID
	broad.Codes = []string{"OTHER.SZ"}
	if err := check(broad); err != nil {
		t.Fatalf("explicit all-instrument grant rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id='license-test'`); err != nil {
		t.Fatal(err)
	}
	model := base
	model.Use = dataservice.SourceModel
	if err := check(model); err == nil {
		t.Fatal("provider-level model denial was bypassed")
	}
	if err := check(base); err != nil {
		t.Fatalf("provider model denial incorrectly removed storage permission: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET field_names=ARRAY['NOW','TIME','OTHER'] WHERE id=$1`, licenseID); err == nil {
		t.Fatal("active license scope was silently rewritten")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM source_licenses WHERE id=$1`, licenseID); err == nil {
		t.Fatal("active license evidence was deleted")
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, licenseID); err != nil {
		t.Fatal(err)
	}
	if err := check(base); err == nil {
		t.Fatal("revoked grant was accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=NULL WHERE id=$1`, licenseID); err == nil {
		t.Fatal("revoked grant was silently restored")
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET field_names=ARRAY['NOW','TIME','OTHER'] WHERE id=$1`, licenseID); err == nil {
		t.Fatal("license scope was silently rewritten")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM source_licenses WHERE id=$1`, licenseID); err == nil {
		t.Fatal("license evidence was deleted")
	}
	var canInsert, canUpdate, canDelete bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('vera_app','source_licenses','INSERT'), has_table_privilege('vera_app','source_licenses','UPDATE'), has_table_privilege('vera_app','source_licenses','DELETE')`).Scan(&canInsert, &canUpdate, &canDelete); err != nil || canInsert || canUpdate || canDelete {
		t.Fatalf("workspace role can change licenses: insert=%v update=%v delete=%v err=%v", canInsert, canUpdate, canDelete, err)
	}
}

func TestSourceUseRechecksExpiryAfterWaitingForLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind) VALUES('expiry-test','Synthetic expiry test','vendor')`); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses
		(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,valid_until,allow_storage)
		VALUES ('expiry-test','synthetic-product','mkt.daily_bar','CSD',ARRAY['X'],'listed',ARRAY['A'],
		'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 day',clock_timestamp()+interval '1 second',true)
		RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(ctx, `SELECT id FROM source_licenses WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	pidCh := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback(context.Background())
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			done <- err
			return
		}
		pidCh <- pid
		done <- dataservice.RequireSourceUse(ctx, tx, dataservice.SourceUseRequest{LicenseID: id, ProviderID: "expiry-test", Product: "synthetic-product", DatasetID: "mkt.daily_bar", Method: "CSD", Fields: []string{"X"}, Codes: []string{"A"}, Use: dataservice.SourceStorage})
	}()
	var pid int
	select {
	case pid = <-pidCh:
	case err := <-done:
		t.Fatalf("authorization did not start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	blocked, expired := false, false
	for !blocked || !expired {
		if err := pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0, clock_timestamp() >= (SELECT valid_until FROM source_licenses WHERE id=$2)`, pid, id).Scan(&blocked, &expired); err != nil {
			t.Fatal(err)
		}
		if blocked && expired {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("authorization did not wait for the lock: %v", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("grant expired while waiting but was authorized")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
