package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const ObservationBriefVersion = "1.1.0-demo.2"
const MixedObservationVersion = "1.1.0-demo.4"

// RealtimeBriefVersion is the negotiated 0.4 single-stock brief. It is kept
// separate from the historical observation brief so an old worker cannot
// interpret a collect_latest task as the legacy daily observation flow.
const RealtimeBriefVersion = "1.1.0-demo.3"

// BriefService publishes current observations from the Choice API.
// It archives every vendor response for reproducibility; collection, display
// and model use are each gated by a current source_licenses grant.
type BriefService struct {
	Runtime                           *Runtime
	Catalog                           *dataservice.Catalog
	ProviderID, Product               string
	SecurityLicense, LiquidityLicense uuid.UUID
}

type BriefManifest struct {
	Version             string
	Code, From, To      string
	Security, Liquidity RegisteredArchive
	Supplement          []dataservice.ObservationRef `json:",omitempty"`
}
type BriefObservation struct {
	Manifest BriefManifest
	Name     string
	Rows     []DailyLiquidity
	Cutoff   time.Time
	Hash     string
}

var briefCode = regexp.MustCompile(`^[0-9]{6}\.(SH|SZ|BJ)$`)

func (b *BriefService) collector(id uuid.UUID) (RegisteredCollector, error) {
	return b.Runtime.RegisteredCollector(b.Catalog, b.ProviderID, b.Product, id)
}

// validBriefCode reports whether code is a well-formed A-share code.
func validBriefCode(code string) bool { return briefCode.MatchString(code) }

func (b *BriefService) Ready(ctx context.Context, tx pgx.Tx, code string, use dataservice.SourceUse) error {
	if !validBriefCode(code) {
		return errors.New("emquant: invalid A-share code format")
	}
	for _, g := range []dataservice.SourceUseRequest{
		{LicenseID: b.SecurityLicense, ProviderID: b.ProviderID, Product: b.Product, DatasetID: "ref.security", Method: "CSS", Fields: strings.Split(securityIndicators, ","), Codes: []string{code}, Use: use},
		{LicenseID: b.LiquidityLicense, ProviderID: b.ProviderID, Product: b.Product, DatasetID: "mkt.daily_bar", Method: "CSD", Fields: strings.Split(dailyIndicators, ","), Codes: []string{code}, Use: use},
	} {
		if err := dataservice.RequireSourceUse(ctx, tx, g); err != nil {
			return err
		}
	}
	return nil
}

// Provider methods register only verified current security identities. Canonical
// financial/daily records and capabilities stay empty until fully implemented.
func (b *BriefService) ID() string                 { return b.ProviderID }
func (b *BriefService) Name() string               { return "Choice / 东方财富" }
func (b *BriefService) Synthetic() bool            { return false }
func (b *BriefService) AllowModelProcessing() bool { return true }
func (b *BriefService) Basis() map[string]string {
	return map[string]string{"mkt.daily_bar": "Choice 日成交观察：成交量为股，成交额为元，换手率按百分数转换；不表示完整日线或实时行情"}
}
func (b *BriefService) Now() time.Time                                        { return time.Now().UTC() }
func (b *BriefService) Records(context.Context) ([]dataservice.Record, error) { return nil, nil }
func (b *BriefService) Capabilities(context.Context) ([]dataservice.Capability, error) {
	return nil, nil
}
func (b *BriefService) Entities(ctx context.Context) ([]dataservice.Entity, error) {
	// Entities are populated lazily via real-time search (searchEntities handler).
	return nil, nil
}

func (b *BriefService) Collect(ctx context.Context, code string) (BriefManifest, error) {
	m := BriefManifest{Version: ObservationBriefVersion, Code: code}
	if !validBriefCode(code) {
		return m, errors.New("emquant: invalid A-share code format")
	}
	// Exclude today's potentially partial session; returned CSD dates are vendor
	// period labels, never intraday event times or publication timestamps.
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	today := time.Now().In(zone)
	m.To = today.AddDate(0, 0, -1).Format("2006-01-02")
	m.From = today.AddDate(0, 0, -45).Format("2006-01-02")
	security, err := b.collector(b.SecurityLicense)
	if err != nil {
		return m, err
	}
	_, m.Security, err = security.CollectSecurityBasics(ctx, code)
	if err != nil {
		return m, err
	}
	liquidity, err := b.collector(b.LiquidityLicense)
	if err != nil {
		return m, err
	}
	rows, raw, err := liquidity.CollectDailyLiquidity(ctx, code, m.From, m.To)
	if err != nil {
		return m, err
	}
	if len(rows) == 0 {
		return m, errors.New("emquant: no dated liquidity observations")
	}
	m.Liquidity = raw
	return m, nil
}

// verifyArchive requires exact archived metadata before replaying content.
func (b *BriefService) verifyArchive(ctx context.Context, tx pgx.Tx, raw RegisteredArchive, cutoff time.Time) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM raw_objects WHERE id=$1 AND provider_id=$2 AND dataset_id=$3
  AND source_license_id=$4 AND stored_path=$5 AND content_hash=$6 AND request_fingerprint=$7
  AND fetched_at=$8 AND archived_at=$9 AND registered_at<=$10)`, raw.ID, b.ProviderID, raw.DatasetID, raw.LicenseID,
		raw.Source.BodyRef, raw.Source.BodySHA256, raw.Source.RequestFingerprint, raw.Source.ReceivedAt.Truncate(time.Microsecond), raw.Source.ArchivedAt.Truncate(time.Microsecond), cutoff).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("emquant: brief archive registry mismatch")
	}
	return nil
}
func (b *BriefService) Replay(ctx context.Context, tx pgx.Tx, m BriefManifest, cutoff time.Time, use dataservice.SourceUse) (BriefObservation, error) {
	out := BriefObservation{Manifest: m, Cutoff: cutoff}
	if m.Version != ObservationBriefVersion || len(m.Supplement) != 0 || m.Security.DatasetID != "ref.security" || m.Liquidity.DatasetID != "mkt.daily_bar" || m.Security.LicenseID != b.SecurityLicense || m.Liquidity.LicenseID != b.LiquidityLicense || m.Security.Product != b.Product || m.Liquidity.Product != b.Product {
		return out, errors.New("emquant: brief scope mismatch")
	}
	if err := b.Ready(ctx, tx, m.Code, use); err != nil {
		return out, err
	}
	for _, raw := range []RegisteredArchive{m.Security, m.Liquidity} {
		if err := b.verifyArchive(ctx, tx, raw, cutoff); err != nil {
			return out, err
		}
	}
	basics, err := b.Runtime.MapSecurityFromArchive(ctx, m.Security.Source, m.Code)
	if err != nil {
		return out, err
	}
	out.Name = basics.ShortName
	body, err := b.Runtime.ReadArchive(ctx, m.Liquidity.Source)
	if err != nil {
		return out, err
	}
	cube, _, err := DecodeArchive(body, m.Liquidity.Source.BodySHA256)
	if err != nil {
		return out, err
	}
	from, to, err := dailyLiquidityRange(m.Code, m.From, m.To)
	if err != nil {
		return out, err
	}
	if !to.Before(cutoff) {
		return out, errors.New("emquant: future brief period")
	}
	out.Rows, err = mapDailyLiquidity(cube, m.Liquidity.Source, m.Code, from, to)
	if err != nil {
		return out, err
	}
	if len(out.Rows) == 0 {
		return out, errors.New("emquant: empty brief")
	}
	return out, nil
}

// Freeze binds only a consumed, unchanged, live task. Concurrent retries keep
// the first committed snapshot, and never replace it with a later observation.
func (b *BriefService) Freeze(ctx context.Context, ws, task uuid.UUID, epoch int64, m BriefManifest) error {
	return b.freeze(ctx, ws, task, epoch, m, nil)
}

func (b *BriefService) FreezeMixed(ctx context.Context, ws, task uuid.UUID, epoch int64, m BriefManifest, store *dataservice.Store) error {
	if store == nil {
		return errors.New("emquant: missing closing data source")
	}
	return b.freeze(ctx, ws, task, epoch, m, store)
}

func (b *BriefService) freeze(ctx context.Context, ws, task uuid.UUID, epoch int64, m BriefManifest, store *dataservice.Store) error {
	return pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error {
		var code, version, status, mode string
		var actual int64
		var cutoff time.Time
		var hash *string
		var subject uuid.UUID
		var live bool
		var preview time.Time
		err := tx.QueryRow(ctx, `SELECT e.code,e.id,t.skill_version,t.status,t.snapshot_mode,t.epoch,t.snapshot_manifest_hash,clock_timestamp(),t.deadline>clock_timestamp(),p.as_of
   FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
   JOIN entities e ON e.id=t.subject_id WHERE t.id=$1 AND t.workspace_id=$2 AND p.consumed_task_id=t.id AND NOT e.synthetic FOR UPDATE OF t`, task, ws).Scan(&code, &subject, &version, &status, &mode, &actual, &hash, &cutoff, &live, &preview)
		if err != nil {
			return err
		}
		expectedVersion := ObservationBriefVersion
		if store != nil {
			expectedVersion = MixedObservationVersion
		}
		if actual != epoch || !live || version != expectedVersion || mode != "collect_latest" || status != "COLLECTING" || code != m.Code {
			return errors.New("emquant: brief task changed")
		}
		if hash != nil {
			return nil
		}
		if _, err = b.Replay(ctx, tx, m, cutoff, dataservice.SourceStorage); err != nil {
			return err
		}
		if store != nil {
			if err := b.MixedReady(ctx, tx, code, dataservice.SourceStorage); err != nil {
				return err
			}
			row, err := store.ObservationBar(ctx, tx, subject, code, preview, nil)
			if err != nil {
				return err
			}
			m.Version = MixedObservationVersion
			m.Supplement = []dataservice.ObservationRef{{ID: row.ID, DatasetID: row.DatasetID, ContentHash: row.ContentHash}}
		}
		body, err := json.Marshal(m)
		if err != nil {
			return err
		}
		h := digest(body)
		if _, err = tx.Exec(ctx, `INSERT INTO observation_brief_snapshots(workspace_id,task_id,epoch,entity_id,cutoff,content_hash,manifest) VALUES($1,$2,$3,$4,$5,$6,$7)`, ws, task, epoch, subject, cutoff, h, body); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE research_tasks SET snapshot_as_of=$4,snapshot_manifest_hash=$5,revision=revision+1 WHERE id=$1 AND workspace_id=$2 AND epoch=$3 AND status='COLLECTING' AND deadline>clock_timestamp()`, task, ws, epoch, cutoff, h)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("emquant: brief task expired")
		}
		return nil
	})
}
func (b *BriefService) Read(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID, use dataservice.SourceUse) (BriefObservation, error) {
	o, _, err := b.read(ctx, tx, ws, task, use, nil)
	return o, err
}

func (b *BriefService) ReadMixed(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID, use dataservice.SourceUse, store *dataservice.Store) (BriefObservation, []dataservice.StoredRecord, error) {
	if store == nil {
		return BriefObservation{}, nil, errors.New("emquant: missing TuShare store")
	}
	return b.read(ctx, tx, ws, task, use, store)
}

func (b *BriefService) MixedReady(ctx context.Context, tx pgx.Tx, code string, use dataservice.SourceUse) error {
	if err := b.Ready(ctx, tx, code, use); err != nil {
		return err
	}
	return b.MixedProviderUse(ctx, tx, use)
}

func (b *BriefService) MixedProviderUse(ctx context.Context, tx pgx.Tx, use dataservice.SourceUse) error {
	if b.Runtime == nil || b.ProviderID == "" || b.ProviderID == "tushare" || b.Product == "" {
		return errors.New("emquant: invalid mixed source")
	}
	var id string
	return tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 AND enabled AND NOT synthetic AND kind='vendor' AND ($2::text <> 'model' OR allow_model_processing) FOR SHARE`, b.ProviderID, string(use)).Scan(&id)
}

func (b *BriefService) read(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID, use dataservice.SourceUse, store *dataservice.Store) (BriefObservation, []dataservice.StoredRecord, error) {
	var m BriefManifest
	var body []byte
	var cutoff time.Time
	var h string
	version := ObservationBriefVersion
	if store != nil {
		version = MixedObservationVersion
	}
	var subject uuid.UUID
	var preview time.Time
	err := tx.QueryRow(ctx, `SELECT s.manifest,s.cutoff,s.content_hash,s.entity_id,t.as_of FROM observation_brief_snapshots s JOIN research_tasks t
 ON t.id=s.task_id AND t.workspace_id=s.workspace_id AND t.epoch=s.epoch AND t.subject_id=s.entity_id
 AND t.snapshot_manifest_hash=s.content_hash AND t.snapshot_as_of=s.cutoff
 WHERE s.workspace_id=$1 AND s.task_id=$2 AND t.skill_version=$3 AND t.status NOT IN ('CANCEL_REQUESTED','CANCELLED','FAILED','ABSTAINED','BLOCKED')`, ws, task, version).Scan(&body, &cutoff, &h, &subject, &preview)
	if err != nil {
		return BriefObservation{}, nil, err
	}
	if digest(body) != h || json.Unmarshal(body, &m) != nil || m.Version != version {
		return BriefObservation{}, nil, errors.New("emquant: corrupt brief manifest")
	}
	daily := m
	var records []dataservice.StoredRecord
	if store != nil {
		if len(m.Supplement) != 1 || m.Security.Product != b.Product || m.Liquidity.Product != b.Product {
			return BriefObservation{}, nil, errors.New("emquant: mixed scope mismatch")
		}
		if err := b.MixedReady(ctx, tx, m.Code, use); err != nil {
			return BriefObservation{}, nil, err
		}
		row, err := store.ObservationBar(ctx, tx, subject, m.Code, preview, &m.Supplement[0])
		if err != nil {
			return BriefObservation{}, nil, err
		}
		records = []dataservice.StoredRecord{row}
		daily.Version, daily.Supplement = ObservationBriefVersion, nil
	}
	out, err := b.Replay(ctx, tx, daily, cutoff, use)
	out.Manifest = m
	out.Hash = h
	return out, records, err
}
func (b *BriefService) WithSnapshot(ctx context.Context, ws, task uuid.UUID, use dataservice.SourceUse, fn func(BriefObservation) error) error {
	return pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error {
		out, err := b.Read(ctx, tx, ws, task, use)
		if err != nil {
			return fmt.Errorf("emquant: brief unavailable: %w", err)
		}
		return fn(out)
	})
}

// WithDisplayCatalog holds current display permissions while an API renders
// cached identities and report titles. Any removed or revoked display scope
// fails closed.
func (b *BriefService) WithDisplayCatalog(ctx context.Context, fn func() error) error {
	return pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT code FROM entities WHERE NOT synthetic`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			if err := rows.Scan(&code); err != nil {
				return err
			}
			if err := b.Ready(ctx, tx, code, dataservice.SourceDisplay); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return fn()
	})
}
