package emquant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimeConfig connects the real vendor SDK, durable archive and shared
// account gate. AccountScope must be the same non-secret identifier for all
// clients of the account. Values and credentials come from the deployment's
// secret/configuration provider; they must not be logged or committed.
type RuntimeConfig struct {
	Library           string
	Username          string `json:"-"`
	Password          string `json:"-"`
	Database          *pgxpool.Pool
	ObjectClient      *s3.Client
	Bucket            string
	Prefix            string
	AccountScope      string
	MinInterval       time.Duration
	RateLimitCooldown time.Duration
}

// String and GoString reduce accidental exposure when a config is logged.
func (RuntimeConfig) String() string   { return "emquant.RuntimeConfig{redacted}" }
func (RuntimeConfig) GoString() string { return "emquant.RuntimeConfig{redacted}" }

// Runtime is an SDK collection component, not a registered research data
// provider. Source permissions, field semantics, DB lineage and snapshots
// must be verified and implemented by the data service before report use.
type Runtime struct {
	client    *Client
	collector Collector
}

// OpenRuntime requires real SDK, S3-compatible archive and persistent rate
// gate configuration. It performs the vendor login through the same shared
// gate and fails if the gate's state table is missing or unresolved. A
// successful login does not prove data coverage or usage permissions.
func OpenRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	store, err := NewS3RawStore(cfg.ObjectClient, cfg.Bucket, cfg.Prefix)
	if err != nil {
		return nil, err
	}
	gate := PostgreSQLRateGate{Pool: cfg.Database, Scope: cfg.AccountScope, MinInterval: cfg.MinInterval, RateLimitCooldown: cfg.RateLimitCooldown}
	var client *Client
	err = gate.Do(ctx, func() error {
		var e error
		client, e = Open(cfg.Library, cfg.Username, cfg.Password)
		return e
	})
	if err != nil {
		if client != nil {
			err = errors.Join(err, client.Close())
		}
		return nil, fmt.Errorf("emquant: open runtime: %w", err)
	}
	return &Runtime{client: client, collector: Collector{Fetcher: client, Store: store, Gate: gate}}, nil
}

// CollectQuote returns a source archive and unnormalized observation. It does
// not certify that the vendor field is real time or ready for a report.
func (r *Runtime) CollectQuote(ctx context.Context, codes, indicators, options string) (Quote, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return Quote{}, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectQuote(ctx, codes, indicators, options)
}

// CollectCSD and CollectCSS return archived, unnormalized vendor observations.
// The data service must select registered fields and validate each mapping.
func (r *Runtime) CollectCSD(ctx context.Context, codes, indicators, from, to, options string) (Cube, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return Cube{}, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectCSD(ctx, codes, indicators, from, to, options)
}

func (r *Runtime) CollectCSS(ctx context.Context, codes, indicators, options string) (Cube, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return Cube{}, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectCSS(ctx, codes, indicators, options)
}

func (r *Runtime) CollectDailyLiquidity(ctx context.Context, code, from, to string) ([]DailyLiquidity, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectDailyLiquidity(ctx, code, from, to)
}

func (r *Runtime) CollectStatementFields(ctx context.Context, code, period string, statementType int) (StatementFields, error) {
	if r == nil || r.client == nil {
		return StatementFields{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectStatementFields(ctx, code, period, statementType)
}

func (r *Runtime) CollectCFN(ctx context.Context, codes, content string, mode int, options string) (Cube, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return Cube{}, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectCFN(ctx, codes, content, mode, options)
}

func (r *Runtime) CollectOpenDates(ctx context.Context, market, from, to string) ([]string, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return nil, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectOpenDates(ctx, market, from, to)
}

func (r *Runtime) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
