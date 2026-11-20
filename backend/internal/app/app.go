// Package app wires configuration into the services shared by vera-api, vera-worker, vera-admin and vera-harness.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/tushare"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/httpapi"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity/devoidc"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/watchlist"
)

type App struct {
	Cfg   *config.Config
	Pool  *pgxpool.Pool
	Store *dataservice.Store
	// Primary supplies the closing research clock/basis and watchlist series.
	// Choice observation requests always go through Brief and ChoiceCatalog.
	Primary       dataservice.Provider
	ChoiceCatalog *dataservice.Store
	Engine        *harness.Engine
	Watch         *watchlist.Service
	Brief         *emquant.BriefService
	TuShare       *tushare.Provider
	Policy        *policy.Policy
}

// Provider returns the configured data provider. Vendor adapters register here once their SDK/contract is provided.
func newProvider(c *config.Config) (dataservice.Provider, error) {
	if c.DataProvider == "fixture" {
		return fixture.New(c.FixtureManifest)
	}
	return nil, fmt.Errorf("data provider %q is not implemented yet: vendor adapters are added when vendor documentation is available", c.DataProvider)
}

// New connects, migrates (optional) and builds services. It does not start River workers.
func New(ctx context.Context, c *config.Config) (*App, error) {
	pool, err := db.Open(ctx, c.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if c.AutoMigrate {
		if err := db.Migrate(ctx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}
	a := &App{Cfg: c, Pool: pool}
	if err := a.build(ctx); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) build(ctx context.Context) error {
	c := a.Cfg
	cat, err := dataservice.LoadCatalog(filepath.Join(c.ContractsDir, "datasets", "catalog.json"))
	if err != nil {
		return err
	}
	if c.DataProvider == "fixture" {
		var realEntities bool
		if err := a.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entities WHERE NOT synthetic)`).Scan(&realEntities); err != nil {
			return err
		}
		if realEntities {
			return fmt.Errorf("fixture mode requires a separate database: real security identities already exist")
		}
		a.Store = dataservice.NewStore(a.Pool, cat)
	} else {
		a.Store = dataservice.NewVendorStore(a.Pool, cat)
	}
	if c.ChoiceConfigured() {
		a.Brief, err = emquant.OpenBriefFromEnv(ctx, a.Pool, cat, os.Getenv)
		if err != nil {
			return err
		}
		a.ChoiceCatalog = a.Store
		a.Primary = a.Brief
	}
	if c.TuShareConfigured() {
		if a.TuShare, err = tushare.Open(a.Pool, os.Getenv); err != nil {
			return err
		}
		if a.Brief != nil && a.Brief.ID() == a.TuShare.ID() {
			return fmt.Errorf("configured providers must have distinct identities")
		}
		// The canonical research store serves TuShare closing observations.
		// Choice retains its registered SDK collector and bound archive reader.
		a.Primary = a.TuShare
		a.Store = dataservice.NewResearchStore(a.Pool, cat, a.TuShare.ID(), a.TuShare.SubjectCodes(), a.TuShare.AllowModelProcessing())
	}
	if a.Primary == nil {
		if a.Primary, err = newProvider(c); err != nil {
			return err
		}
	}
	if a.Policy, err = policy.Load(c.PolicyPath); err != nil {
		return err
	}
	schemas, err := model.LoadSchemas(filepath.Join(c.ContractsDir, "model"))
	if err != nil {
		return err
	}
	var prices *model.PriceTable
	profiles := map[string]model.Profile{}
	adapters := map[string]model.Adapter{"sim": model.Sim{}, "replay": model.Replay{Dir: c.ReplayDir}}
	for name, p := range c.Profiles {
		timeout := time.Duration(a.Policy.ModelCall.HTTPTimeoutSeconds) * time.Second
		if p.TimeoutSeconds > 0 {
			timeout = time.Duration(p.TimeoutSeconds) * time.Second
		}
		mp := model.Profile{Name: name, Adapter: p.Adapter, BaseURL: p.BaseURL, ModelID: p.ModelID, APIKey: p.APIKey, PriceTable: p.PriceTable,
			Temperature: p.Temperature, Thinking: p.Thinking, ExtraBody: p.ExtraBody, Timeout: timeout, MaxOutput: p.MaxOutput,
			Concurrency: p.Concurrency}
		if mp.ModelID == "" {
			mp.ModelID = "vera-" + p.Adapter
		}
		if p.Adapter == "openai" || p.Adapter == "record" {
			adapters["openai"] = model.OpenAI{HTTP: &http.Client{}}
			// record: cassette hits replay for free, misses go to the vendor and are saved (development only).
			adapters["record"] = model.Replay{Dir: c.ReplayDir, Record: adapters["openai"]}
			if prices == nil {
				if prices, err = model.LoadPrices(c.PriceTablePath); err != nil {
					return err
				}
				if prices.Version != p.PriceTable {
					return fmt.Errorf("price table version %q does not match MODEL_PRICE_TABLE_VERSION %q", prices.Version, p.PriceTable)
				}
			}
			if _, err := prices.Reserve(p.ModelID, 1, 1); err != nil {
				return fmt.Errorf("profile %s: %w", name, err)
			}
		}
		profiles[name] = mp
	}
	limit := c.DailyCostLimit
	if a.Policy.Quota.PlatformDailyCostLimitUSD.LessThan(limit) {
		limit = a.Policy.Quota.PlatformDailyCostLimitUSD
	}
	gw := model.NewGateway(a.Pool, profiles, adapters, prices, schemas, a.Policy, limit)
	a.Engine = &harness.Engine{Pool: a.Pool, Store: a.Store, ChoiceCatalog: a.ChoiceCatalog, Gateway: gw, Policy: a.Policy, Skills: harness.Skills(), Now: a.Primary.Now,
		Synthetic: c.Synthetic(), Basis: a.Primary.Basis(), Prompts: harness.PromptVersion, RealResearch: c.RealResearch, Brief: a.Brief}
	if a.Brief != nil {
		a.Engine.Skills["stock_analysis"] = harness.ObservationBriefSkill()
		// Choice grant and mapping identities are resolved per requested security
		// at collection time; deployment config names only the runtime product.
		a.Engine.RealtimeSource = &harness.BriefQuoteSource{ProviderID: a.Brief.ProviderID, Product: a.Brief.Product}
	}
	if a.TuShare != nil {
		a.Engine.Skills = harness.UnifiedCloseSkills()
		a.Engine.WithCloseResearchUse = a.Store.WithResearchUse
		a.Engine.CloseResearchCutoff = a.Store.PublicationCutoff
		if a.Brief != nil {
			a.Engine.ResearchSupplements = []dataservice.ResearchSupplementSource{a.Brief}
		}
	}
	a.Watch = &watchlist.Service{Pool: a.Pool, Store: a.Store, Now: a.Primary.Now, ProviderID: a.Primary.ID()}
	return nil
}

// Sync loads provider data into the store (idempotent).
func (a *App) Sync(ctx context.Context) error {
	if a.Brief != nil {
		if err := a.RegisterChoice(ctx); err != nil || a.TuShare == nil {
			return err
		}
	}
	if a.TuShare != nil {
		return a.SyncTuShare(ctx)
	}
	start := time.Now()
	n, err := a.Store.Sync(ctx, a.Primary)
	if err == nil {
		slog.Info("data sync finished", "provider", a.Primary.ID(), "inserted", n, "took", time.Since(start).Round(time.Millisecond))
	}
	return err
}

// RegisterChoice publishes the SDK provider identity before observation work is served.
func (a *App) RegisterChoice(ctx context.Context) error {
	if _, err := a.ChoiceCatalog.Sync(ctx, a.Brief); err != nil {
		return fmt.Errorf("register Choice observation provider: %w", err)
	}
	return nil
}

// SyncTuShare publishes a complete, source-scoped observation per registered subject.
func (a *App) SyncTuShare(ctx context.Context) error {
	for _, code := range a.TuShare.SubjectCodes() {
		var notes []string
		n, err := a.Store.SyncObserved(ctx, a.TuShare.ID(), func(ctx context.Context) (dataservice.Provider, error) {
			batch, limitations, err := a.TuShare.Collect(ctx, code, a.TuShare.Now())
			notes = limitations
			return batch, err
		})
		if err != nil {
			return fmt.Errorf("TuShare %s: %w", code, err)
		}
		slog.InfoContext(ctx, "TuShare observation published", "subject", code, "inserted", n, "limitations", notes)
	}
	return nil
}

func (a *App) ProviderByID(id string) dataservice.Provider {
	if a.Brief != nil && id == a.Brief.ID() {
		return a.Brief
	}
	if a.Primary != nil && id == a.Primary.ID() {
		return a.Primary
	}
	return nil
}

// HTTP builds the API handler (plus dev OIDC and static web when configured).
func (a *App) HTTP() (http.Handler, error) {
	c := a.Cfg
	mounts := map[string]http.Handler{}
	opt := identity.Options{Issuer: c.OIDCIssuer, InternalIssuer: c.OIDCInternal, ClientID: c.OIDCClientID, ClientSecret: c.OIDCClientSecret,
		PublicURL: c.PublicURL, CookieSecure: c.CookieSecure, Registration: c.Registration}
	if c.DevOIDC {
		secret := c.OIDCClientSecret
		if secret == "" {
			secret = identity.Random(24) // per-process; the issuer lives in the same process
		}
		idp, err := devoidc.New(c.PublicURL+"/dev-oidc", "vera-web", secret, c.PublicURL+"/auth/callback")
		if err != nil {
			return nil, err
		}
		mounts["/dev-oidc/"] = http.StripPrefix("/dev-oidc", idp.Handler())
		opt.Issuer, opt.ClientID, opt.ClientSecret = idp.Issuer, "vera-web", secret
		opt.InternalIssuer = "http://" + c.APIAddr + "/dev-oidc"
		slog.Warn("development OIDC issuer enabled: any username can log in; never expose this deployment")
	}
	if c.WebDir != "" {
		mounts["/"] = spa(c.WebDir)
	}
	s := &httpapi.Server{Pool: a.Pool, ID: identity.New(a.Pool, opt), Store: a.Store, Engine: a.Engine, Watch: a.Watch, Synthetic: a.Engine.Synthetic, RequireChoiceQuote: c.RequireChoiceQuote,
		Mounts: mounts, FullVendorResearch: a.TuShare != nil}
	return s.Handler(), nil
}

// spa serves the built web app. The landing page (index.html) is served for
// the root and marketing routes; the React SPA (app.html) is served as
// fallback for /app/* and other authenticated routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := filepathStat(p); err != nil || st {
			w.Header().Set("Cache-Control", "no-cache")
			// React SPA (app.html) owns the authenticated routes: /app/* and /onboarding.
			// All other unknown paths fall back to the landing page (index.html).
			fallback := "index.html"
			if r.URL.Path == "/app" || strings.HasPrefix(r.URL.Path, "/app/") ||
				r.URL.Path == "/onboarding" || strings.HasPrefix(r.URL.Path, "/onboarding/") {
				fallback = "app.html"
			}
			http.ServeFile(w, r, filepath.Join(dir, fallback))
			return
		}
		if len(r.URL.Path) > 8 && r.URL.Path[:8] == "/assets/" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fs.ServeHTTP(w, r)
	})
}

func (a *App) Close() {
	if a.Brief != nil {
		_ = a.Brief.Runtime.Close()
	}
	a.Pool.Close()
}
