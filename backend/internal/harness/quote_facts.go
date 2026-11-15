package harness

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

// QuoteFactEvidence retains the archived source and timestamps for an exact
// frozen quote. It is not an authorization to expose the archive; the evidence
// endpoint must independently verify task binding and current display rights.
type QuoteFactEvidence struct {
	ID          uuid.UUID
	EntityID    uuid.UUID
	ProviderID  string
	Code        string
	RawSHA256   string
	RawLocator  string
	TimeLocator string
	EventAt     time.Time
	ReceivedAt  time.Time
	AvailableAt time.Time
	LatencyMode string
	MarketState string
}

// BoundQuoteFacts obtains an exact frozen quote with current model-use rights
// and converts it into the existing deterministic fact format. The caller must
// keep tx open through the protected model use and separately reauthorize any
// display. This helper does not create a task, call a model or deliver a report.
func BoundQuoteFacts(ctx context.Context, tx pgx.Tx, runtime *emquant.Runtime, workspace, task, entity uuid.UUID, epoch, revision int64, snapshotHash string) (*facts.Sheet, QuoteFactEvidence, error) {
	if runtime == nil || tx == nil || entity == uuid.Nil {
		return nil, QuoteFactEvidence{}, errors.New("harness: invalid bound quote request")
	}
	view, err := runtime.ReadBoundSnapshotQuote(ctx, tx, workspace, task, epoch, revision, snapshotHash, dataservice.SourceModel)
	if err != nil {
		return nil, QuoteFactEvidence{}, err
	}
	return quoteFactSheet(task, entity, epoch, view)
}

func quoteFactSheet(task, entity uuid.UUID, epoch int64, view emquant.QuoteView) (*facts.Sheet, QuoteFactEvidence, error) {
	if task == uuid.Nil || entity == uuid.Nil || epoch < 1 || view.Cutoff.IsZero() || len(view.Quotes) != 1 || len(view.Unavailable) != 0 {
		return nil, QuoteFactEvidence{}, errors.New("harness: quote snapshot is incomplete")
	}
	q := view.Quotes[0]
	if q.EntityID != entity || q.StageID == uuid.Nil || q.Code == "" || q.ProviderID == "" || len(q.RawSHA256) != 64 || q.Locators["NOW"] == "" || q.Locators["TIME"] == "" || q.EventAt.IsZero() || q.ReceivedAt.IsZero() || q.AvailableAt.IsZero() || q.EventAt.After(q.ReceivedAt) || q.ReceivedAt.After(q.AvailableAt) || q.AvailableAt.After(view.Cutoff) {
		return nil, QuoteFactEvidence{}, errors.New("harness: quote provenance or timing is invalid")
	}
	price, err := decimal.NewFromString(q.Price)
	if err != nil || !price.IsPositive() {
		return nil, QuoteFactEvidence{}, errors.New("harness: invalid quote price")
	}
	if view.Requirement != emquant.QuoteLatestAvailable && view.Requirement != emquant.QuoteRealtimeTrade {
		return nil, QuoteFactEvidence{}, errors.New("harness: invalid quote requirement")
	}
	if q.LatencyMode != "real_time" && q.LatencyMode != "delayed" && q.LatencyMode != "unknown" {
		return nil, QuoteFactEvidence{}, errors.New("harness: invalid quote latency")
	}
	if q.MarketState != "continuous" && q.MarketState != "suspended" && q.MarketState != "closed" && q.MarketState != "unknown" {
		return nil, QuoteFactEvidence{}, errors.New("harness: invalid market status")
	}
	if view.Requirement == emquant.QuoteRealtimeTrade && (q.LatencyMode != "real_time" || q.MarketState != "continuous") {
		return nil, QuoteFactEvidence{}, errors.New("harness: quote does not satisfy realtime requirement")
	}
	sheet := facts.NewSheet(task, epoch)
	priceFact := sheet.Value("mkt", q.Code, "行情最新价（厂商快照）", q.EventAt.UTC().Format(time.RFC3339), facts.KindMarket, q.ProviderID, "price", price, q.StageID)
	// The quote must retain the vendor's exact decimal value; the generic price
	// formatter rounds to two places and can lose valid sub-cent precision.
	priceFact.Display = q.Price
	sheet.Sources["mkt.intraday_quote"] = q.AvailableAt.UTC().Format(time.RFC3339Nano)
	switch q.LatencyMode {
	case "unknown":
		sheet.Limitations = append(sheet.Limitations, "行情延迟性质未确认")
	case "delayed":
		sheet.Limitations = append(sheet.Limitations, "行情为延迟行情")
	}
	switch q.MarketState {
	case "unknown":
		sheet.Limitations = append(sheet.Limitations, "交易状态未确认")
	case "suspended":
		sheet.Limitations = append(sheet.Limitations, "行情事件时证券处于停牌状态")
	case "closed":
		sheet.Limitations = append(sheet.Limitations, "行情事件时市场处于非交易状态")
	}
	sheet.Finalize()
	ev := QuoteFactEvidence{ID: q.StageID, EntityID: q.EntityID, ProviderID: q.ProviderID, Code: q.Code, RawSHA256: q.RawSHA256,
		RawLocator: q.Locators["NOW"], TimeLocator: q.Locators["TIME"], EventAt: q.EventAt, ReceivedAt: q.ReceivedAt,
		AvailableAt: q.AvailableAt, LatencyMode: q.LatencyMode, MarketState: q.MarketState}
	return sheet, ev, nil
}
