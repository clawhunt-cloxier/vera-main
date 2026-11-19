package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		for i, shift := range []int32{-2, -4} {
			if raw := row[i+1]; raw != nil {
				v, err := decimal.NewFromString(*raw)
				if err != nil {
					return fmt.Errorf("invalid observation value in %s: %w", t.Columns[i+1], err)
				}
				row[i+1] = facts.Ptr(v.Shift(shift).StringFixed(2))
			}
		}
	}
	t.Columns[1], t.Columns[2] = "成交量（手）", "成交额（万元）"
	return nil
}

// This wraps all report representations, including PDF and series, for both
// protocol versions. RLS lookup precedes system-side archive authorization.
func (s *Server) protectBriefRead(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request) error) error {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !strings.HasPrefix(r.URL.Path, "/api/v1/reports/") {
		return fn(w, r)
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return fn(w, r)
	}
	_, ws := principal(r)
	var task uuid.UUID
	var version string
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT t.id,t.skill_version FROM report_versions v JOIN research_tasks t ON t.id=v.task_id AND t.workspace_id=v.workspace_id WHERE v.report_id=$1`, id).Scan(&task, &version)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.NotFound()
	}
	if err != nil {
		return err
	}
	if version != emquant.ObservationBriefVersion && version != harness.RealtimeBriefVersion {
		return fn(w, r)
	}
	if s.Engine.Brief == nil || s.Engine.Brief.Runtime == nil {
		return errs.Unavailable("SOURCE_UNAVAILABLE", "真实报告来源当前不可读取")
	}
	if version == harness.RealtimeBriefVersion {
		if s.Engine.Brief.Runtime == nil {
			return errs.Unavailable("SOURCE_UNAVAILABLE", "实时行情来源当前不可读取")
		}
		// Build the response before taking the grant locks, then reauthorize
		// and keep them through the final display. PDF and series share this gate.
		buf := httptest.NewRecorder()
		buf.Header().Set("Content-Type", w.Header().Get("Content-Type"))
		if err := fn(buf, r); err != nil {
			return err
		}
		return s.Engine.Brief.Runtime.WithTaskSnapshotQuote(r.Context(), ws, task, dataservice.SourceDisplay, func(emquant.QuoteView) error {
			for k, values := range buf.Header() {
				w.Header()[k] = values
			}
			w.WriteHeader(buf.Code)
			_, err := w.Write(buf.Body.Bytes())
			return err
		})
	}
	if err := s.Engine.Brief.WithSnapshot(r.Context(), ws, task, dataservice.SourceDisplay, func(emquant.BriefObservation) error { return nil }); err != nil {
		return err
	}
	return fn(w, r)
}

func (s *Server) briefEvidence(w http.ResponseWriter, r *http.Request) (bool, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return true, err
	}
	_, ws := principal(r)
	var task uuid.UUID
	err = db.InWorkspace(r.Context(), s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT s.task_id FROM observation_brief_snapshots s
 JOIN report_versions v ON v.task_id=s.task_id AND v.workspace_id=s.workspace_id
 WHERE (convert_from(s.manifest,'UTF8')::jsonb -> 'Liquidity' ->> 'ID'=$1 OR convert_from(s.manifest,'UTF8')::jsonb -> 'Security' ->> 'ID'=$1)
 ORDER BY v.delivered_at LIMIT 1`, id.String()).Scan(&task)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.realtimeEvidence(w, r, ws, id)
	}
	if err != nil {
		return true, err
	}
	if s.Engine.Brief == nil {
		return true, errs.Unavailable("SOURCE_UNAVAILABLE", "真实证据来源当前不可读取")
	}
	err = s.Engine.Brief.WithSnapshot(r.Context(), ws, task, dataservice.SourceDisplay, func(o emquant.BriefObservation) error {
		raw := o.Manifest.Liquidity
		label := "日成交观察（非完整日线）"
		var lines []string
		if id == o.Manifest.Security.ID {
			raw = o.Manifest.Security
			label = "当前证券名称观察"
			lines = []string{o.Manifest.Code + " · " + o.Name}
		} else {
			for _, row := range o.Rows {
				line := fmt.Sprintf("%s 成交量 %s 股（%s），成交额 %s 元（%s）", row.TradeDate, row.VolumeShares, row.Locators["VOLUME"], row.AmountCNY, row.Locators["AMOUNT"])
				if row.TurnoverRatio != nil {
					line += fmt.Sprintf("，换手率 %s%%（%s；厂商百分数÷100得到内部比例 %s）", decimal.RequireFromString(*row.TurnoverRatio).Shift(2).String(), row.Locators["TURN"], *row.TurnoverRatio)
				}
				lines = append(lines, line)
			}
		}
		loc := "原始归档 SHA-256 " + raw.Source.BodySHA256 + "；$.data.Values（与 Codes/Indicators/Dates 轴对应）"
		excerpt := strings.Join(lines, "\n")
		basis := "当前采集的 Choice 原始返回；不证明首次发布时间、历史版本或实时性"
		httpx.JSON(w, r, 200, Evidence{ID: id, DatasetID: raw.DatasetID, Provider: "Choice / 东方财富", Origin: "vendor_derived", SourceTier: "L2", Title: o.Name + " · " + label, Label: label, Basis: &basis, ReceivedAt: &raw.Source.ReceivedAt, AvailableAt: o.Cutoff, LatencyMode: "unknown", Locator: &loc, Excerpt: &excerpt, ContentHash: raw.Source.BodySHA256})
		return nil
	})
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
	err = s.Engine.Brief.Runtime.WithTaskSnapshotQuote(r.Context(), ws, task, dataservice.SourceDisplay, func(view emquant.QuoteView) error {
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
