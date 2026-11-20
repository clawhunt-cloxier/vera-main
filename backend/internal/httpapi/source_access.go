package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

type responseGrant func(func() error) error

func noop() error                        { return nil }
func unrestricted(fn func() error) error { return fn() }

func (s *Server) closeGrant(r *http.Request) responseGrant {
	return func(fn func() error) error {
		err := s.Store.WithResearchUse(r.Context(), fn)
		var pe *dataservice.ProviderError
		if errors.As(err, &pe) {
			return errs.Unavailable("SOURCE_UNAVAILABLE", "TuShare 研究来源当前不可用")
		}
		return err
	}
}

func (s *Server) choiceGrant(r *http.Request) responseGrant {
	return func(fn func() error) error {
		if s.combined() {
			return s.Engine.WithChoiceResearchUse(r.Context(), fn)
		}
		return s.Engine.Brief.WithDisplayCatalog(r.Context(), fn)
	}
}

func (s *Server) versionGrant(r *http.Request, version string) (responseGrant, error) {
	if !slices.Contains(s.historyVersions(), version) {
		return nil, errs.NotFound()
	}
	switch {
	case version == harness.MixedObservationVersion:
		return func(fn func() error) error { return s.Engine.WithMixedResearchUse(r.Context(), fn) }, nil
	case version == emquant.ObservationBriefVersion || version == harness.RealtimeBriefVersion:
		return s.choiceGrant(r), nil
	case harness.IsCloseResearchVersion(version):
		return s.closeGrant(r), nil
	default:
		return unrestricted, nil
	}
}

// Resolve task and report versions under workspace RLS before deciding which
// source may display them. Accept negotiates representation, never ownership.
func (s *Server) resourceVersion(r *http.Request) (task uuid.UUID, version string, err error) {
	query := ""
	args := []any{}
	if r.Pattern == "POST /api/v1/research-tasks" {
		var input struct {
			PlanID uuid.UUID `json:"plan_id"`
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return task, "", errs.BadRequest("INVALID_BODY", "请求内容无效")
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		if json.Unmarshal(data, &input) != nil {
			return task, "", errs.BadRequest("INVALID_BODY", "请求内容无效")
		}
		query = "SELECT id,skill_version FROM research_plans WHERE id=$1"
		args = append(args, input.PlanID)
	} else {
		id, err := pathID(r, "id")
		if err != nil {
			return task, "", err
		}
		args = append(args, id)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/research-plans/"):
			query = "SELECT id,skill_version FROM research_plans WHERE id=$1"
		case strings.HasPrefix(r.URL.Path, "/api/v1/research-tasks/"):
			query = "SELECT id,skill_version FROM research_tasks WHERE id=$1"
		case strings.HasPrefix(r.URL.Path, "/api/v1/calculations/"):
			query = "SELECT t.id,t.skill_version FROM calculations c JOIN research_tasks t ON t.id=c.task_id AND t.workspace_id=c.workspace_id WHERE c.id=$1"
		case strings.HasPrefix(r.URL.Path, "/api/v1/reports/"):
			number, err := strconv.Atoi(r.PathValue("version"))
			if err != nil || number < 1 {
				return task, "", errs.NotFound()
			}
			query = "SELECT t.id,t.skill_version FROM report_versions v JOIN research_tasks t ON t.id=v.task_id AND t.workspace_id=v.workspace_id WHERE v.report_id=$1 AND v.version=$2"
			args = append(args, number)
		default:
			return task, "", errs.NotFound()
		}
	}
	_, ws := principal(r)
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), query, args...).Scan(&task, &version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = errs.NotFound()
	}
	return task, version, err
}

func (s *Server) responseGrant(r *http.Request) (responseGrant, error) {
	path := r.URL.Path
	switch {
	case path == "/api/v1/me", path == "/api/v1/onboarding", strings.HasSuffix(path, "/cancel"),
		strings.HasPrefix(path, "/api/v1/evidence/"):
		return unrestricted, nil
	case strings.HasPrefix(path, "/api/v1/watchlist"):
		if s.FullVendorResearch {
			return s.closeGrant(r), nil
		}
	case r.PathValue("id") != "" || r.Pattern == "POST /api/v1/research-tasks":
		task, version, err := s.resourceVersion(r)
		if err != nil {
			return nil, err
		}
		grant, err := s.versionGrant(r, version)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(path, "/api/v1/reports/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			return s.snapshotGrant(r, task, version, grant), nil
		}
		return grant, nil
	}
	switch {
	case s.choiceRequest(r):
		return s.choiceGrant(r), nil
	case s.FullVendorResearch:
		return s.closeGrant(r), nil
	default:
		return unrestricted, nil
	}
}

func (s *Server) snapshotGrant(r *http.Request, task uuid.UUID, version string, meta responseGrant) responseGrant {
	_, ws := principal(r)
	ctx := r.Context()
	return func(fn func() error) error {
		switch version {
		case harness.MixedObservationVersion:
			return s.Engine.WithMixedDisplay(ctx, ws, task, func(emquant.BriefObservation, []dataservice.StoredRecord) error { return fn() })
		case emquant.ObservationBriefVersion, harness.RealtimeBriefVersion:
			if s.Engine.Brief == nil || s.Engine.Brief.Runtime == nil {
				return errs.Unavailable("SOURCE_UNAVAILABLE", "真实报告来源当前不可读取")
			}
			return meta(func() error {
				if version == harness.RealtimeBriefVersion {
					return s.Engine.Brief.Runtime.WithTaskSnapshotQuote(ctx, ws, task, dataservice.SourceDisplay, func(emquant.QuoteView) error { return fn() })
				}
				return s.Engine.Brief.WithSnapshot(ctx, ws, task, dataservice.SourceDisplay, func(emquant.BriefObservation) error { return fn() })
			})
		default:
			return meta(fn)
		}
	}
}

// Filter the list before SQL LIMIT. If a source is revoked, its own items and
// mixed items disappear together. Keep the same set of rights through writing.
func (s *Server) listGrant(r *http.Request) ([]string, responseGrant, error) {
	choice, close := s.choiceGrant(r), s.closeGrant(r)
	ce, te := choice(noop), close(noop)
	vs := []string{}
	if ce == nil {
		if realtimeVersion(r) {
			vs = append(vs, harness.RealtimeBriefVersion)
		}
		if observationVersion(r) || realtimeVersion(r) {
			vs = append(vs, emquant.ObservationBriefVersion)
		}
	}
	if te == nil {
		vs = append(vs, harness.CloseResearchVersion, harness.CloseCompanyVersion)
	}
	switch {
	case ce == nil && te == nil:
		if observationVersion(r) || realtimeVersion(r) {
			vs = append(vs, harness.MixedObservationVersion)
		}
		return vs, func(fn func() error) error { return s.Engine.WithMixedResearchUse(r.Context(), fn) }, nil
	case ce == nil:
		return vs, choice, nil
	case te == nil:
		return vs, close, nil
	default:
		return nil, nil, te
	}
}

func (s *Server) protectResponse(w http.ResponseWriter, r *http.Request, handle func(http.ResponseWriter, *http.Request) error) error {
	if !s.FullVendorResearch && s.Engine.Brief == nil || strings.HasPrefix(r.URL.Path, "/api/v1/evidence/") || strings.HasSuffix(r.URL.Path, "/cancel") || r.URL.Path == "/api/v1/me" || r.URL.Path == "/api/v1/onboarding" {
		return handle(w, r)
	}
	var grant responseGrant
	var err error
	if s.combined() && (r.Pattern == "GET /api/v1/research-tasks" || r.Pattern == "GET /api/v1/reports") {
		var versions []string
		versions, grant, err = s.listGrant(r)
		r = withListVersions(r, versions)
	} else {
		grant, err = s.responseGrant(r)
		if s.combined() && strings.HasPrefix(r.URL.Path, "/api/v1/watchlist") {
			r = withListVersions(r, []string{harness.CloseResearchVersion, harness.CloseCompanyVersion})
		}
	}
	if err != nil {
		return err
	}
	if err := grant(noop); err != nil {
		return err
	}
	buf := httptest.NewRecorder()
	buf.Header().Set("Content-Type", w.Header().Get("Content-Type"))
	if err := handle(buf, r); err != nil {
		return err
	}
	// Observation plans select their source set by subject and coverage.
	// Recheck the actual persisted version before exposing the preview.
	if s.combined() && r.Pattern == "POST /api/v1/research-plans" && observationVersion(r) {
		var result PlanResult
		if err := json.Unmarshal(buf.Body.Bytes(), &result); err != nil {
			return err
		}
		if result.Plan != nil {
			grant, err = s.versionGrant(r, result.Plan.SkillVersion)
			if err != nil {
				return err
			}
		}
	}
	return grant(func() error {
		for k, v := range buf.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(buf.Code)
		if buf.Body.Len() == 0 {
			return nil
		}
		_, err := w.Write(buf.Body.Bytes())
		return err
	})
}
