package httpapi

import (
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
)

// Persisted research uses distinct skill versions for each data source. Scope
// both detail and list queries before materializing history or applying LIMIT.
func (s *Server) historyVersions() []string {
	if s.Engine.Brief != nil {
		return []string{emquant.ObservationBriefVersion, harness.RealtimeBriefVersion}
	}
	if sk := s.Engine.Skills["stock_analysis"]; sk != nil {
		if sk.Version == harness.CloseResearchVersion {
			return []string{harness.CloseResearchVersion}
		}
		if s.Engine.Synthetic && sk.Version == "1.0.0" {
			return []string{sk.Version}
		}
	}
	return nil
}

// Choice evidence has its own manifest-authorized readers. Only fixture and
// close research can read unified records through the generic evidence route.
func (s *Server) historyRecordProvider() string {
	if s.Engine.Brief == nil {
		if sk := s.Engine.Skills["stock_analysis"]; sk != nil && sk.Version == harness.CloseResearchVersion {
			return "tushare"
		}
		if s.Engine.Synthetic {
			return "fixture"
		}
	}
	return ""
}
