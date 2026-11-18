package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestReadyRejectsUnregisteredRealtimeSource(t *testing.T) {
	e := setupEngine(t, func(engine *harness.Engine) {
		engine.RealtimeSource = &harness.BriefQuoteSource{
			ProviderID: "choice",
			Product:    "choice-realtime",
		}
	})
	resp := checkReady(e, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready status=%d, want 503", resp.StatusCode)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Reason != "realtime_source_unavailable" {
		t.Fatalf("ready reason=%q", body.Reason)
	}
}

func TestReadyRejectsMissingAllScopeRealtimeGrant(t *testing.T) {
	e := setupEngine(t, func(engine *harness.Engine) {
		engine.Brief = &emquant.BriefService{
			Runtime:    &emquant.Runtime{},
			ProviderID: "choice",
			Product:    "choice-realtime",
		}
		engine.RealtimeSource = &harness.BriefQuoteSource{
			ProviderID: "choice",
			Product:    "choice-realtime",
		}
	})
	normal := checkReady(e, false)
	defer normal.Body.Close()
	if normal.StatusCode != http.StatusOK {
		t.Fatalf("ready status before registration=%d, want 200", normal.StatusCode)
	}
	resp := checkReady(e, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready status=%d, want 503", resp.StatusCode)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Reason != "realtime_source_unavailable" {
		t.Fatalf("ready reason=%q", body.Reason)
	}
}

func TestReadyAcceptsAllScopeRealtimeGrant(t *testing.T) {
	e, _ := realtimeAPIEnv(t)
	resp := checkReady(e, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready status after registration=%d, want 200", resp.StatusCode)
	}
}

func checkReady(e *env, requireQuote bool) *http.Response {
	s := &Server{Pool: e.pool, Engine: e.engine, RequireChoiceQuote: requireQuote}
	w := httptest.NewRecorder()
	s.ready(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return w.Result()
}

func TestReadyBeforeFirstChoiceSearch(t *testing.T) {
	pool := dbtest.New(t)
	if _, err := pool.Exec(t.Context(), `INSERT INTO providers(id,name,kind,synthetic) VALUES('test-choice','Synthetic Choice test','vendor',false)`); err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, engine: &harness.Engine{Brief: &emquant.BriefService{Runtime: &emquant.Runtime{}}}}
	resp := checkReady(e, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty on-demand catalog blocks first search: %d", resp.StatusCode)
	}
}
