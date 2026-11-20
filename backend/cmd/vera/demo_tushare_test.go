package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestDemoTuShareReadiness(t *testing.T) {
	ctx := t.Context()
	pool := dbtest.New(t)
	fail := make(chan struct{})
	sync := startDemoTuShareSync(ctx, pool, []string{"600000.SH", "000001.SZ"}, "", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-fail:
			return errors.New("simulated upstream failure")
		}
	})
	defer sync.stop()
	h := sync.readiness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	check := func(path string, status int, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status || w.Body.String() != body {
			t.Fatalf("%s: HTTP %d, %s", path, w.Code, w.Body.String())
		}
	}
	const waiting = "{\"status\":\"not_ready\",\"reason\":\"tushare_initial_sync\"}\n"
	const failed = "{\"status\":\"not_ready\",\"reason\":\"tushare_sync_failed\"}\n"
	check("/readyz", 503, waiting)
	check("/healthz", 200, "")
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind) VALUES ('choice','Choice','vendor'),('tushare','TuShare','vendor');
		INSERT INTO entities(kind,code,name) VALUES ('stock','600000.SH','demo A'),('stock','000001.SZ','demo B')`); err != nil {
		t.Fatal(err)
	}
	record := func(code, provider, dataset, quality string) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO records(dataset_id,entity_id,record_key,logical_key,available_at,provider_id,origin,source_tier,payload,content_hash,quality_state,raw_ref,raw_sha256,raw_locator)
			SELECT $3,id, $2 || code || $3 || $4,'demo',now(),$2,'exchange','L2','{}','demo',$4,'demo',repeat('a',64),'row'
			FROM entities WHERE code=$1`, code, provider, dataset, quality)
		if err != nil {
			t.Fatal(err)
		}
	}
	record("600000.SH", "choice", "mkt.daily_bar", "PASS")
	record("000001.SZ", "choice", "mkt.daily_bar", "PASS")
	check("/readyz", 503, waiting)
	record("600000.SH", "tushare", "mkt.daily_bar", "PASS")
	record("000001.SZ", "tushare", "mkt.valuation_daily", "PASS") // peer publication
	record("000001.SZ", "tushare", "mkt.daily_bar", "QUARANTINED")
	check("/readyz", 503, waiting)
	close(fail)
	<-sync.done
	check("/readyz", 503, failed)
	// An explicit retry against the running demo database can restore health
	// without a process restart. Subsequent refresh failures keep these rows.
	record("000001.SZ", "tushare", "mkt.daily_bar", "PASS")
	check("/readyz", 200, "")
	// In combined mode, warm TuShare rows do not prove the configured SDK
	// identity is enabled. Other provider rows may remain in the same database.
	sync.choiceID = "configured-choice"
	const choiceUnavailable = "{\"status\":\"not_ready\",\"reason\":\"choice_provider_unavailable\"}\n"
	check("/readyz", 503, choiceUnavailable)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,enabled) VALUES ('configured-choice','Choice','vendor',false)`); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 503, choiceUnavailable)
	if _, err := pool.Exec(ctx, `UPDATE providers SET enabled=true WHERE id='configured-choice'`); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 200, "")
}

func TestDemoTuShareStopWaitsForCollector(t *testing.T) {
	cancelled := make(chan struct{})
	release := make(chan struct{})
	sync := startDemoTuShareSync(t.Context(), nil, nil, "", func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	})
	stopped := make(chan struct{})
	go func() {
		sync.stop()
		close(stopped)
	}()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel the collector")
	}
	select {
	case <-stopped:
		t.Fatal("returned before collector finished")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not join the collector")
	}
}
