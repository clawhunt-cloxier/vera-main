package emquant

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestPostgreSQLRateGateSerializesAndRecovers(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	// This is a test-only schema. Production must use an agreed migration.
	if _, err := pool.Exec(ctx, `CREATE TABLE emquant_rate_state (scope text PRIMARY KEY, in_flight boolean NOT NULL, next_allowed timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	gate := PostgreSQLRateGate{Pool: pool, Scope: "synthetic-account", MinInterval: 90 * time.Millisecond}
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
	called := false
	gate := PostgreSQLRateGate{Pool: pool, Scope: "synthetic-account", MinInterval: time.Second}
	if err := gate.Do(context.Background(), func() error { called = true; return nil }); err == nil || called {
		t.Fatal("missing persistent state allowed vendor call")
	}
}
