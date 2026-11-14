package emquant

import (
	"context"
	"encoding/json"
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
	store     *S3RawStore
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
	return &Runtime{client: client, collector: Collector{Fetcher: client, Store: store, Gate: gate}, store: store}, nil
}

// ReadArchive reads back the exact object addressed by the source reference,
// checks its hash, format, request binding and observation time, then returns
// its decoded bytes for deterministic mapping. The data service still needs
// to authenticate the reference and its publication time against its registry;
// a hash by itself is not proof of vendor origin or permission.
func (r *Runtime) ReadArchive(ctx context.Context, ref ArchiveRef) ([]byte, error) {
	if r == nil || r.store == nil || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return nil, errors.New("emquant: missing archive provenance")
	}
	key, err := r.store.key(ref.BodySHA256)
	if err != nil || ref.BodyRef != "s3://"+r.store.bucket+"/"+key {
		return nil, errors.New("emquant: archive location mismatch")
	}
	body, err := r.store.Get(ctx, ref.BodySHA256)
	if err != nil {
		return nil, err
	}
	_, observed, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return nil, err
	}
	var envelope archiveBody
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Format != "emquant-sdk-decoded-v2" || envelope.RequestFingerprint != ref.RequestFingerprint || !observed.Equal(ref.ReceivedAt) {
		return nil, errors.New("emquant: archive provenance mismatch")
	}
	return body, nil
}

// ReadCodeTableArchive checks object identity and returns the vendor's
// unselected code candidates; caller-supplied provenance still needs registry
// and permission verification before the table can be used by research.
func (r *Runtime) ReadCodeTableArchive(ctx context.Context, ref ArchiveRef, codes string) (Table, error) {
	if err := validateStockCodeQuery(codes); err != nil {
		return Table{}, err
	}
	return r.readTableArchive(ctx, ref, request{Method: "cec", Codes: codes, Options: stockCodeOptions})
}

func (r *Runtime) ReadNameTableArchive(ctx context.Context, ref ArchiveRef, name string) (Table, error) {
	options, err := stockNameOptions(name)
	if err != nil {
		return Table{}, err
	}
	return r.readTableArchive(ctx, ref, request{Method: "ctr", Codes: "SECUMATCH", Indicators: stockNameIndicators, Options: options})
}

func (r *Runtime) readTableArchive(ctx context.Context, ref ArchiveRef, query request) (Table, error) {
	if r == nil || r.store == nil || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return Table{}, errors.New("emquant: missing table archive provenance")
	}
	req, err := json.Marshal(query)
	if err != nil || digest(req) != ref.RequestFingerprint {
		return Table{}, errors.New("emquant: table request provenance mismatch")
	}
	key, err := r.store.key(ref.BodySHA256)
	if err != nil || ref.BodyRef != "s3://"+r.store.bucket+"/"+key {
		return Table{}, errors.New("emquant: table archive location mismatch")
	}
	body, err := r.store.Get(ctx, ref.BodySHA256)
	if err != nil {
		return Table{}, err
	}
	table, observed, fingerprint, err := DecodeTableArchive(body, ref.BodySHA256)
	if err != nil || !observed.Equal(ref.ReceivedAt) || fingerprint != ref.RequestFingerprint {
		return Table{}, errors.New("emquant: table archive provenance mismatch")
	}
	return table, nil
}

// MapQuoteFromArchive rereads the archived response before applying a caller's
// proposed mapping policy. Policy references must be independently validated
// by the data service before any result may be published or called real time.
func (r *Runtime) MapQuoteFromArchive(ctx context.Context, ref ArchiveRef, code string, cutoff time.Time, policy QuoteMappingPolicy) (MappedQuote, error) {
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return MappedQuote{}, err
	}
	return MapArchivedQuote(body, ref, code, cutoff, policy)
}

// MapCalendarFromArchive maps only the vendor's returned open dates. The
// resulting records retain archive lineage and still require the data service
// to publish them with a market-aware key and an actual database commit time.
func (r *Runtime) MapCalendarFromArchive(ctx context.Context, ref ArchiveRef, market, from, to string) ([]MappedRecord, error) {
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return nil, err
	}
	return MapOpenDates(body, ref, market, from, to)
}

// MapStatementFromArchive recovers only the two observed monetary fields for
// the requested period and statement type. It does not establish a filing date,
// revision, historical point-in-time correctness, or complete fin.statement.
func (r *Runtime) MapStatementFromArchive(ctx context.Context, ref ArchiveRef, code, period string, statementType int) (StatementFields, error) {
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return StatementFields{}, err
	}
	cube, _, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return StatementFields{}, err
	}
	return mapStatementFields(cube, ref, code, period, statementType)
}

// MapIndustryFromArchive recovers one requested level. It does not establish
// effective intervals, complete hierarchy or historical publication semantics.
func (r *Runtime) MapIndustryFromArchive(ctx context.Context, ref ArchiveRef, code, asOf string, level int) (IndustryObservation, error) {
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return IndustryObservation{}, err
	}
	cube, _, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return IndustryObservation{}, err
	}
	return mapIndustryLevel(cube, ref, code, asOf, level)
}

// MapMarginFromArchive retains the unnormalized vendor cells and their
// source locations; it does not establish units or publication timing.
func (r *Runtime) MapMarginFromArchive(ctx context.Context, ref ArchiveRef, code, from, to string) (MarginObservations, error) {
	start, end, err := marginRange(code, from, to)
	if err != nil {
		return MarginObservations{}, err
	}
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return MarginObservations{}, err
	}
	cube, _, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return MarginObservations{}, err
	}
	return mapMarginObservations(cube, ref, code, start, end)
}

func (r *Runtime) CollectMarginObservations(ctx context.Context, code, from, to string) (MarginObservations, error) {
	if r == nil || r.client == nil {
		return MarginObservations{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectMarginObservations(ctx, code, from, to)
}

// MapSecurityFromArchive recovers partial security basics from the exact
// archived response; it does not establish the security's trading status or
// the historical validity of its short name.
func (r *Runtime) MapSecurityFromArchive(ctx context.Context, ref ArchiveRef, code string) (SecurityBasics, error) {
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return SecurityBasics{}, err
	}
	cube, _, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return SecurityBasics{}, err
	}
	return mapSecurityBasics(cube, ref, code)
}

// UsageStatistics is a read-only account diagnostic, not a research source.
// It goes through the account gate and is not archived or sent to a model.
func (r *Runtime) UsageStatistics(ctx context.Context, function, from, to string) (Table, error) {
	if r == nil || r.client == nil {
		return Table{}, errors.New("emquant: runtime unavailable")
	}
	if _, err := usageOptions(function, from, to); err != nil {
		return Table{}, err
	}
	var table Table
	if err := r.collector.callVendor(ctx, func() error { var err error; table, err = r.client.UsageStatistics(function, from, to); return err }); err != nil {
		return Table{}, err
	}
	if err := ctx.Err(); err != nil {
		return Table{}, err
	}
	if err := validateTable(table); err != nil {
		return Table{}, err
	}
	return table, nil
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

func (r *Runtime) CollectIndustryLevel(ctx context.Context, code, asOf string, level int) (IndustryObservation, error) {
	if r == nil || r.client == nil {
		return IndustryObservation{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectIndustryLevel(ctx, code, asOf, level)
}

// CollectDisclosureObservation preserves vendor disclosure metadata without
// asserting an association with a particular financial statement version.
func (r *Runtime) CollectDisclosureObservation(ctx context.Context, code, period string) (DisclosureObservation, error) {
	if r == nil || r.client == nil {
		return DisclosureObservation{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectDisclosureObservation(ctx, code, period)
}

// MapDisclosureFromArchive checks archive and request identity before mapping.
func (r *Runtime) MapDisclosureFromArchive(ctx context.Context, ref ArchiveRef, code, period string) (DisclosureObservation, error) {
	body, err := r.ReadArchive(ctx, ref)
	if err != nil {
		return DisclosureObservation{}, err
	}
	cube, _, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return DisclosureObservation{}, err
	}
	return mapDisclosureObservation(cube, ref, code, period)
}

func (r *Runtime) CollectSecurityBasics(ctx context.Context, code string) (SecurityBasics, error) {
	if r == nil || r.client == nil {
		return SecurityBasics{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectSecurityBasics(ctx, code)
}

func (r *Runtime) CollectStockCodeCandidates(ctx context.Context, codes string) (Table, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return Table{}, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectStockCodeCandidates(ctx, codes)
}

func (r *Runtime) CollectStockNameCandidates(ctx context.Context, name string) (Table, ArchiveRef, error) {
	if r == nil || r.client == nil {
		return Table{}, ArchiveRef{}, errors.New("emquant: runtime unavailable")
	}
	return r.collector.CollectStockNameCandidates(ctx, name)
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
