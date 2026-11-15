package emquant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RateGate serializes vendor calls. Production wiring must require a gate
// shared by every process using the same vendor account, including other
// applications. This interface does not imply the gate has been configured.
type RateGate interface {
	Do(context.Context, func() error) error
}

// PostgreSQLRateGate keeps one account's calls serialized across processes.
// The account scope must be a stable, non-secret identifier. Its state table
// must be provisioned by a reviewed migration before use; no table is created
// or silently bypassed by this component. A stricter verified account limit
// must be configured when the product or account requires it.
type PostgreSQLRateGate struct {
	Pool              *pgxpool.Pool
	Scope             string
	MinInterval       time.Duration
	RateLimitCooldown time.Duration
}

var ErrRateGateRecovery = errors.New("emquant: rate gate has an unresolved in-flight request")

// Do holds a session advisory lock across the native request. Before the
// call, it durably marks the account in flight. If the session is lost while
// the SDK is executing, a different worker cannot know whether that call is
// still running, so the next holder fails closed until safe recovery. Canceled
// callers that never enter the function do not contact the vendor.
func (g PostgreSQLRateGate) Do(ctx context.Context, call func() error) (result error) {
	const minInterval = (time.Minute + 699) / 700
	if g.Pool == nil || g.Scope == "" || strings.TrimSpace(g.Scope) != g.Scope || len(g.Scope) > 128 || g.MinInterval < minInterval || g.MinInterval > 24*time.Hour || g.RateLimitCooldown < g.MinInterval || g.RateLimitCooldown > 24*time.Hour || call == nil {
		return errors.New("emquant: invalid account rate gate configuration")
	}
	intervalMicros := (g.MinInterval.Nanoseconds() + 999) / 1000
	conn, err := g.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("emquant: acquire rate gate: %w", err)
	}
	// Failed lock acquisition may have raced with cancellation after the lock
	// was granted. Destroy that session rather than returning a held lock to
	// the pool. A cleanup failure is handled the same way.
	locked := false
	inFlight := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		destroy := !locked
		if inFlight {
			cooldownMicros := intervalMicros
			if errors.Is(result, ErrRateLimited) {
				cooldownMicros = (g.RateLimitCooldown.Nanoseconds() + 999) / 1000
			}
			tag, e := conn.Exec(cleanup, `UPDATE emquant_rate_state SET in_flight=false, next_allowed=clock_timestamp()+$2::bigint*interval '1 microsecond' WHERE scope=$1`, g.Scope, cooldownMicros)
			if e == nil && tag.RowsAffected() != 1 {
				e = errors.New("missing rate state")
			}
			if e != nil {
				result = errors.Join(result, fmt.Errorf("emquant: finish rate gate: %w", e))
				destroy = true
			}
		}
		if locked && !destroy {
			var unlocked bool
			e := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1, 5478451))`, g.Scope).Scan(&unlocked)
			if e != nil || !unlocked {
				result = errors.Join(result, errors.New("emquant: release rate gate failed"))
				destroy = true
			}
		}
		if destroy {
			_ = conn.Hijack().Close(cleanup)
		} else {
			conn.Release()
		}
	}()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 5478451))`, g.Scope); err != nil {
		return fmt.Errorf("emquant: lock rate gate: %w", err)
	}
	locked = true
	if _, err := conn.Exec(ctx, `INSERT INTO emquant_rate_state(scope, in_flight, next_allowed) VALUES($1,false,clock_timestamp()) ON CONFLICT(scope) DO NOTHING`, g.Scope); err != nil {
		return fmt.Errorf("emquant: initialize rate gate: %w", err)
	}
	var pending bool
	if err := conn.QueryRow(ctx, `SELECT in_flight FROM emquant_rate_state WHERE scope=$1`, g.Scope).Scan(&pending); err != nil {
		return fmt.Errorf("emquant: read rate gate state: %w", err)
	}
	if pending {
		return ErrRateGateRecovery
	}
	for {
		var waitMicros int64
		if err := conn.QueryRow(ctx, `SELECT GREATEST(0, CEIL(EXTRACT(EPOCH FROM (next_allowed-clock_timestamp()))*1000000))::bigint FROM emquant_rate_state WHERE scope=$1`, g.Scope).Scan(&waitMicros); err != nil {
			return fmt.Errorf("emquant: read rate gate: %w", err)
		}
		if waitMicros == 0 {
			break
		}
		if waitMicros > int64((24*time.Hour)/time.Microsecond)+1000000 {
			return errors.New("emquant: invalid rate gate cooldown")
		}
		timer := time.NewTimer(time.Duration(waitMicros) * time.Microsecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tag, err := conn.Exec(ctx, `UPDATE emquant_rate_state SET in_flight=true WHERE scope=$1`, g.Scope)
	if err != nil {
		return fmt.Errorf("emquant: mark rate gate in flight: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("emquant: missing rate gate state")
	}
	inFlight = true
	if err := ctx.Err(); err != nil {
		return err
	}
	return call()
}
