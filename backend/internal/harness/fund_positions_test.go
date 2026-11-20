package harness

import (
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/google/uuid"
)

func TestFundPositionSampleUsesLatestDisclosurePerCode(t *testing.T) {
	id := uuid.New()
	var rows []dataservice.StoredRecord
	add := func(fund, period, pub, state, provider string) {
		rows = append(rows, dataservice.StoredRecord{ID: uuid.New(), ProviderID: provider, DatasetID: "fund.disclosed_position", EntityID: &id, Payload: map[string]any{"fund_code": fund, "period_end": period, "announcement_date": pub, "holding_shares": "123", "observation_state": state}})
	}
	add("000001.OF", "2026-09-30", "2026-10-20", "disclosed", "tushare")
	add("000001.OF", "2026-09-30", "2026-11-15", "conflicting", "tushare")
	add("000002.OF", "2026-06-30", "2026-08-10", "disclosed", "tushare")
	add("000003.OF", "2026-09-30", "2026-10-24", "disclosed", "tushare")
	add("000004.OF", "2026-09-30", "2026-10-25", "disclosed", "another_provider")
	add("000005.OF", "2026-12-31", "2027-01-20", "disclosed", "tushare")
	b := newBundle(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), dataservice.EntityRow{ID: id}, nil, rows)
	got := fundPositionSample(b, id)
	if len(got) != 1 || str(got[0], "fund_code") != "000003.OF" {
		t.Fatalf("selected records: %+v", got)
	}
}
