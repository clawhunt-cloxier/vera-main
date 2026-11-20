package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

const (
	sharesToLotsShift = -2
	yuanToWanShift    = -4
)

func observationDisplayValue(raw string, shift int32) (string, error) {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return "", err
	}
	return v.Shift(shift).StringFixed(2), nil
}

// Convert the frozen table only for display. HTML and PDF share this path
// for both historical and newly delivered reports.
func formatObservationTable(t *facts.Table) error {
	if t.Title != "Choice 日成交观察（非实时）" || !slices.Equal(t.Columns, []string{"厂商返回日期", "成交量（股）", "成交额（元）", "换手率（%）"}) {
		return nil
	}
	for _, row := range t.Rows {
		if len(row) != len(t.Columns) {
			return errors.New("unexpected observation table row width")
		}
		for i, shift := range []int32{sharesToLotsShift, yuanToWanShift} {
			if raw := row[i+1]; raw != nil {
				v, err := observationDisplayValue(*raw, shift)
				if err != nil {
					return fmt.Errorf("invalid observation value in %s: %w", t.Columns[i+1], err)
				}
				row[i+1] = facts.Ptr(v)
			}
		}
	}
	t.Columns[1], t.Columns[2] = "成交量（手）", "成交额（万元）"
	return nil
}

func (s *Server) briefEvidence(w http.ResponseWriter, r *http.Request) (bool, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return true, err
	}
	_, ws := principal(r)
	var task uuid.UUID
	var version string
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT s.task_id,t.skill_version FROM observation_brief_snapshots s
			JOIN report_versions v ON v.task_id=s.task_id AND v.workspace_id=s.workspace_id
			JOIN research_tasks t ON t.id=v.task_id AND t.workspace_id=v.workspace_id
			CROSS JOIN LATERAL (SELECT convert_from(s.manifest,'UTF8')::jsonb AS m) manifest
			WHERE t.skill_version=ANY($2::text[]) AND (m->'Liquidity'->>'ID'=$1 OR m->'Security'->>'ID'=$1
			OR EXISTS(SELECT 1 FROM jsonb_array_elements(m->'Supplement') x WHERE x->>'ID'=$1))
			ORDER BY (t.skill_version=$3) DESC,v.delivered_at DESC LIMIT 1`, id.String(), s.historyVersions(), emquant.ObservationBriefVersion).Scan(&task, &version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.realtimeEvidence(w, r, ws, id)
	}
	if err != nil {
		return true, err
	}
	if s.Engine.Brief == nil || s.Engine.Brief.Runtime == nil {
		return true, errs.Unavailable("SOURCE_UNAVAILABLE", "真实证据来源当前不可读取")
	}
	display := func(o emquant.BriefObservation) error {
		raw := o.Manifest.Liquidity
		label := "日成交观察（非完整日线）"
		var lines []string
		switch id {
		case o.Manifest.Security.ID:
			raw = o.Manifest.Security
			label = "当前证券名称观察"
			lines = []string{o.Manifest.Code + " · " + o.Name}
		case o.Manifest.Liquidity.ID:
			for _, row := range o.Rows {
				volume, err := observationDisplayValue(row.VolumeShares, sharesToLotsShift)
				if err != nil {
					return err
				}
				amount, err := observationDisplayValue(row.AmountCNY, yuanToWanShift)
				if err != nil {
					return err
				}
				line := fmt.Sprintf("%s 成交量 %s 手，成交额 %s 万元", row.TradeDate, volume, amount)
				if row.TurnoverRatio != nil {
					line += fmt.Sprintf("，换手率 %s%%", decimal.RequireFromString(*row.TurnoverRatio).Shift(2).String())
				}
				lines = append(lines, line)
			}
		default:
			return errs.NotFound()
		}
		loc := "原始归档 SHA-256 " + raw.Source.BodySHA256 + "；$.data.Values（与 Codes/Indicators/Dates 轴对应）"
		excerpt := strings.Join(lines, "\n")
		basis := "来自 Choice，仅反映采集时取得的版本。采集时间不代表首次发布时间，数据为非实时观察。"
		httpx.JSON(w, r, 200, Evidence{ID: id, DatasetID: raw.DatasetID, Provider: "Choice / 东方财富", Origin: "vendor_derived", SourceTier: "L2", Title: o.Name + " · " + label, Label: label, Basis: &basis, ReceivedAt: &raw.Source.ReceivedAt, AvailableAt: o.Cutoff, LatencyMode: "unknown", Locator: &loc, Excerpt: &excerpt, ContentHash: raw.Source.BodySHA256})
		return nil
	}
	if version == harness.MixedObservationVersion {
		var rendered bool
		err = s.Engine.WithMixedDisplay(r.Context(), ws, task, func(o emquant.BriefObservation, records []dataservice.StoredRecord) error {
			rendered = true
			for _, rec := range records {
				if rec.ID == id && rec.ProviderID == "tushare" {
					// Render only the frozen closing price actually bound to
					// this brief. All labels come from the same manifest.
					price, ok := rec.Payload["close"].(string)
					day, hasDay := rec.Payload["trade_date"].(string)
					if !ok || !hasDay {
						return errs.NotFound()
					}
					label, unit := "不复权收盘价", "元/股"
					basis := "TuShare 不复权收盘日线"
					httpx.JSON(w, r, 200, Evidence{ID: id, DatasetID: rec.DatasetID, Provider: "TuShare Pro", Origin: rec.Origin, SourceTier: rec.SourceTier, Title: o.Name + " · " + label, Label: label, Value: &price, Unit: &unit, Basis: &basis, Period: &day, AvailableAt: rec.AvailableAt, PublishedAt: rec.PublishedAt, Locator: rec.RawLocator, ContentHash: rec.ContentHash})
					return nil
				}
			}
			return display(o)
		})
		if err != nil && !rendered {
			return true, errs.Unavailable("SOURCE_UNAVAILABLE", "双来源证据当前不可读取").Wrap(err)
		}
	} else {
		err = s.withDaily(r, ws, task, display)
	}
	return true, err
}

// realtimeEvidence starts with a workspace-scoped delivered report lookup,
// then reauthorizes the exact frozen quote while emitting its evidence.
func (s *Server) realtimeEvidence(w http.ResponseWriter, r *http.Request, ws, evidenceID uuid.UUID) (bool, error) {
	var task uuid.UUID
	err := db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT v.task_id FROM report_versions v
			JOIN research_tasks t ON t.id=v.task_id AND t.workspace_id=v.workspace_id
			WHERE t.skill_version=$2 AND EXISTS (
				SELECT 1 FROM jsonb_array_elements(v.document->'claims') c,
				jsonb_array_elements_text(c->'evidence_refs') e WHERE e=$1)
			ORDER BY v.delivered_at DESC LIMIT 1`, evidenceID.String(), harness.RealtimeBriefVersion).Scan(&task)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if s.Engine.Brief == nil || s.Engine.Brief.Runtime == nil {
		return true, errs.Unavailable("SOURCE_UNAVAILABLE", "真实证据来源当前不可读取")
	}
	err = s.withQuote(r, ws, task, func(view emquant.QuoteView) error {
		for _, q := range view.Quotes {
			if q.StageID != evidenceID {
				continue
			}
			basis := "冻结的 Choice 行情快照；时间分别表示行情事件、系统接收和数据库可用时点"
			state := map[string]string{"continuous": "连续交易", "closed": "非交易状态", "suspended": "停牌", "unknown": "未确认"}[q.MarketState]
			excerpt := fmt.Sprintf("最新价 %s 元；交易状态 %s；行情事件 %s；接收 %s；可用 %s", q.Price, state, q.EventAt.UTC().Format(time.RFC3339), q.ReceivedAt.UTC().Format(time.RFC3339), q.AvailableAt.UTC().Format(time.RFC3339))
			loc := "原始归档 SHA-256 " + q.RawSHA256 + "；价格 " + q.Locators["NOW"] + "；时间 " + q.Locators["TIME"]
			eventAt, receivedAt, unit := q.EventAt, q.ReceivedAt, "CNY/share"
			httpx.JSON(w, r, 200, Evidence{ID: evidenceID, DatasetID: "mkt.intraday_quote", Provider: q.ProviderID,
				Origin: "vendor_derived", SourceTier: "L2", Title: q.Code + " · 行情快照", Label: "行情最新价（厂商快照）",
				Value: &q.Price, Unit: &unit, Basis: &basis, EventAt: &eventAt, ReceivedAt: &receivedAt, AvailableAt: q.AvailableAt,
				LatencyMode: q.LatencyMode, Locator: &loc, Excerpt: &excerpt, ContentHash: q.RawSHA256})
			return nil
		}
		return errs.NotFound()
	})
	return true, err
}
