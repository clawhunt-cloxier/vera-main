package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

// This wraps all report representations, including PDF and series, for both
// protocol versions. RLS lookup precedes system-side archive authorization.
func (s *Server) protectBriefRead(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request) error) error {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/reports/") {
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
	if version != emquant.ObservationBriefVersion {
		return fn(w, r)
	}
	if s.Engine.Brief == nil {
		return errs.Unavailable("SOURCE_UNAVAILABLE", "真实报告来源当前不可读取")
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
		return false, nil
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
		excerpt := strings.Join(lines, "；")
		basis := "当前采集的 Choice 原始返回；不证明首次发布时间、历史版本或实时性"
		httpx.JSON(w, r, 200, Evidence{ID: id, DatasetID: raw.DatasetID, Provider: "Choice / 东方财富", Origin: "vendor", SourceTier: "L2", Title: o.Name + " · " + label, Label: label, Basis: &basis, ReceivedAt: &raw.Source.ReceivedAt, AvailableAt: o.Cutoff, LatencyMode: "unknown", Locator: &loc, Excerpt: &excerpt, ContentHash: raw.Source.BodySHA256})
		return nil
	})
	return true, err
}
