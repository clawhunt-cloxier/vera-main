package httpapi

import (
	"context"
	"maps"
	"net/http"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
)

// Persisted research uses distinct skill versions for each data source. Scope
// both detail and list queries before materializing history or applying LIMIT.
func (s *Server) historyVersions() []string {
	var versions []string
	if s.Engine.Brief != nil {
		versions = []string{emquant.ObservationBriefVersion, harness.RealtimeBriefVersion}
		if !s.FullVendorResearch {
			return versions
		}
		versions = append(versions, harness.MixedObservationVersion)
	}
	if sk := s.Engine.Skills["stock_analysis"]; sk != nil {
		if harness.IsCloseResearchVersion(sk.Version) {
			return append(versions, harness.CloseResearchVersion, harness.CloseCompanyVersion, harness.CloseDisclosureVersion)
		}
		if s.Engine.Synthetic && sk.Version == "1.0.0" {
			return []string{sk.Version}
		}
	}
	return versions
}

type listVersionsKey struct{}

func (s *Server) listVersions(r *http.Request) []string {
	if vs, ok := r.Context().Value(listVersionsKey{}).([]string); ok {
		return vs
	}
	return s.historyVersions()
}

func withListVersions(r *http.Request, versions []string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), listVersionsKey{}, versions))
}

func (s *Server) combined() bool { return s.FullVendorResearch && s.Engine.Brief != nil }

func (s *Server) choiceRequest(r *http.Request) bool {
	return s.Engine.Brief != nil && (!s.FullVendorResearch || observationVersion(r) || realtimeVersion(r))
}

func (s *Server) catalogFor(r *http.Request) *dataservice.Store {
	if s.choiceRequest(r) && s.Engine.ChoiceCatalog != nil {
		return s.Engine.ChoiceCatalog
	}
	return s.Store
}

func (s *Server) skillsFor(r *http.Request) map[string]*harness.Skill {
	if !s.choiceRequest(r) {
		return s.Engine.Skills
	}
	registry := maps.Clone(s.Engine.Skills)
	registry["stock_analysis"] = harness.ObservationBriefSkill()
	if realtimeVersion(r) {
		registry["stock_analysis"] = harness.RealtimeBriefSkill()
	}
	return registry
}

// Choice evidence has its own manifest-authorized readers. Only fixture and
// close research can read unified records through the generic evidence route.
func (s *Server) historyRecordProvider() string {
	if s.Engine.Brief == nil || s.FullVendorResearch {
		if sk := s.Engine.Skills["stock_analysis"]; sk != nil && harness.IsCloseResearchVersion(sk.Version) {
			return "tushare"
		}
		if s.Engine.Synthetic {
			return "fixture"
		}
	}
	return ""
}
