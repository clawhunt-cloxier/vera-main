// Command vera runs the Vera services: api, worker, admin, harness, migrate, demo.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/app"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/config"
)

const usage = `用法: vera <command> [args]

  api        启动 HTTP API（插入任务，不执行任务）
  worker     启动研究任务与定时作业 Worker
  migrate    执行数据库迁移
  sync       从配置的数据提供方同步数据（幂等）
  emquant    受控 Choice 行情采集（emquant help）
  admin      管理命令（vera admin help）
  harness    评测与重放（vera harness help）
  demo       本地一键演示：内嵌 PostgreSQL + API + Worker + 开发登录（仅 development）
`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "api":
		err = withApp(ctx, func(a *app.App) error { return serveAPI(ctx, a) })
	case "worker":
		err = withApp(ctx, func(a *app.App) error { return runWorker(ctx, a) })
	case "migrate":
		err = withApp(ctx, func(*app.App) error { return nil }) // New() migrates
	case "sync":
		err = withApp(ctx, func(a *app.App) error { return a.Sync(ctx) })
	case "emquant":
		err = emquantCommand(ctx, os.Args[2:], os.Stdout, os.Getenv)
	case "admin":
		err = withApp(ctx, func(a *app.App) error { return admin(ctx, a, os.Args[2:]) })
	case "harness":
		err = withApp(ctx, func(a *app.App) error { return harnessCmd(ctx, a, os.Args[2:]) })
	case "demo":
		err = demo(ctx, os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func withApp(ctx context.Context, fn func(*app.App) error) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	a, err := app.New(ctx, c)
	if err != nil {
		return err
	}
	defer a.Close()
	return fn(a)
}

func serveAPI(ctx context.Context, a *app.App) error {
	if _, err := a.Engine.NewRiver(a.Pool, false, 0); err != nil {
		return err
	}
	h, err := a.HTTP()
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: a.Cfg.APIAddr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("vera api listening", "addr", a.Cfg.APIAddr, "env", a.Cfg.Env, "synthetic", a.Engine.Synthetic)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

func runWorker(ctx context.Context, a *app.App) error {
	c, err := a.Engine.NewRiver(a.Pool, true, a.Cfg.WorkerConcurrency, a.Watch.Register, a.Engine.Gateway.Register)
	if err != nil {
		return err
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	slog.Info("vera worker started", "concurrency", a.Cfg.WorkerConcurrency)
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Stop(sctx)
}
