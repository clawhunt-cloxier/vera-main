//go:build linux && cgo

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestEmquantCommandPublishesLicensedQuote(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic) VALUES('cli-test','synthetic test provider','vendor',false)`); err != nil {
		t.Fatal(err)
	}
	var entity, license, policy uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO entities(kind,code,name,synthetic) VALUES('stock','A','synthetic',false) RETURNING id`).Scan(&entity); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO source_licenses(provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,allow_storage)
		VALUES('cli-test','synthetic-product','mkt.intraday_quote','CSQSNAPSHOT',ARRAY['TIME','NOW'],'listed',ARRAY['A'],'synthetic-evidence','synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute',true) RETURNING id`).Scan(&license); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO quote_mapping_policies(source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,price_unit,price_evidence_ref,latency_mode,max_age_ms,verified_by,verified_at,valid_from)
		VALUES($1,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai','synthetic-time','CNY','synthetic-price','unknown',60000,'synthetic-operator',clock_timestamp(),clock_timestamp()-interval '1 minute') RETURNING id`, license).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, exists := objects[r.URL.Path]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case http.MethodGet:
			body, exists := objects[r.URL.Path]
			if !exists {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	src, err := os.ReadFile("../../internal/dataservice/emquant/testdata/fake.c")
	if err != nil {
		t.Fatal(err)
	}
	const oldQuote = `int csqsnapshot(const char *codes, const char *indicators, const char *options, eqdata **out) {
 return csd(codes, indicators, "", "", options, out);
}`
	const quote = `int csqsnapshot(const char *codes, const char *indicators, const char *options, eqdata **out) {
 (void)options;
 if (strcmp(codes,"A") || strcmp(indicators,"TIME,NOW")) return 88;
 time_t now=time(NULL)+28800; struct tm utc; gmtime_r(&now,&utc);
 static char day[11]; strftime(day,sizeof(day),"%Y-%m-%d",&utc);
 static eqchar code[]={{"A",2}}, fields[]={{"TIME",5},{"NOW",4}}, dates[1];
 static eqvariant values[2]; static eqdata data;
 dates[0]=(eqchar){day,11}; values[0]=(eqvariant){.type=6,.value.i=utc.tm_hour*10000+utc.tm_min*100+utc.tm_sec};
 values[1]=(eqvariant){.type=11,.value.d=10.25}; data=(eqdata){{code,1},{fields,2},{dates,1},{values,2}}; *out=&data; return 0;
}`
	if !strings.Contains(string(src), oldQuote) {
		t.Fatal("synthetic SDK fixture changed")
	}
	program := "#include <time.h>\n" + strings.Replace(string(src), oldQuote, quote, 1)
	dir := t.TempDir()
	cfile, lib := filepath.Join(dir, "fake.c"), filepath.Join(dir, "fake.so")
	if err := os.WriteFile(cfile, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cc", "-shared", "-fPIC", "-o", lib, cfile).CombinedOutput(); err != nil {
		t.Fatalf("compile synthetic SDK: %v: %s", err, output)
	}
	env := syntheticEmquantEnv()
	env["DATABASE_URL"], env["EMQUANT_LIBRARY"], env["EMQUANT_USER"], env["EMQUANT_PASSWORD"] = pool.Config().ConnString(), lib, "user", "pass"
	env["EMQUANT_S3_ENDPOINT"], env["EMQUANT_PROVIDER_ID"] = server.URL, "cli-test"
	var out bytes.Buffer
	err = emquantCommand(ctx, []string{"quote", "--entity", entity.String(), "--code", "A", "--license", license.String(), "--policy", policy.String()}, &out, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("synthetic end-to-end ingestion: %v", err)
	}
	var id string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "candidate_id=") {
			id = strings.TrimPrefix(line, "candidate_id=")
		}
	}
	stage, err := uuid.Parse(id)
	if err != nil || !strings.Contains(out.String(), "available_at=") {
		t.Fatal("missing publication identity or availability")
	}
	var savedLicense uuid.UUID
	var price string
	var fields []string
	var validOrder bool
	if err := pool.QueryRow(ctx, `SELECT o.source_license_id,s.payload->>'last_price',o.vendor_fields,
		s.event_at<=s.received_at AND p.available_at>=o.archived_at
		FROM quote_publications p JOIN staged_quotes s ON s.id=p.staged_quote_id JOIN raw_objects o ON o.id=s.raw_object_id WHERE p.staged_quote_id=$1`, stage).Scan(&savedLicense, &price, &fields, &validOrder); err != nil || savedLicense != license || price != "10.25" || len(fields) != 2 || fields[0] != "TIME" || fields[1] != "NOW" || !validOrder {
		t.Fatalf("missing licensed publication: %v", err)
	}
	var auditResult string
	if err := pool.QueryRow(ctx, `SELECT result FROM audit_events WHERE action='emquant.quote.collect'`).Scan(&auditResult); err != nil || auditResult != "success" {
		t.Fatalf("missing success audit: %v", err)
	}
	for _, key := range []string{"EMQUANT_LIBRARY", "EMQUANT_USER", "EMQUANT_PASSWORD", "EMQUANT_ACCOUNT_SCOPE", "EMQUANT_MIN_INTERVAL", "EMQUANT_RATE_COOLDOWN"} {
		delete(env, key)
	}
	var recovered bytes.Buffer
	if err := emquantCommand(ctx, []string{"publish", "--candidate", stage.String()}, &recovered, func(k string) string { return env[k] }); err != nil || recovered.String() != out.String() {
		t.Fatalf("publication recovery changed its result or required SDK credentials: %v", err)
	}
	var publications int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM quote_publications WHERE staged_quote_id=$1`, stage).Scan(&publications); err != nil || publications != 1 {
		t.Fatalf("publication retry was not idempotent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE source_licenses SET revoked_at=clock_timestamp() WHERE id=$1`, license); err != nil {
		t.Fatal(err)
	}
	recovered.Reset()
	if err := emquantCommand(ctx, []string{"publish", "--candidate", stage.String()}, &recovered, func(k string) string { return env[k] }); err == nil || recovered.Len() != 0 {
		t.Fatal("revoked source was recovered as a successful publication")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='emquant.quote.publish' AND result='failure'`).Scan(&publications); err != nil || publications != 1 {
		t.Fatalf("missing failed recovery audit: %v", err)
	}
}
