// Package app wires configuration into the services shared by vera-api, vera-worker, vera-admin and vera-harness.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
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
	Cfg      *config.Config
	Pool     *pgxpool.Pool
	Store    *dataservice.Store
	Provider dataservice.Provider
	Engine   *harness.Engine
	Watch    *watchlist.Service
	Policy   *policy.Policy
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
	if err := a.build(); err != nil {
		pool.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) build() error {
	c := a.Cfg
	cat, err := dataservice.LoadCatalog(filepath.Join(c.ContractsDir, "datasets", "catalog.json"))
	if err != nil {
		return err
	}
	a.Store = dataservice.NewStore(a.Pool, cat)
	if a.Provider, err = newProvider(c); err != nil {
		return err
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
		mp := model.Profile{Name: name, Adapter: p.Adapter, BaseURL: p.BaseURL, ModelID: p.ModelID, APIKey: p.APIKey, PriceTable: p.PriceTable,
			Timeout: time.Duration(a.Policy.ModelCall.HTTPTimeoutSeconds) * time.Second}
		if mp.ModelID == "" {
			mp.ModelID = "vera-" + p.Adapter
		}
		if p.ExtraBody != "" {
			_ = json.Unmarshal([]byte(p.ExtraBody), &mp.ExtraBody)
		}
		if p.Adapter == "openai" {
			adapters["openai"] = model.OpenAI{HTTP: &http.Client{}}
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
	a.Engine = &harness.Engine{Pool: a.Pool, Store: a.Store, Gateway: gw, Policy: a.Policy, Skills: harness.Skills(), Now: a.Provider.Now,
		Synthetic: c.Synthetic(), Basis: a.Provider.Basis(), Prompts: harness.PromptVersion, RealResearch: c.RealResearch}
	a.Watch = &watchlist.Service{Pool: a.Pool, Store: a.Store, Now: a.Provider.Now}
	return nil
}

// Sync loads provider data into the store (idempotent).
func (a *App) Sync(ctx context.Context) error {
	start := time.Now()
	n, err := a.Store.Sync(ctx, a.Provider)
	if err == nil {
		slog.Info("data sync finished", "provider", a.Provider.ID(), "inserted", n, "took", time.Since(start).Round(time.Millisecond))
	}
	return err
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
	s := &httpapi.Server{Pool: a.Pool, ID: identity.New(a.Pool, opt), Store: a.Store, Engine: a.Engine, Watch: a.Watch, Synthetic: a.Engine.Synthetic,
		Mounts: mounts}
	return s.Handler(), nil
}

// spa serves the built web app with index.html fallback for client routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := filepathStat(p); err != nil || st {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if len(r.URL.Path) > 8 && r.URL.Path[:8] == "/assets/" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fs.ServeHTTP(w, r)
	})
}

func (a *App) Close() { a.Pool.Close() }
