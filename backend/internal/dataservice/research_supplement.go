package dataservice

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResearchSupplementSource collects and replays registered observations that
// are not canonical records. A manifest identifies only the acquired source;
// it is not a substitute for the source's replay and authorization checks.
type ResearchSupplementSource interface {
	ID() string
	CollectResearchSupplement(ctx context.Context, code string) (json.RawMessage, error)
	ReplayResearchSupplement(ctx context.Context, tx pgx.Tx, code string, cutoff time.Time, manifest json.RawMessage, use SourceUse) (ResearchSupplement, error)
}

type ResearchSupplement struct {
	Title    string
	Evidence ResearchSupplementEvidence
	Rows     []ResearchLiquidityRow
}

// DatasetID classifies the archive. It does not assert canonical coverage.
type ResearchSupplementEvidence struct {
	ID                     uuid.UUID
	DatasetID, ContentHash string
	ReceivedAt             time.Time
	Basis, Locator         string
}

// Date is a vendor period label, not an event or publication timestamp.
// TurnoverRatio, when present, is a fraction rather than a percentage.
type ResearchLiquidityRow struct {
	Date                                   string
	VolumeShares, AmountCNY, TurnoverRatio *string
}
