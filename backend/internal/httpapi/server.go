// Package httpapi implements contracts/openapi.json. Handlers are thin: identity, harness, watchlist and the data
// service do the work; responses are typed structs mirroring the contract.
package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/watchlist"
)

type Server struct {
	Pool               *pgxpool.Pool
	ID                 *identity.Service
	Store              *dataservice.Store
	Engine             *harness.Engine
	Watch              *watchlist.Service
	Synthetic          bool
	RequireChoiceQuote bool
	FullVendorResearch bool
	// Extra mounts (dev OIDC issuer, static web) added by the binary.
	Mounts map[string]http.Handler
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	auth := s.ID.Require(true)
	authNoWS := s.ID.Require(false)
	h := func(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			callErr := s.protectResponse(w, r, f)
			if err := callErr; err != nil {
				httpx.Error(w, r, err)
			}
		}
	}
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, 200, map[string]string{"status": "ok", "service": "vera-api"})
	})
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /auth/login", s.ID.Login)
	m.HandleFunc("POST /auth/login", s.ID.PasswordLogin)
	m.HandleFunc("GET /auth/callback", s.ID.Callback)
	m.Handle("POST /auth/logout", authNoWS(http.HandlerFunc(s.ID.Logout)))
	m.Handle("GET /api/v1/me", authNoWS(h(s.me)))
	m.Handle("PATCH /api/v1/me", authNoWS(h(s.patchMe)))
	m.Handle("POST /api/v1/onboarding", authNoWS(h(s.onboard)))
	m.Handle("GET /api/v1/entities", auth(h(s.searchEntities)))
	m.Handle("POST /api/v1/research-subjects/{id}/prepare", auth(h(s.prepareSubject)))
	m.Handle("GET /api/v1/skills", auth(h(s.skills)))
	m.Handle("POST /api/v1/research-plans", auth(h(s.createPlan)))
	m.Handle("GET /api/v1/research-plans/{id}", auth(h(s.getPlan)))
	m.Handle("POST /api/v1/research-tasks", auth(s.idempotent(h(s.createTask))))
	m.Handle("GET /api/v1/research-tasks", auth(h(s.listTasks)))
	m.Handle("GET /api/v1/research-tasks/{id}", auth(h(s.getTask)))
	m.Handle("POST /api/v1/research-tasks/{id}/cancel", auth(s.idempotent(h(s.cancelTask))))
	m.Handle("GET /api/v1/reports", auth(h(s.listReports)))
	m.Handle("GET /api/v1/reports/{id}/versions/{version}", auth(h(s.getReport)))
	m.Handle("GET /api/v1/reports/{id}/versions/{version}/pdf", auth(h(s.getReportPDF)))
	m.Handle("GET /api/v1/reports/{id}/versions/{version}/series/{series_id}", auth(h(s.getSeries)))
	m.Handle("POST /api/v1/reports/{id}/versions/{version}/feedback", auth(s.idempotent(h(s.feedback))))
	m.Handle("GET /api/v1/evidence/{id}", auth(h(s.getEvidence)))
	m.Handle("GET /api/v1/calculations/{id}", auth(h(s.getCalculation)))
	m.Handle("GET /api/v1/watchlist", auth(h(s.listWatch)))
	m.Handle("POST /api/v1/watchlist", auth(s.idempotent(h(s.addWatch))))
	m.Handle("PATCH /api/v1/watchlist/{item_id}", auth(h(s.patchWatch)))
	m.Handle("DELETE /api/v1/watchlist/{item_id}", auth(h(s.deleteWatch)))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { httpx.Error(w, r, errs.NotFound()) })
	for p, hh := range s.Mounts {
		m.Handle(p, hh)
	}
	return httpx.WithRequestID(recoverer(secure(s.versionGate(m))))
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("X-Frame-Options", "DENY")
		if len(r.URL.Path) > 4 && r.URL.Path[:5] == "/api/" || r.URL.Path == "/auth/callback" {
			hd.Set("Cache-Control", "no-store")
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		next.ServeHTTP(w, r)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()))
				httpx.Error(w, r, errors.New("panic"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	fail := func(reason string) {
		httpx.JSON(w, r, 503, map[string]string{"status": "not_ready", "reason": reason})
	}
	if err := s.Pool.Ping(ctx); err != nil {
		fail("database_unreachable")
		return
	}
	var providers, entities int
	if err := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM providers WHERE enabled), (SELECT count(*) FROM entities)`).Scan(&providers, &entities); err != nil {
		fail("schema_not_migrated")
		return
	}
	// Choice search populates the catalog on demand, after the first login.
	if providers == 0 || (entities == 0 && (s.Engine.Brief == nil || s.Engine.Brief.Runtime == nil)) {
		fail("data_not_synced")
		return
	}
	// Deployments can register the grant after the new image is healthy.
	if s.RequireChoiceQuote {
		if quote, err := s.quoteCapabilities(ctx); err != nil || !quote.Latest {
			fail("realtime_source_unavailable")
			return
		}
	}
	httpx.JSON(w, r, 200, map[string]string{"status": "ready"})
}

func (s *Server) quoteCapabilities(ctx context.Context) (emquant.QuoteCapabilities, error) {
	source := s.Engine.RealtimeSource
	if source == nil {
		return emquant.QuoteCapabilities{}, nil
	}
	if !s.Engine.RealtimeEnabled() {
		return emquant.QuoteCapabilities{}, errors.New("choice quote runtime unavailable")
	}
	return emquant.AllStockQuoteCapabilities(ctx, s.Pool, source.ProviderID, source.Product)
}

func principal(r *http.Request) (*identity.Principal, uuid.UUID) {
	p, _ := identity.FromContext(r.Context())
	if p.WorkspaceID == nil {
		return p, uuid.Nil
	}
	return p, *p.WorkspaceID
}

func pathID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, errs.NotFound()
	}
	return id, nil
}

// ---- idempotency (Idempotency-Key header, per workspace + route) ----

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

func (s *Server) idempotent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 100 {
			httpx.Error(w, r, errs.BadRequest("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key 过长"))
			return
		}
		_, ws := principal(r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			httpx.Error(w, r, errs.BadRequest("INVALID_BODY", "请求过大"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		scope := s.replayScope()
		sum := sha256.Sum256(append([]byte(r.URL.Path+"\x00"+representation(r)+"\x00"+scope+"\x00"), body...))
		hash := hex.EncodeToString(sum[:])
		route := r.Pattern
		var stored struct {
			hash   string
			status int
			resp   []byte
		}
		err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(), `SELECT request_hash, status, response FROM idempotency_keys WHERE workspace_id=$1 AND method=$2 AND route=$3 AND key=$4 AND expires_at > now()`,
				ws, r.Method, route, key).Scan(&stored.hash, &stored.status, &stored.resp)
		})
		if err == nil {
			if stored.hash != hash {
				httpx.Error(w, r, errs.Conflict("IDEMPOTENCY_KEY_REUSED", "Idempotency-Key 已用于不同的请求"))
				return
			}
			replay := func() error {
				w.Header().Set("Content-Type", representation(r)+"; charset=utf-8")
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(stored.status)
				if stored.status == http.StatusNoContent {
					return nil
				}
				_, err := w.Write(stored.resp)
				return err
			}
			// Replays use the same grant as the original command, including
			// the persisted plan version for task creation.
			grant, err := s.responseGrant(r)
			if err == nil {
				err = grant(replay)
			}
			if err != nil {
				httpx.Error(w, r, err)
			}
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, r, err)
			return
		}
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status >= 500 || rec.status == 429 || !json.Valid(rec.buf.Bytes()) && rec.buf.Len() > 0 {
			return
		}
		resp := rec.buf.Bytes()
		if len(resp) == 0 {
			resp = []byte("null")
		}
		_ = db.InWorkspace(context.WithoutCancel(r.Context()), s.Pool, ws, func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys(workspace_id,method,route,key,request_hash,status,response,expires_at)
				VALUES($1,$2,$3,$4,$5,$6,$7, now() + interval '24 hours') ON CONFLICT DO NOTHING`, ws, r.Method, route, key, hash, rec.status, resp)
			return err
		})
	})
}

// Scope cached domain responses to their deployment and licensed product.
// NUL separators distinguish the current namespaces from all previous vendor
// scopes (a single environment variable). The empty fixture scope is stable.
func (s *Server) replayScope() string {
	if s.FullVendorResearch {
		if source := s.Engine.Brief; source != nil {
			return "v3\x00tushare+emquant\x00" + source.ProviderID + "\x00" + source.Product
		}
		return "v2\x00tushare"
	}
	if source := s.Engine.Brief; source != nil {
		return "v2\x00emquant\x00" + source.ProviderID + "\x00" + source.Product
	}
	return ""
}
