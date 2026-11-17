package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResearchVersionNegotiation(t *testing.T) {
	legacy := versionGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), false, false)
	for _, tc := range []struct {
		name, accept, content string
		status                int
	}{
		{"default legacy", "", "", 200},
		{"browser wildcard", "*/*", "", 200},
		{"explicit legacy", "application/json", "application/json", 200},
		{"new only unsupported", realtimeMedia, realtimeMedia, 406},
		{"legacy explicitly excluded", "application/json;q=0, */*;q=1", "", 406},
		{"bad quality", "application/json;q=NaN", "", 406},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/research-plans", nil)
			r.Header.Set("Accept", tc.accept)
			if tc.content != "" {
				r.Header.Set("Content-Type", tc.content)
			}
			w := httptest.NewRecorder()
			legacy.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if !strings.Contains(w.Header().Get("Vary"), "Accept") {
				t.Fatal("missing Vary: Accept")
			}
		})
	}
}

func TestRealtimeVersionNegotiationRequiresExplicitRequestMedia(t *testing.T) {
	h := versionGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), true, true)
	for _, tc := range []struct {
		name, accept, content string
		status                int
	}{
		{"explicit v04", realtimeMedia, realtimeMedia, 200},
		{"v04 response with legacy body", realtimeMedia, "application/json", 415},
		{"legacy response", "application/json", "application/json", 200},
		{"observation response", observationMedia, observationMedia, 200},
		{"v04 preferred over observation", realtimeMedia + ", " + observationMedia + ";q=0.9", realtimeMedia, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/research-plans", nil)
			r.Header.Set("Accept", tc.accept)
			r.Header.Set("Content-Type", tc.content)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.status == 200 && !strings.Contains(w.Header().Get("Content-Type"), strings.Split(tc.accept, ",")[0]) {
				t.Fatalf("content type = %q", w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestResearchVersionPathDoesNotGatePDF(t *testing.T) {
	if researchVersionPath("/api/v1/reports/1/pdf") {
		t.Fatal("PDF should negotiate its own media type")
	}
	if !researchVersionPath("/api/v1/reports/1") {
		t.Fatal("report JSON should negotiate")
	}
}

func TestPlanConfirmationCannotSwitchProtocol(t *testing.T) {
	for _, requirement := range []string{"realtime_trade", "latest_available"} {
		if !planConfirmationVersion("collect_latest", &requirement, realtimeMedia) {
			t.Fatal("0.4 plan must accept 0.4 confirmation")
		}
		if planConfirmationVersion("collect_latest", &requirement, observationMedia) || planConfirmationVersion("collect_latest", &requirement, legacyMedia) {
			t.Fatal("0.4 plan accepted an older protocol")
		}
	}
	if planConfirmationVersion("fixed_legacy", nil, realtimeMedia) || planConfirmationVersion("collect_latest", nil, realtimeMedia) {
		t.Fatal("0.4 must not reinterpret historical plan intent")
	}
	if !planConfirmationVersion("collect_latest", nil, observationMedia) || !planConfirmationVersion("fixed_legacy", nil, legacyMedia) {
		t.Fatal("historical confirmations lost their protocol")
	}
}

func TestLegacyRepresentationStripsOnlyVersionedFields(t *testing.T) {
	body := []byte(`{"items":[{"id":"one","snapshot_mode":"collect_latest","snapshot_as_of":null,"quote_requirement":"realtime_trade","revision":9007199254740993}],"next_cursor":null}`)
	filtered := legacyJSON(body, "/api/v1/research-tasks", legacyMedia)
	if strings.Contains(string(filtered), "snapshot_") || strings.Contains(string(filtered), "quote_requirement") {
		t.Fatalf("legacy fields leaked: %s", filtered)
	}
	if !strings.Contains(string(filtered), "9007199254740993") {
		t.Fatal("numeric precision changed")
	}
	observation := legacyJSON(body, "/api/v1/research-tasks", observationMedia)
	if !strings.Contains(string(observation), "snapshot_mode") || strings.Contains(string(observation), "quote_requirement") {
		t.Fatalf("observation representation: %s", observation)
	}
	var value any
	if err := json.Unmarshal(filtered, &value); err != nil {
		t.Fatal(err)
	}
	if got := legacyJSON(body, "/api/v1/research-tasks", realtimeMedia); string(got) != string(body) {
		t.Fatal("0.4 body changed")
	}
}

func TestRepresentationWritesInsideHandler(t *testing.T) {
	inside := false
	spy := &writeScopeRecorder{ResponseRecorder: httptest.NewRecorder(), inside: &inside, t: t}
	h := versionGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inside = true
		defer func() { inside = false }()
		_, _ = w.Write([]byte(`{"id":"one","snapshot_mode":"fixed_legacy","quote_requirement":null}`))
	}), true, true)
	h.ServeHTTP(spy, httptest.NewRequest(http.MethodGet, "/api/v1/research-plans/one", nil))
	if strings.Contains(spy.Body.String(), "snapshot_mode") {
		t.Fatal("legacy field leaked")
	}
}

type writeScopeRecorder struct {
	*httptest.ResponseRecorder
	inside *bool
	t      *testing.T
}

func (w *writeScopeRecorder) Write(body []byte) (int, error) {
	if !*w.inside {
		w.t.Fatal("response flushed outside the authorization scope")
	}
	return w.ResponseRecorder.Write(body)
}
