package emquant

import (
	"context"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestHasAllStockQuoteSourceOnlyMatchesAllScope(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic,allow_model_processing)
		VALUES('choice-source-test','Choice source test','vendor',false,true)`); err != nil {
		t.Fatal(err)
	}
	insert := func(scope string, codes []string) {
		t.Helper()
		var licenseID string
		if err := pool.QueryRow(ctx, `INSERT INTO source_licenses
			(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage,allow_display,allow_model_processing)
			VALUES('choice-source-test','choice-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW'],$1,$2,'test-evidence','test-operator',clock_timestamp(),clock_timestamp()-interval '1 minute',true,true,true)
			RETURNING id`, scope, codes).Scan(&licenseID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO quote_mapping_policies
			(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
			VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','test-time','CNY','test-price','unknown',60000,'test-operator',clock_timestamp(),clock_timestamp()-interval '1 minute')`, licenseID); err != nil {
			t.Fatal(err)
		}
	}

	insert("listed", []string{"600000.SH"})
	available, err := AllStockQuoteCapabilities(ctx, pool, "choice-source-test", "choice-product")
	if err != nil {
		t.Fatal(err)
	}
	if available.Latest || available.Realtime {
		t.Fatal("listed-only grant must not satisfy all-stock readiness")
	}
	insert("all", []string{})
	available, err = AllStockQuoteCapabilities(ctx, pool, "choice-source-test", "choice-product")
	if err != nil {
		t.Fatal(err)
	}
	if !available.Latest || available.Realtime {
		t.Fatal("all-scope unknown grant must enable latest only")
	}
}
