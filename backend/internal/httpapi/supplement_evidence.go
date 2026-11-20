package httpapi

import (
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/httpx"
)

func (s *Server) supplementEvidence(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) (bool, error) {
	if !slices.Contains(s.historyVersions(), harness.CloseUnifiedVersion) {
		return false, nil
	}
	ctx := r.Context()
	var task uuid.UUID
	var provider string
	err := db.InWorkspace(ctx, s.Pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT x.task_id,x.provider_id FROM research_source_supplements x
			JOIN research_tasks t ON t.workspace_id=x.workspace_id AND t.id=x.task_id
			AND t.epoch=x.epoch AND t.snapshot_hash=x.snapshot_hash
			WHERE x.workspace_id=$1 AND x.evidence_id=$2 AND t.skill_version=$3 AND t.status='COMPLETED'
			AND EXISTS(SELECT 1 FROM report_versions v WHERE v.workspace_id=t.workspace_id AND v.task_id=t.id)
			ORDER BY x.cutoff DESC,x.task_id,x.provider_id LIMIT 1`, ws, id, harness.CloseUnifiedVersion).Scan(&task, &provider)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	matched := false
	err = s.Engine.WithUnifiedSources(ctx, ws, task, dataservice.SourceDisplay, func(tx pgx.Tx, sources []harness.BoundResearchSupplement) error {
		for _, source := range sources {
			e := source.Evidence
			if source.ProviderID != provider || e.ID != id || len(source.Rows) == 0 {
				continue
			}
			var name string
			if err := tx.QueryRow(ctx, `SELECT name FROM providers WHERE id=$1`, source.ProviderID).Scan(&name); err != nil {
				return err
			}
			first, last := source.Rows[0].Date, source.Rows[len(source.Rows)-1].Date
			label := source.Title + " · " + last
			if first != last {
				label = source.Title + " · " + first + " 至 " + last
			}
			matched = true
			httpx.JSON(w, r, http.StatusOK, Evidence{ID: e.ID, DatasetID: e.DatasetID, Provider: name,
				Origin: "vendor_derived", SourceTier: "L2", Title: source.Title, Label: label, Period: &last,
				Basis: &e.Basis, ReceivedAt: &e.ReceivedAt, AvailableAt: source.Cutoff, LatencyMode: "unknown",
				Locator: &e.Locator, ContentHash: e.ContentHash})
			return nil
		}
		return errs.NotFound()
	})
	if err != nil && !matched && errs.As(err).Status != http.StatusNotFound {
		return true, errs.Unavailable("SOURCE_UNAVAILABLE", "研报的补充证据当前不可读取").Wrap(err)
	}
	return true, err
}
