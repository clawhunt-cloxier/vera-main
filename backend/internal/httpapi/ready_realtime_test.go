package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
)

func TestReadyRejectsUnregisteredRealtimeSource(t *testing.T) {
	e := setupEngine(t, func(engine *harness.Engine) {
		engine.RealtimeSource = &harness.BriefQuoteSource{
			ProviderID: "choice",
			Product:    "choice-realtime",
		}
	})
	resp, err := http.Get(e.srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
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
