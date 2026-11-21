package dataservice

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ObservationRef names a single published closing bar in a mixed-source manifest.
type ObservationRef struct {
	ID                     uuid.UUID
	DatasetID, ContentHash string
}

func (s *Store) RequireTuShareResearch(ctx context.Context, tx pgx.Tx) error {
	if s == nil || s.providerID != "tushare" || !s.allowResearch {
		return errors.New("TuShare 研究用途未启用")
	}
	var provider string
	return tx.QueryRow(ctx, `SELECT id FROM providers WHERE id='tushare' AND kind='vendor' AND enabled AND allow_model_processing AND NOT synthetic FOR SHARE`).Scan(&provider)
}

// ObservationBar locks the current research permission and the chosen canonical row
// in the caller's transaction. Replays require the original ID and content hash.
func (s *Store) ObservationBar(ctx context.Context, tx pgx.Tx, subject uuid.UUID, code string, cutoff time.Time, frozen *ObservationRef) (StoredRecord, error) {
	if s == nil || !s.allowResearch {
		return StoredRecord{}, errors.New("TuShare 研究对象或用途未启用")
	}
	registered, err := s.researchSubjectRegistered(ctx, tx, code)
	if err != nil {
		return StoredRecord{}, err
	}
	if !registered {
		return StoredRecord{}, errors.New("TuShare 研究对象或用途未启用")
	}
	if err := s.RequireTuShareResearch(ctx, tx); err != nil {
		return StoredRecord{}, err
	}
	var id *uuid.UUID
	if frozen != nil {
		if frozen.DatasetID != "mkt.daily_bar" || frozen.ID == uuid.Nil || frozen.ContentHash == "" {
			return StoredRecord{}, errors.New("TuShare 收盘证据标识无效")
		}
		id = &frozen.ID
	}
	rows, err := tx.Query(ctx, `SELECT `+recordCols+` FROM records r JOIN entities e ON e.id=r.entity_id
		WHERE e.id=$1 AND e.code=$3 AND e.kind='stock' AND NOT e.synthetic AND `+visibleAtCutoff+`
		AND r.dataset_id='mkt.daily_bar' AND r.provider_id='tushare' AND r.quality_state IN ('PASS','WARN')
		AND r.period IS NOT NULL AND r.period <= ($2 AT TIME ZONE 'Asia/Shanghai')::date
		AND ($4::uuid IS NULL OR r.id=$4)
		ORDER BY r.period DESC,r.record_version DESC,r.record_key,r.id LIMIT 1 FOR SHARE OF r,e`, subject, cutoff, code, id)
	if err != nil {
		return StoredRecord{}, err
	}
	row, err := pgx.CollectExactlyOneRow(rows, scanRecord)
	if err != nil {
		return row, err
	}
	day, ok := row.Payload["trade_date"].(string)
	close, hasPrice := row.Payload["close"].(string)
	content := Record{DatasetID: row.DatasetID, RecordKey: row.RecordKey, Payload: row.Payload}
	if !ok || day != row.Period.UTC().Format(time.DateOnly) || !hasPrice || close == "" || row.RawRef == nil || row.RawSHA256 == nil || row.RawLocator == nil ||
		content.Hash() != row.ContentHash || (frozen != nil && row.ContentHash != frozen.ContentHash) {
		return row, errors.New("TuShare 收盘数据缺少可核验的日期、价格或来源")
	}
	return row, nil
}
