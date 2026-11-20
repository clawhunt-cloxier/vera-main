package harness

import (
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

const fundPositionLimit = "按基金代码展示最近已归档季度的披露样例；陆股通持股、公募组合总数及市场合计缺少可比数据"

// A share-class code is not a unique portfolio. Expose individual filings,
// with a stable, non-size-based sample, and never sum across these records.
func fundPositionSample(b *Bundle, e uuid.UUID) []dataservice.StoredRecord {
	day := b.AsOf.In(cst).Format(time.DateOnly)
	earliest := b.AsOf.In(cst).AddDate(0, -9, 0).Format(time.DateOnly)
	byFund := map[string]dataservice.StoredRecord{}
	var quarter string
	for _, rec := range b.Rows("fund.disclosed_position", e) {
		period, pub, code := str(rec, "period_end"), str(rec, "announcement_date"), str(rec, "fund_code")
		if rec.ProviderID != "tushare" || period < earliest || period < quarter || pub < period || pub > day || code == "" {
			continue
		}
		if period > quarter {
			clear(byFund)
			quarter = period
		}
		if pub > str(byFund[code], "announcement_date") {
			byFund[code] = rec
		}
	}
	var sample []dataservice.StoredRecord
	for _, code := range slices.Sorted(maps.Keys(byFund)) {
		rec := byFund[code]
		if v, ok := dec(rec, "holding_shares"); !ok || !v.IsPositive() || str(rec, "observation_state") != "disclosed" {
			continue
		}
		sample = append(sample, rec)
		if len(sample) == 3 {
			break
		}
	}
	return sample
}

func (b *builder) fundPositionFacts(e uuid.UUID, group string) bool {
	sample := fundPositionSample(b.B, e)
	if len(sample) == 0 {
		return false
	}
	b.sources(sample)
	table := facts.Table{Title: "基金定期披露持股样例（按基金代码排序）", Columns: []string{"基金代码", "报告期末", "公告日", "持股数（股）"}}
	for _, r := range sample {
		fund, period, pub := str(r, "fund_code"), str(r, "period_end"), str(r, "announcement_date")
		shares, _ := dec(r, "holding_shares")
		table.Rows = append(table.Rows, []*string{facts.Ptr(fund), facts.Ptr(period), facts.Ptr(pub), facts.Ptr(shares.String())})
		table.Evidence = append(table.Evidence, r.ID)
		b.S.Value(group, b.name(e), "基金 "+fund+" 披露的持股数量", period+"（公告日 "+pub+"）", facts.KindReported, "基金定期披露（单一基金代码）", "shares", shares, r.ID)
	}
	b.S.Tables["S5"] = append(b.S.Tables["S5"], table)
	return true
}
