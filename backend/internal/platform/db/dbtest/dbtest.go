// Package dbtest starts one embedded PostgreSQL per test binary and hands out fresh databases.
package dbtest

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

var (
	once     sync.Once
	baseDSN  string
	startErr error
	counter  int
	mu       sync.Mutex
)

func start() {
	port, err := freePort()
	if err != nil {
		startErr = err
		return
	}
	root := os.Getenv("VERA_TEST_PG_DIR")
	if root == "" {
		root = filepath.Join(os.TempDir(), "vera-embedded-pg")
	}
	dir, err := os.MkdirTemp("", "vera-pg-run-")
	if err != nil {
		startErr = err
		return
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).Port(uint32(port)).
		CachePath(filepath.Join(root, "cache")).RuntimePath(dir).
		Logger(nil))
	if err := pg.Start(); err != nil {
		startErr = fmt.Errorf("start embedded postgres: %w", err)
		return
	}
	baseDSN = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d", port)
}

// New returns a migrated pool on a fresh database. Skips when VERA_SKIP_DB_TESTS=1.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("VERA_SKIP_DB_TESTS") == "1" {
		t.Skip("VERA_SKIP_DB_TESTS=1")
	}
	once.Do(start)
	if startErr != nil {
		t.Fatal(startErr)
	}
	mu.Lock()
	counter++
	name := fmt.Sprintf("t%d_%d", os.Getpid(), counter)
	mu.Unlock()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, baseDSN+"/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	_ = admin.Close(ctx)
	pool, err := db.Open(ctx, baseDSN+"/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// DSN exposes the server address for tests that need a second connection.
func DSN() string { return baseDSN }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
