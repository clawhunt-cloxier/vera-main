package emquant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const liquiditySupplementVersion = "choice-daily-liquidity-v1"
const liquidityArchiveDataset = "mkt.daily_bar"

var liquidityPeriodZone = time.FixedZone("Asia/Shanghai", 8*60*60)

type liquiditySupplementManifest struct {
	Version        string
	Code, From, To string
	Liquidity      RegisteredArchive
}

var _ dataservice.ResearchSupplementSource = (*BriefService)(nil)

// CollectResearchSupplement does not resolve the security again or request a
// quote; it adds only archived daily liquidity to a known research subject.
func (b *BriefService) CollectResearchSupplement(ctx context.Context, code string) (json.RawMessage, error) {
	if b == nil || b.Runtime == nil || !validBriefCode(code) {
		return nil, errors.New("emquant: invalid research supplement source or security")
	}
	collector, err := b.collector(b.LiquidityLicense)
	if err != nil {
		return nil, err
	}
	if err := pgx.BeginFunc(ctx, b.Runtime.database, func(tx pgx.Tx) error {
		return b.MixedProviderUse(ctx, tx, dataservice.SourceModel)
	}); err != nil {
		return nil, err
	}
	today := time.Now().In(liquidityPeriodZone)
	m := liquiditySupplementManifest{Version: liquiditySupplementVersion, Code: code,
		From: today.AddDate(0, 0, -45).Format(time.DateOnly), To: today.AddDate(0, 0, -1).Format(time.DateOnly)}
	rows, raw, err := collector.CollectDailyLiquidity(ctx, code, m.From, m.To)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("emquant: no registered daily liquidity for research")
	}
	m.Liquidity = raw
	return json.Marshal(m)
}

func decodeLiquiditySupplement(body json.RawMessage, code, product string, licenseID uuid.UUID, cutoff time.Time) (liquiditySupplementManifest, time.Time, time.Time, error) {
	var m liquiditySupplementManifest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || dec.Decode(new(any)) != io.EOF {
		return m, time.Time{}, time.Time{}, errors.New("emquant: invalid research supplement manifest")
	}
	raw := m.Liquidity
	if m.Version != liquiditySupplementVersion || m.Code != code || !validBriefCode(code) ||
		raw.ID == uuid.Nil || raw.DatasetID != liquidityArchiveDataset || raw.Product != product || product == "" || raw.LicenseID != licenseID ||
		cutoff.IsZero() || raw.Source.ReceivedAt.IsZero() || raw.Source.ArchivedAt.Before(raw.Source.ReceivedAt) || raw.Source.ArchivedAt.After(cutoff) {
		return m, time.Time{}, time.Time{}, errors.New("emquant: research supplement manifest scope mismatch")
	}
	start, end, err := dailyLiquidityRange(code, m.From, m.To)
	if err != nil || !end.Equal(start.AddDate(0, 0, 44)) || m.To >= raw.Source.ReceivedAt.In(liquidityPeriodZone).Format(time.DateOnly) {
		return m, time.Time{}, time.Time{}, errors.New("emquant: invalid research supplement period")
	}
	expected, _ := json.Marshal(request{Method: "csd", Codes: code, Indicators: dailyIndicators, From: m.From, To: m.To, Options: dailyOptions})
	if raw.Source.RequestFingerprint != digest(expected) {
		return m, time.Time{}, time.Time{}, errors.New("emquant: research supplement request mismatch")
	}
	return m, start, end, nil
}

func (b *BriefService) ReplayResearchSupplement(ctx context.Context, tx pgx.Tx, code string, cutoff time.Time, manifest json.RawMessage, use dataservice.SourceUse) (dataservice.ResearchSupplement, error) {
	var out dataservice.ResearchSupplement
	if b == nil || tx == nil || (use != dataservice.SourceStorage && use != dataservice.SourceModel && use != dataservice.SourceDisplay) {
		return out, errors.New("emquant: invalid research supplement replay")
	}
	m, start, end, err := decodeLiquiditySupplement(manifest, code, b.Product, b.LiquidityLicense, cutoff)
	if err != nil {
		return out, err
	}
	// A report derived using the model cannot remain available after model
	// permission is withdrawn, even when this request is for display only.
	if err := b.MixedProviderUse(ctx, tx, dataservice.SourceModel); err != nil {
		return out, err
	}
	if err := dataservice.RequireSourceUse(ctx, tx, dataservice.SourceUseRequest{
		LicenseID: b.LiquidityLicense, ProviderID: b.ProviderID, Product: b.Product,
		DatasetID: liquidityArchiveDataset, Method: "CSD", Fields: strings.Split(dailyIndicators, ","),
		Codes: []string{code}, Use: dataservice.SourceModel,
	}); err != nil {
		return out, err
	}
	raw := m.Liquidity
	ref := raw.Source
	var archiveID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM raw_objects WHERE id=$1 AND provider_id=$2 AND dataset_id=$3
		AND stored_path=$4 AND content_hash=$5 AND request_fingerprint=$6 AND fetched_at=$7 AND archived_at=$8
		AND registered_at<=$9 AND archive_format='emquant-sdk-decoded-v2' AND source_license_id=$10 FOR SHARE`, raw.ID, b.ID(), raw.DatasetID,
		ref.BodyRef, ref.BodySHA256, ref.RequestFingerprint, ref.ReceivedAt.Truncate(time.Microsecond), ref.ArchivedAt.Truncate(time.Microsecond), cutoff, b.LiquidityLicense).Scan(&archiveID); err != nil {
		return out, err
	}
	if archiveID != raw.ID {
		return out, errors.New("emquant: research supplement archive identity mismatch")
	}
	body, err := b.Runtime.ReadArchive(ctx, ref)
	if err != nil {
		return out, err
	}
	cube, _, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return out, err
	}
	rows, err := mapDailyLiquidity(cube, ref, code, start, end)
	if err != nil {
		return out, err
	}
	if len(rows) == 0 {
		return out, errors.New("emquant: empty research supplement")
	}
	out.Title = "Choice 日成交观察"
	out.Evidence = dataservice.ResearchSupplementEvidence{ID: archiveID, DatasetID: raw.DatasetID,
		ContentHash: ref.BodySHA256, ReceivedAt: ref.ReceivedAt, Basis: b.Basis()[liquidityArchiveDataset], Locator: "$.data.Values"}
	for _, row := range rows {
		out.Rows = append(out.Rows, dataservice.ResearchLiquidityRow{Date: row.TradeDate,
			VolumeShares: &row.VolumeShares, AmountCNY: &row.AmountCNY, TurnoverRatio: row.TurnoverRatio})
	}
	return out, nil
}
