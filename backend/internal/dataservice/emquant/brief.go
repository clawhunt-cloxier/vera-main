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

// BriefService publishes only current observations with confirmed field units.
// It never promotes unverified TIME/status fields to realtime facts.
type BriefService struct {
	Runtime                           *Runtime
	Catalog                           *dataservice.Catalog
	ProviderID, Product               string
	SecurityLicense, LiquidityLicense uuid.UUID
	Codes                             []string
}

type BriefManifest struct {
	Version             string
	Code, From, To      string
	Security, Liquidity RegisteredArchive
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
func (b *BriefService) permitted(code string) bool {
	if !briefCode.MatchString(code) {
		return false
	}
	for _, c := range b.Codes {
		if c == code {
			return true
		}
	}
	return false
}
func (b *BriefService) Ready(ctx context.Context, tx pgx.Tx, code string, use dataservice.SourceUse) error {
	if !b.permitted(code) {
		return errors.New("emquant: security outside configured brief scope")
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
	c, err := b.collector(b.SecurityLicense)
	if err != nil {
		return nil, err
	}
	var out []dataservice.Entity
	for _, code := range b.Codes {
		if !b.permitted(code) {
			return nil, errors.New("emquant: invalid configured security")
		}
		basics, raw, err := c.CollectSecurityBasics(ctx, code)
		if err != nil {
			return nil, err
		}
		// Check display rights before publishing the observed name to entity search.
		err = pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error { return b.Ready(ctx, tx, code, dataservice.SourceDisplay) })
		if err != nil {
			return nil, err
		}
		out = append(out, dataservice.Entity{Kind: "stock", Code: code, Name: basics.ShortName, Status: "unknown", Aliases: []string{strings.Split(code, ".")[0], basics.ShortName}, Attrs: map[string]any{"identity_raw_id": raw.ID.String(), "identity_observed_at": raw.Source.ReceivedAt, "classification_verified": false}})
	}
	return out, nil
}

func (b *BriefService) Collect(ctx context.Context, code string) (BriefManifest, error) {
	m := BriefManifest{Version: ObservationBriefVersion, Code: code}
	if !b.permitted(code) {
		return m, errors.New("emquant: unconfigured brief security")
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

// verifyArchive requires exact licensed registry metadata, then replays the
// content-addressed object. No caller-provided normalized values are trusted.
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
	if m.Version != ObservationBriefVersion || m.Security.DatasetID != "ref.security" || m.Liquidity.DatasetID != "mkt.daily_bar" || m.Security.LicenseID != b.SecurityLicense || m.Liquidity.LicenseID != b.LiquidityLicense || m.Security.Product != b.Product || m.Liquidity.Product != b.Product {
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
	return out, b.Ready(ctx, tx, m.Code, use)
}

// Freeze binds only a consumed, unchanged, live task. Concurrent retries keep
// the first committed snapshot, and never replace it with a later observation.
func (b *BriefService) Freeze(ctx context.Context, ws, task uuid.UUID, epoch int64, m BriefManifest) error {
	return pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error {
		var code, version, status, mode string
		var actual int64
		var cutoff time.Time
		var hash *string
		var subject uuid.UUID
		var live bool
		err := tx.QueryRow(ctx, `SELECT e.code,e.id,t.skill_version,t.status,t.snapshot_mode,t.epoch,t.snapshot_manifest_hash,clock_timestamp(),t.deadline>clock_timestamp()
   FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
   JOIN entities e ON e.id=t.subject_id WHERE t.id=$1 AND t.workspace_id=$2 AND p.consumed_task_id=t.id AND NOT e.synthetic FOR UPDATE OF t`, task, ws).Scan(&code, &subject, &version, &status, &mode, &actual, &hash, &cutoff, &live)
		if err != nil {
			return err
		}
		if actual != epoch || !live || version != ObservationBriefVersion || mode != "collect_latest" || status != "COLLECTING" || code != m.Code {
			return errors.New("emquant: brief task changed")
		}
		if hash != nil {
			return nil
		}
		if _, err = b.Replay(ctx, tx, m, cutoff, dataservice.SourceStorage); err != nil {
			return err
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
	var m BriefManifest
	var body []byte
	var cutoff time.Time
	var h string
	err := tx.QueryRow(ctx, `SELECT s.manifest,s.cutoff,s.content_hash FROM observation_brief_snapshots s JOIN research_tasks t
 ON t.id=s.task_id AND t.workspace_id=s.workspace_id AND t.epoch=s.epoch AND t.subject_id=s.entity_id
 AND t.snapshot_manifest_hash=s.content_hash AND t.snapshot_as_of=s.cutoff
 WHERE s.workspace_id=$1 AND s.task_id=$2 AND t.skill_version=$3 AND t.status NOT IN ('CANCEL_REQUESTED','CANCELLED','FAILED','ABSTAINED','BLOCKED')`, ws, task, ObservationBriefVersion).Scan(&body, &cutoff, &h)
	if err != nil {
		return BriefObservation{}, err
	}
	if digest(body) != h || json.Unmarshal(body, &m) != nil {
		return BriefObservation{}, errors.New("emquant: corrupt brief manifest")
	}
	out, err := b.Replay(ctx, tx, m, cutoff, use)
	out.Hash = h
	return out, err
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

// WithDisplayCatalog holds current permissions while an API renders cached
// identities and report titles. Any removed or revoked scope fails closed.
func (b *BriefService) WithDisplayCatalog(ctx context.Context, fn func() error) error {
	return pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error {
		for _, code := range b.Codes {
			if err := b.Ready(ctx, tx, code, dataservice.SourceDisplay); err != nil {
				return err
			}
		}
		return fn()
	})

}
