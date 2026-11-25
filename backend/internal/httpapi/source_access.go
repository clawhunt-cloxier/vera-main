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

// Keep handler errors and visibility failures intact. Treat other failures
// before the callback as source unavailability.
func checkSource(check responseGrant, message string) responseGrant {
	return func(fn func() error) error {
		called := false
		err := check(func() error {
			called = true
			return fn()
		})
		if err != nil && !called && errs.As(err).Status != http.StatusNotFound {
			return errs.Unavailable("SOURCE_UNAVAILABLE", message).Wrap(err)
		}
		return err
	}
}

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
			return checkSource(func(use func() error) error { return s.Engine.WithChoiceResearchUse(r.Context(), use) }, "Choice 研究来源当前不可用")(fn)
		}
		return s.Engine.Brief.WithDisplayCatalog(r.Context(), fn)
	}
}

func (s *Server) mixedGrant(r *http.Request) responseGrant {
	return checkSource(func(use func() error) error { return s.Engine.WithMixedResearchUse(r.Context(), use) }, "双来源研究数据当前不可用")
}

func (s *Server) versionGrant(r *http.Request, version string) (responseGrant, error) {
	if !slices.Contains(s.historyVersions(), version) {
		return nil, errs.NotFound()
	}
	switch {
	case version == harness.MixedObservationVersion:
		return s.mixedGrant(r), nil
	case emquant.IsDailyBriefVersion(version) || version == harness.RealtimeBriefVersion:
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
		query = "SELECT p.id,p.skill_version FROM research_plans p WHERE p.id=$1 AND NOT p.conversation_plan"
		args = append(args, input.PlanID)
	} else {
		id, err := pathID(r, "id")
		if err != nil {
			return task, "", err
		}
		args = append(args, id)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/research-plans/"):
			query = "SELECT p.id,p.skill_version FROM research_plans p WHERE p.id=$1 AND NOT p.conversation_plan"
		case strings.HasPrefix(r.URL.Path, "/api/v1/research-tasks/"):
			query = `SELECT t.id,t.skill_version FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
				WHERE t.id=$1 AND ` + visibleShortResearch
		case strings.HasPrefix(r.URL.Path, "/api/v1/calculations/"):
			query = `SELECT t.id,t.skill_version FROM calculations c JOIN research_tasks t ON t.id=c.task_id AND t.workspace_id=c.workspace_id
				JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id WHERE c.id=$1 AND ` + visibleShortResearch
		case strings.HasPrefix(r.URL.Path, "/api/v1/reports/"):
			number, err := strconv.Atoi(r.PathValue("version"))
			if err != nil || number < 1 {
				return task, "", errs.NotFound()
			}
			query = `SELECT t.id,t.skill_version FROM report_versions v JOIN research_tasks t ON t.id=v.task_id AND t.workspace_id=v.workspace_id
				JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id WHERE v.report_id=$1 AND v.version=$2 AND ` + visibleShortResearch
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
	case r.Pattern == "POST /api/v1/research-subjects/{id}/prepare":
		if !s.combined() {
			return nil, errs.NotFound()
		}
		return s.mixedGrant(r), nil
	case r.PathValue("id") != "" || r.Pattern == "POST /api/v1/research-tasks":
		task, version, err := s.resourceVersion(r)
		if err != nil {
			return nil, err
		}
		grant, err := s.versionGrant(r, version)
		if err != nil {
			return nil, err
		}
		if (strings.HasPrefix(path, "/api/v1/reports/") || (version == harness.CloseUnifiedVersion || version == harness.ConversationCloseVersion) && strings.HasPrefix(path, "/api/v1/calculations/")) &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
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
		case harness.ConversationCloseVersion:
			return checkSource(func(use func() error) error {
				return s.Engine.WithConversationSnapshot(ctx, ws, task, func(pgx.Tx) error { return use() })
			}, "研究所用的冻结数据当前不可读取")(fn)
		case harness.CloseUnifiedVersion:
			return checkSource(func(use func() error) error {
				return s.Engine.WithUnifiedSources(ctx, ws, task, dataservice.SourceDisplay, func(pgx.Tx, []harness.BoundResearchSupplement) error { return use() })
			}, "研报的冻结数据当前不可读取")(fn)
		case harness.MixedObservationVersion:
			return checkSource(func(use func() error) error {
				return s.Engine.WithMixedDisplay(ctx, ws, task, func(emquant.BriefObservation, []dataservice.StoredRecord) error { return use() })
			}, "双来源研报的冻结数据当前不可读取")(fn)
		case emquant.ObservationBriefVersion, emquant.ConversationObservationVersion:
			return s.withDaily(r, ws, task, version, func(emquant.BriefObservation) error { return fn() })
		case harness.RealtimeBriefVersion:
			return s.withQuote(r, ws, task, func(emquant.QuoteView) error { return fn() })
		default:
			return meta(fn)
		}
	}
}

// The provider switch and the bound snapshot are checked in one transaction.
// In particular a full pool of concurrent readers never waits for a second
// connection while already holding its first grant lock.
func (s *Server) withDaily(r *http.Request, ws, task uuid.UUID, version string, use func(emquant.BriefObservation) error) error {
	brief := s.Engine.Brief
	if brief == nil || brief.Runtime == nil {
		return errs.Unavailable("SOURCE_UNAVAILABLE", "Choice 研究来源当前不可用")
	}
	if !s.combined() && version != emquant.ConversationObservationVersion {
		return brief.WithSnapshot(r.Context(), ws, task, dataservice.SourceDisplay, use)
	}
	var observation emquant.BriefObservation
	grant := checkSource(func(display func() error) error {
		return pgx.BeginFunc(r.Context(), s.Pool, func(tx pgx.Tx) error {
			// Confirm also acquires the provider permission before the conversation.
			if s.combined() {
				if err := brief.MixedProviderUse(r.Context(), tx, dataservice.SourceModel); err != nil {
					return err
				}
			}
			if version == emquant.ConversationObservationVersion {
				if err := harness.LockShortConversation(r.Context(), tx, ws, task); err != nil {
					return err
				}
			}
			o, err := brief.Read(r.Context(), tx, ws, task, dataservice.SourceDisplay)
			if err != nil {
				return err
			}
			if o.Manifest.Security.Product != brief.Product || o.Manifest.Liquidity.Product != brief.Product {
				return errs.Unavailable("SOURCE_UNAVAILABLE", "成交快照不属于当前 Choice 产品")
			}
			observation = o
			return display()
		})
	}, "Choice 研究来源当前不可用")
	return grant(func() error {
		return use(observation)
	})
}

func (s *Server) withQuote(r *http.Request, ws, task uuid.UUID, use func(emquant.QuoteView) error) error {
	brief := s.Engine.Brief
	if brief == nil || brief.Runtime == nil {
		return errs.Unavailable("SOURCE_UNAVAILABLE", "Choice 研究来源当前不可用")
	}
	if !s.combined() {
		return brief.Runtime.WithTaskSnapshotQuote(r.Context(), ws, task, dataservice.SourceDisplay, use)
	}
	return pgx.BeginTxFunc(r.Context(), s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if err := brief.MixedProviderUse(r.Context(), tx, dataservice.SourceModel); err != nil {
			return errs.Unavailable("SOURCE_UNAVAILABLE", "Choice 研究来源当前不可用").Wrap(err)
		}
		view, err := brief.Runtime.ReadTaskSnapshotQuote(r.Context(), tx, ws, task, dataservice.SourceDisplay)
		if err != nil {
			return err
		}
		for _, q := range view.Quotes {
			if q.ProviderID != brief.ProviderID || q.Product != brief.Product {
				return errs.Unavailable("SOURCE_UNAVAILABLE", "行情快照不属于当前 Choice 产品")
			}
		}
		return use(view)
	})
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
		vs = append(vs, closeHistoryVersions()...)
	}
	switch {
	case ce == nil && te == nil:
		if observationVersion(r) || realtimeVersion(r) {
			vs = append(vs, harness.MixedObservationVersion)
		}
		return vs, s.mixedGrant(r), nil
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
			r = withListVersions(r, closeHistoryVersions())
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
