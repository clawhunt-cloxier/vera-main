package emquant

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestPostgreSQLRateGateSerializesAndRecovers(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	gate := PostgreSQLRateGate{Pool: pool, Scope: "synthetic-account", MinInterval: 90 * time.Millisecond, RateLimitCooldown: 200 * time.Millisecond}
	var mu sync.Mutex
	var active atomic.Int32
	var starts []time.Time
	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- gate.Do(ctx, func() error {
				if active.Add(1) != 1 {
					return errors.New("overlapping vendor calls")
				}
				mu.Lock()
				starts = append(starts, time.Now())
				mu.Unlock()
				active.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	if len(starts) != 3 || starts[1].Sub(starts[0]) < 80*time.Millisecond || starts[2].Sub(starts[1]) < 80*time.Millisecond {
		t.Fatal("account calls did not respect the shared interval")
	}
	if err := gate.Do(ctx, func() error { return &SDKError{Operation: "query", Code: 10000016} }); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("vendor rate error not propagated: %v", err)
	}
	// A fresh gate instance must observe the persisted cooldown as well.
	second := PostgreSQLRateGate{Pool: pool, Scope: gate.Scope, MinInterval: gate.MinInterval, RateLimitCooldown: gate.RateLimitCooldown}
	start := time.Now()
	if err := second.Do(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 180*time.Millisecond {
		t.Fatal("vendor rate-limit cooldown was not shared")
	}
	// Simulates the persistent state left when a process died during a call.
	if _, err := pool.Exec(ctx, `UPDATE emquant_rate_state SET in_flight=true, next_allowed=clock_timestamp()-interval '1 hour' WHERE scope=$1`, gate.Scope); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := gate.Do(ctx, func() error { called = true; return nil }); !errors.Is(err, ErrRateGateRecovery) || called {
		t.Fatal("unresolved in-flight call did not fail closed")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := gate.Do(canceled, func() error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled call reached vendor")
	}
	gate.MinInterval = time.Millisecond
	if err := gate.Do(ctx, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("unsafe configured rate reached vendor")
	}
}

func TestPostgreSQLRateGateRequiresStateTable(t *testing.T) {
	pool := dbtest.New(t)
	if _, err := pool.Exec(context.Background(), `DROP TABLE emquant_rate_state`); err != nil {
		t.Fatal(err)
	}
	called := false
	gate := PostgreSQLRateGate{Pool: pool, Scope: "synthetic-account", MinInterval: time.Second, RateLimitCooldown: time.Second}
	if err := gate.Do(context.Background(), func() error { called = true; return nil }); err == nil || called {
		t.Fatal("missing persistent state allowed vendor call")
	}
}

func TestRateStateMigrationProtectsAccountState(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var allowed bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('vera_app', 'emquant_rate_state', 'UPDATE')`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("workspace role can alter account gate: allowed=%v err=%v", allowed, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO emquant_rate_state(scope,in_flight,next_allowed) VALUES('synthetic-account',true,clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../../platform/db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("rollback discarded persistent account fencing state")
	}
	var inFlight bool
	if err := pool.QueryRow(ctx, `SELECT in_flight FROM emquant_rate_state WHERE scope='synthetic-account'`).Scan(&inFlight); err != nil || !inFlight {
		t.Fatalf("rollback did not retain in-flight state: in_flight=%v err=%v", inFlight, err)
	}
}

// The database session can disappear while a synchronous native SDK call is
// still executing. A newly acquired lock must not be treated as proof that the
// old call stopped: its committed in-flight marker must fence the new worker.
func TestPostgreSQLRateGateSessionLossWhileCallRuns(t *testing.T) {
	pool := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gate := PostgreSQLRateGate{Pool: pool, Scope: "synthetic-session-loss", MinInterval: 90 * time.Millisecond, RateLimitCooldown: 200 * time.Millisecond}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	var releaseOnce sync.Once
	go func() {
		finished <- gate.Do(ctx, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	defer releaseOnce.Do(func() { close(release) })
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("first call did not start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var pid int
	if err := pool.QueryRow(ctx, `SELECT l.pid FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE l.locktype='advisory' AND l.granted AND a.datname=current_database()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate gate session: %v", err)
	}
	called := false
	if err := gate.Do(ctx, func() error { called = true; return nil }); !errors.Is(err, ErrRateGateRecovery) || called {
		t.Fatalf("lost session allowed another vendor call: %v", err)
	}
	var inFlight bool
	if err := pool.QueryRow(ctx, `SELECT in_flight FROM emquant_rate_state WHERE scope=$1`, gate.Scope).Scan(&inFlight); err != nil || !inFlight {
		t.Fatalf("lost session did not retain fencing state: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("lost session reported success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
