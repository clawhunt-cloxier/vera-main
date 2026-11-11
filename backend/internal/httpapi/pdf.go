package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/reportpdf"
)

// pdfFileName builds a download name from the report title; characters that are unsafe in file names become underscores.
func pdfFileName(title string, version int) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, title)
	clean = strings.TrimSpace(clean)
	if clean == "" {
		clean = "report"
	}
	return fmt.Sprintf("%s_v%d.pdf", clean, version)
}

// getReportPDF typesets the delivered report on the server and returns it as a PDF download. The fonts are embedded in the
// binary, so the output does not depend on the client machine or on fonts installed on the server.
func (s *Server) getReportPDF(w http.ResponseWriter, r *http.Request) error {
	_, d, raw, err := s.loadDoc(r)
	if err != nil {
		return err
	}
	ents, err := s.Store.Entities(r.Context(), []uuid.UUID{d.SubjectID})
	if err != nil {
		return err
	}
	rep := Report{ID: d.ID, Version: d.Version, TaskID: d.TaskID, Title: d.Title, SkillID: d.SkillID, SkillVersion: d.SkillVersion,
		Subject: toEntity(ents[d.SubjectID], s.Engine.Skills), AsOf: d.AsOf, DataAsOf: d.DataAsOf, ResultKind: d.ResultKind, Synthetic: d.Synthetic,
		AIGenerated: true, SummaryRefs: d.SummaryRefs, Sections: d.Sections, Claims: d.Claims, Limitations: d.Limitations, Sources: d.Sources,
		Disclaimer: d.Disclaimer, DeliveredAt: d.DeliveredAt}
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	var pr reportpdf.Report
	if err := json.Unmarshal(b, &pr); err != nil {
		return err
	}
	var list []reportpdf.Series
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	series := make(map[string]reportpdf.Series, len(list))
	for _, sr := range list {
		series[sr.ID] = sr
	}
	out, err := reportpdf.Render(pr, series)
	if err != nil {
		return err
	}
	name := pdfFileName(d.Title, d.Version)
	h := w.Header()
	h.Set("Content-Type", "application/pdf")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"report-v%d.pdf\"; filename*=UTF-8''%s", d.Version, url.PathEscape(name)))
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}
