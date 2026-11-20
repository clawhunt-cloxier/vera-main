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

func TestSkillAvailabilityMatchesResearchProtocol(t *testing.T) {
	pool := dbtest.New(t)
	closeSkills := harness.TuShareSkills()
	for _, tc := range []struct {
		name, media, stockVersion string
		combined, paused          bool
		available                 map[string]bool
	}{
		{
			name: "combined workbench", media: legacyMedia, combined: true,
			stockVersion: closeSkills["stock_analysis"].Version,
			available:    map[string]bool{"stock_analysis": true, "capital_flow": true, "deep_research": true},
		},
		{
			name: "combined observation", media: observationMedia, combined: true,
			stockVersion: emquant.ObservationBriefVersion,
			available:    map[string]bool{"stock_analysis": true},
		},
		{
			name: "combined 0.4", media: realtimeMedia, combined: true,
			stockVersion: harness.RealtimeBriefVersion,
			available:    map[string]bool{"stock_analysis": true},
		},
		{
			name: "combined paused", media: legacyMedia, combined: true, paused: true,
			stockVersion: closeSkills["stock_analysis"].Version,
		},
		{
			name: "Choice only", media: realtimeMedia,
			stockVersion: harness.RealtimeBriefVersion,
			available:    map[string]bool{"stock_analysis": true},
		},
		{
			name: "Choice only paused", media: realtimeMedia, paused: true,
			stockVersion: harness.RealtimeBriefVersion,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
			r.Header.Set("Accept", tc.media)
			if _, err := pool.Exec(r.Context(), `INSERT INTO platform_settings(key,value)
				VALUES('kill_switch', jsonb_build_object('on', $1::boolean))
				ON CONFLICT (key) DO UPDATE SET value=excluded.value`, tc.paused); err != nil {
				t.Fatal(err)
			}
			skills := closeSkills
			if !tc.combined {
				skills = harness.Skills()
				skills["stock_analysis"] = harness.ObservationBriefSkill()
			}
			s := &Server{Pool: pool, FullVendorResearch: tc.combined,
				Engine: &harness.Engine{Brief: &emquant.BriefService{}, Skills: skills}}
			w := httptest.NewRecorder()
			if err := s.skills(w, r); err != nil {
				t.Fatal(err)
			}
			var body struct {
				Items []Skill `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || len(body.Items) != len(skills) {
				t.Fatalf("unexpected catalog response: status %d, %s", w.Code, w.Body.String())
			}
			seen := map[string]bool{}
			for _, sk := range body.Items {
				if skills[sk.ID] == nil || seen[sk.ID] {
					t.Fatalf("unexpected or duplicate skill %s", sk.ID)
				}
				seen[sk.ID] = true
				if sk.Available != tc.available[sk.ID] {
					t.Errorf("%s availability = %t, want %t", sk.ID, sk.Available, tc.available[sk.ID])
				}
				if sk.ID == "stock_analysis" && sk.Version != tc.stockVersion {
					t.Errorf("stock analysis version = %s, want %s", sk.Version, tc.stockVersion)
				}
			}
		})
	}
}
