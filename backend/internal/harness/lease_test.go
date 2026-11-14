package harness

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

func leaseTask(t *testing.T) (*Engine, *taskRow) {
	t.Helper()
	e, pool := testEngine(t)
	ws := workspace(t, pool)
	p := plan(t, e, ws, "stock_analysis", "DM0001.DEMO")
	id, err := e.CreateTask(context.Background(), ws, p.ID, p.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := e.loadTask(context.Background(), ws, id)
	if err != nil {
		t.Fatal(err)
	}
	return e, tr
}

func stepRow(t *testing.T, e *Engine, tr *taskRow, key string) (status string, attempt int) {
	t.Helper()
	err := db.InWorkspace(context.Background(), e.Pool, tr.Workspace, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT status, attempt FROM task_steps WHERE task_id=$1 AND step_key=$2`, tr.ID, key).Scan(&status, &attempt)
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

// A step leased by a live worker is not run twice; a finished step is never rerun.
func TestStepLeaseExcludesConcurrentWorkers(t *testing.T) {
	e, tr := leaseTask(t)
	ctx := context.Background()
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var first string
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		first, firstErr = step(ctx, e, tr, "x", func() (string, error) {
			calls.Add(1)
			close(entered)
			<-release
			return "A", nil
		})
	}()
	<-entered
	if _, err := step(ctx, e, tr, "x", func() (string, error) { calls.Add(1); return "B", nil }); !errors.Is(err, ErrStepBusy) {
		t.Fatalf("second worker: want ErrStepBusy, got %v", err)
	}
	close(release)
	wg.Wait()
	if firstErr != nil || first != "A" {
		t.Fatalf("first worker: %q %v", first, firstErr)
	}
	got, err := step(ctx, e, tr, "x", func() (string, error) { calls.Add(1); return "C", nil })
	if err != nil || got != "A" || calls.Load() != 1 {
		t.Fatalf("replay: %q %v calls=%d", got, err, calls.Load())
	}
}

// A worker whose lease expired (it looked dead) is taken over; when it comes back it cannot overwrite the result.
func TestStepLeaseTakeoverAndFencing(t *testing.T) {
	oldHB := stepHeartbeat
	stepHeartbeat = time.Hour // simulate a stalled worker: no renewals
	t.Cleanup(func() { stepHeartbeat = oldHB })
	e, tr := leaseTask(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var stale string
	var staleErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		stale, staleErr = step(ctx, e, tr, "y", func() (string, error) {
			close(entered)
			<-release
			return "stale", nil
		})
	}()
	<-entered
	if err := db.InWorkspace(ctx, e.Pool, tr.Workspace, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE task_steps SET lease_until = now() - interval '1 second' WHERE task_id=$1 AND step_key='y'`, tr.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := step(ctx, e, tr, "y", func() (string, error) { return "fresh", nil })
	if err != nil || fresh != "fresh" {
		t.Fatalf("takeover: %q %v", fresh, err)
	}
	close(release)
	wg.Wait()
	if staleErr != nil || stale != "fresh" {
		t.Fatalf("fenced worker must return the stored result, got %q %v", stale, staleErr)
	}
	if st, attempt := stepRow(t, e, tr, "y"); st != "SUCCEEDED" || attempt != 2 {
		t.Fatalf("row %s attempt %d", st, attempt)
	}
	stored, done, _ := readStep[string](ctx, e, tr, "y")
	if !done || stored != "fresh" {
		t.Fatalf("stored %q", stored)
	}
}

// V05: two workers running the same task concurrently (River redelivery while the first is alive) deliver one
// report, and no step makes a second successful model call.
func TestConcurrentRunsDeliverOnce(t *testing.T) {
	e, pool := testEngine(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	p := plan(t, e, ws, "stock_analysis", "DM0001.DEMO")
	id, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				err := e.Run(ctx, ws, id, 1)
				if !errors.Is(err, ErrStepBusy) {
					if err != nil {
						t.Error(err)
					}
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if s, _ := status(t, e, ws, id); s != "COMPLETED" {
		t.Fatalf("status %s", s)
	}
	var reports, kept, dupCalls int
	_ = db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM report_versions WHERE task_id=$1`, id).Scan(&reports)
		_ = tx.QueryRow(ctx, `SELECT count(*) - count(DISTINCT claim_ref) FROM claims WHERE task_id=$1`, id).Scan(&kept)
		return tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT step_key FROM model_calls WHERE task_id=$1 AND status='SUCCEEDED'
			GROUP BY step_key HAVING count(*) > 1) d`, id).Scan(&dupCalls)
	})
	if reports != 1 || kept != 0 || dupCalls != 0 {
		t.Fatalf("reports=%d duplicate claims=%d steps with repeated successful calls=%d", reports, kept, dupCalls)
	}
}
