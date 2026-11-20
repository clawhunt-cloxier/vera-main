package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

type BoundResearchSupplement struct {
	dataservice.ResearchSupplement
	ProviderID string
	Cutoff     time.Time
}

type supplementBinding struct {
	provider string
	evidence uuid.UUID
	cutoff   time.Time
	hash     string
	manifest []byte
}

func supplementHash(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return hex.EncodeToString(sum[:])
}

func setResearchWorkspace(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE vera_app"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SELECT set_config('app.workspace_id',$1,true)", ws.String())
	return err
}

func (e *Engine) bindResearchSupplements(ctx context.Context, t *taskRow, subject dataservice.EntityRow, snapshot string) error {
	var bound *string
	if err := e.Pool.QueryRow(ctx, `SELECT snapshot_hash FROM research_tasks
		WHERE id=$1 AND workspace_id=$2 AND epoch=$3 AND status='COLLECTING'`, t.ID, t.Workspace, t.Epoch).Scan(&bound); err != nil {
		return err
	}
	if bound != nil {
		if *bound != snapshot {
			return errors.New("research inputs changed after snapshot was bound")
		}
		return nil
	}
	type candidate struct {
		source   dataservice.ResearchSupplementSource
		manifest json.RawMessage
	}
	sampleCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	results := make(chan candidate, len(e.ResearchSupplements))
	for _, source := range e.ResearchSupplements {
		go func() {
			manifest, err := source.CollectResearchSupplement(sampleCtx, subject.Code)
			if err != nil || len(manifest) > 1048576 || !json.Valid(manifest) {
				manifest = nil
			}
			results <- candidate{source, manifest}
		}()
	}
	var candidates []candidate
collect:
	for range e.ResearchSupplements {
		select {
		case c := <-results:
			if c.manifest != nil {
				candidates = append(candidates, c)
			}
		case <-sampleCtx.Done():
			break collect
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	slices.SortFunc(candidates, func(a, b candidate) int { return strings.Compare(a.source.ID(), b.source.ID()) })
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := e.Store.RequireResearchUseTx(ctx, tx); err != nil {
			return err
		}
		var cutoff time.Time
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&cutoff); err != nil {
			return err
		}
		var bindings []supplementBinding
		for _, c := range candidates {
			o, err := c.source.ReplayResearchSupplement(ctx, tx, subject.Code, cutoff, c.manifest, dataservice.SourceModel)
			if err != nil || o.Evidence.ID == uuid.Nil || len(o.Rows) == 0 {
				continue
			}
			bindings = append(bindings, supplementBinding{
				provider: c.source.ID(), evidence: o.Evidence.ID, cutoff: cutoff,
				hash: supplementHash(c.manifest), manifest: c.manifest,
			})
		}
		if err := setResearchWorkspace(ctx, tx, t.Workspace); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE research_tasks SET snapshot_hash=$4
			WHERE id=$1 AND workspace_id=$2 AND epoch=$3 AND status='COLLECTING'
			  AND deadline>clock_timestamp() AND snapshot_hash IS NULL`, t.ID, t.Workspace, t.Epoch, snapshot)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errStale
		}
		for _, b := range bindings {
			if _, err := tx.Exec(ctx, `INSERT INTO research_source_supplements
				(workspace_id,task_id,epoch,provider_id,snapshot_hash,evidence_id,cutoff,manifest_hash,manifest)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				t.Workspace, t.ID, t.Epoch, b.provider, snapshot, b.evidence, b.cutoff, b.hash, b.manifest); err != nil {
				return err
			}
		}
		return nil
	})
}

// WithUnifiedSources reads only the immutable inputs bound to this
// workspace/task and holds their current-use grants through fn.
func (e *Engine) WithUnifiedSources(ctx context.Context, ws, task uuid.UUID, use dataservice.SourceUse, fn func(pgx.Tx, []BoundResearchSupplement) error) error {
	if !e.RealResearch || e.Store == nil {
		return errors.New("unified research disabled")
	}
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := e.Store.RequireResearchUseTx(ctx, tx); err != nil {
			return err
		}
		var code, snapshot string
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT e.code,t.epoch,t.snapshot_hash
			FROM research_tasks t JOIN entities e ON e.id=t.subject_id
			WHERE t.id=$1 AND t.workspace_id=$2 AND t.skill_version=$3 AND NOT e.synthetic
			  AND t.status IN ('COLLECTING','ANALYZING','ASSEMBLING','DELIVERING','COMPLETED')`,
			task, ws, CloseUnifiedVersion).Scan(&code, &epoch, &snapshot); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT provider_id,evidence_id,cutoff,manifest_hash,manifest
			FROM research_source_supplements WHERE workspace_id=$1 AND task_id=$2 AND epoch=$3 AND snapshot_hash=$4
			ORDER BY provider_id`, ws, task, epoch, snapshot)
		if err != nil {
			return err
		}
		bindings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (supplementBinding, error) {
			var b supplementBinding
			err := row.Scan(&b.provider, &b.evidence, &b.cutoff, &b.hash, &b.manifest)
			return b, err
		})
		if err != nil {
			return err
		}
		out := make([]BoundResearchSupplement, 0, len(bindings))
		for _, b := range bindings {
			var adapter dataservice.ResearchSupplementSource
			for _, s := range e.ResearchSupplements {
				if s.ID() == b.provider {
					adapter = s
					break
				}
			}
			if adapter == nil || supplementHash(b.manifest) != b.hash {
				return fmt.Errorf("bound research source %q unavailable", b.provider)
			}
			o, err := adapter.ReplayResearchSupplement(ctx, tx, code, b.cutoff, b.manifest, use)
			if err != nil {
				return err
			}
			if o.Evidence.ID != b.evidence || len(o.Rows) == 0 {
				return errors.New("bound research evidence changed")
			}
			out = append(out, BoundResearchSupplement{ResearchSupplement: o, ProviderID: b.provider, Cutoff: b.cutoff})
		}
		return fn(tx, out)
	})
}

func addResearchSupplements(sheet *facts.Sheet, subject, skill string, sources []BoundResearchSupplement, expected int) error {
	section := map[string]string{"stock_analysis": "S6", "capital_flow": "S2", "deep_research": "S11"}[skill]
	if section == "" {
		return errors.New("unknown unified research skill")
	}
	if len(sources) < expected {
		sheet.Limit("部分补充行情来源本次未取得可验证数据，不影响已列出的收盘数据口径。")
	}
	for _, source := range sources {
		ev := source.Evidence
		table := facts.Table{Title: source.Title, Columns: []string{"厂商返回日期", "成交量（股）", "成交额（元）", "换手率（%）"}, Evidence: []uuid.UUID{ev.ID}}
		rows := source.Rows[max(0, len(source.Rows)-20):]
		for i, row := range rows {
			var turn *string
			line := []*string{facts.Ptr(row.Date), row.VolumeShares, row.AmountCNY}
			for _, field := range []struct {
				label, unit string
				raw         *string
			}{
				{"成交量", "shares", row.VolumeShares},
				{"成交额", "CNY", row.AmountCNY},
				{"换手率", "ratio", row.TurnoverRatio},
			} {
				if field.raw == nil {
					continue
				}
				value, err := decimal.NewFromString(*field.raw)
				if err != nil || value.IsNegative() {
					return errors.New("invalid bound daily observation")
				}
				if field.unit == "ratio" {
					turn = facts.Ptr(value.Shift(2).String())
				}
				if i == len(rows)-1 {
					sheet.Value("mkt", subject, field.label, row.Date, facts.KindVendorDerived, source.Title, field.unit, value, ev.ID)
				}
			}
			table.Rows = append(table.Rows, append(line, turn))
		}
		sheet.Tables[section] = append(sheet.Tables[section], table)
		if sheet.SourceProviders == nil {
			sheet.SourceProviders = map[string]map[string]string{}
		}
		if sheet.SourceProviders[ev.DatasetID] == nil {
			sheet.SourceProviders[ev.DatasetID] = map[string]string{}
		}
		sheet.SourceProviders[ev.DatasetID][source.ProviderID] = ev.ReceivedAt.UTC().Format(time.RFC3339Nano)
	}
	if len(sources) > 0 {
		sheet.Limit("补充日成交在确认任务后采集，是厂商返回日期的非实时观察；不推定首次发布时间，不与其他来源拼接为同一条日线。")
	}
	return nil
}
