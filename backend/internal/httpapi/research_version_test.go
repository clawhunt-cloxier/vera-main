package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResearchVersionNegotiationUntil04IsImplemented(t *testing.T) {
	h := researchVersionGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		name, path, accept string
		status             int
	}{
		{"default legacy", "/api/v1/research-plans", "", 200},
		{"browser wildcard", "/api/v1/research-tasks/123", "*/*", 200},
		{"explicit legacy", "/api/v1/research-plans", "application/json", 200},
		{"legacy fallback", "/api/v1/research-tasks", "application/vnd.vera.v0.4+json, application/json;q=0.5", 200},
		{"new only", "/api/v1/research-plans", "application/vnd.vera.v0.4+json", 406},
		{"legacy explicitly excluded", "/api/v1/research-tasks", "application/json;q=0, */*;q=1", 406},
		{"bad quality", "/api/v1/research-tasks", "application/json;q=NaN", 406},
		{"unrelated endpoint", "/api/v1/reports/1/pdf", "application/pdf", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if researchVersionPath(tc.path) && !strings.Contains(w.Header().Get("Vary"), "Accept") {
				t.Fatal("missing Vary: Accept")
			}
			if tc.status == 200 && w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("legacy response did not identify its representation")
			}
		})
	}
}
