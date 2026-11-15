package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/app"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
)

// demo runs everything in one process for local evaluation and browser tests. It refuses non-development
// environments: the development OIDC issuer accepts any username.
func demo(ctx context.Context) error {
	if v := os.Getenv("VERA_ENV"); v != "" && v != "development" && v != "test" {
		return errors.New("vera demo 只能在 development/test 环境运行")
	}
	dir := os.Getenv("VERA_DEMO_DIR")
	if dir == "" {
		home, _ := os.UserCacheDir()
		dir = filepath.Join(home, "vera-demo")
	}
	port := 55432
	if p := os.Getenv("VERA_DEMO_PG_PORT"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &port)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Version(embeddedpostgres.V18).Port(uint32(port)).
		Database("vera").Username("vera").Password("vera-demo").DataPath(filepath.Join(dir, "data")).
		RuntimePath(filepath.Join(dir, "run")).CachePath(filepath.Join(dir, "cache")).Logger(nil))
	if err := pg.Start(); err != nil {
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	defer func() { _ = pg.Stop() }()
	setDefault("VERA_ENV", "development")
	setDefault("DATABASE_URL", fmt.Sprintf("postgres://vera:vera-demo@127.0.0.1:%d/vera?sslmode=disable", port))
	setDefault("VERA_DEV_OIDC", "true")
	setDefault("VERA_REGISTRATION_ENABLED", "true") // demo opens workspaces with invites
	setDefault("VERA_COOKIE_SECURE", "false")
	setDefault("VERA_API_ADDR", "127.0.0.1:8080")
	setDefault("VERA_PUBLIC_URL", "http://"+os.Getenv("VERA_API_ADDR"))
	c, err := config.Load()
	if err != nil {
		return err
	}
	a, err := app.New(ctx, c)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Sync(ctx); err != nil {
		return err
	}
	if n := os.Getenv("VERA_DEMO_INVITES"); n != "0" {
		codes, err := identity.CreateInvites(ctx, a.Pool, 3, 1, 14*24*time.Hour, "demo", "vera-demo")
		if err != nil {
			return err
		}
		fmt.Println("演示邀请码（每个可用一次）：")
		for _, c := range codes {
			fmt.Println("  " + c)
		}
	}
	rc, err := a.Engine.NewRiver(a.Pool, true, c.WorkerConcurrency, a.Watch.Register, a.Engine.Gateway.Register)
	if err != nil {
		return err
	}
	if err := rc.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = rc.Stop(context.Background()) }()
	h, err := a.HTTP()
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.APIAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	slog.Info("vera demo ready", "url", c.PublicURL, "web", c.WebDir != "")
	fmt.Printf("Vera 测试服务已启动：%s （开发登录；synthetic=%t）\n", c.PublicURL, a.Engine.Synthetic)
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

func setDefault(k, v string) {
	if os.Getenv(k) == "" {
		os.Setenv(k, v)
	}
}
