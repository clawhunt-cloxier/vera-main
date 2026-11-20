package harness

import (
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

// paidDividends lists individual implemented distributions visible in the
// frozen snapshot. The accounting period may precede payment by several months.
func (b *builder) paidDividends(e uuid.UUID, group string) bool {
	cutoff := b.B.AsOf.In(cst).Format(time.DateOnly)
	eventKey := func(r dataservice.StoredRecord) [3]string {
		return [3]string{r.ProviderID, str(r, "report_period"), str(r, "record_date")}
	}
	statuses := map[[3]string]dataservice.StoredRecord{}
	for _, status := range b.B.Rows("corp.dividend_implementation", e) {
		statuses[eventKey(status)] = status // Store.Query selects the visible version of each event.
	}
	var rows []dataservice.StoredRecord
	for _, r := range b.B.Rows("corp.dividend", e) {
		period, ann, pay := str(r, "report_period"), str(r, "implementation_date"), str(r, "payment_date")
		status := statuses[eventKey(r)]
		if str(r, "record_date") == "" || !boolean(status, "eligible") || str(status, "implementation_date") != ann ||
			len(period) != len(time.DateOnly) || ann < period || pay < ann || pay > cutoff ||
			str(r, "fiscal_year") != period[:4] || r.AvailableAt.After(b.B.AsOf) {
			continue
		}
		per10, c := dec(r, "cash_per_10_shares")
		base, s := dec(r, "base_shares")
		total, t := dec(r, "total_cash")
		if c && s && t && per10.IsPositive() && base.IsPositive() && total.Equal(per10.Shift(-1).Mul(base)) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return false
	}
	slices.SortFunc(rows, func(a, b dataservice.StoredRecord) int {
		for _, field := range []string{"report_period", "payment_date", "implementation_date"} {
			if n := strings.Compare(str(b, field), str(a, field)); n != 0 {
				return n
			}
		}
		return strings.Compare(a.RecordKey, b.RecordKey)
	})
	table := facts.Table{Title: "已披露现金分红实施明细", Columns: []string{"所属报告期", "实施公告日", "派息日", "每十股派现（税前，元）", "分红基准股本（股）", "本次折算派现（元）"}}
	subject := b.name(e)
	for _, r := range rows {
		status := statuses[eventKey(r)]
		b.source(r)
		b.source(status)
		period := str(r, "report_period")
		pay := str(r, "payment_date")
		label := "所属报告期 " + period + "，派息日 " + pay
		per10, _ := input(r, "cash_per_10_shares", "CNY/10 shares")
		base, _ := input(r, "base_shares", "shares")
		declared := b.S.Value(group, subject, "本次每十股派现（税前）", label, facts.KindReported, r.Basis, "price", per10.Value, r.ID, status.ID)
		declared.Display = per10.Value.String() // Retain the announced rate, including sub-cent distributions.
		calc := facts.Derived("dividend.cash_at_declared_base", "CNY", []facts.Input{per10, base}, func() (*decimal.Decimal, string) {
			cash := per10.Value.Shift(-1).Mul(base.Value)
			return &cash, ""
		})
		cash := b.S.Computed(group, subject, "本次按分红基准股本折算的现金金额", label, calc, "dividend-cash|"+r.ID.String())
		cash.Basis = "税前每十股派现金额 ÷ 10 × 本次分红基准股本"
		table.Rows = append(table.Rows, []*string{facts.Ptr(period), facts.Ptr(str(r, "implementation_date")), facts.Ptr(pay),
			facts.Ptr(per10.Value.String()), facts.Ptr(base.Value.String()), facts.Ptr(cash.Value.String())})
		table.Evidence = append(table.Evidence, r.ID, status.ID)
	}
	b.S.Tables["S8"] = append(b.S.Tables["S8"], table)
	b.S.Limit("分红按已取得实施公告且派息日已过的方案逐笔列示。每笔按本次分红基准股本折算，全年度方案覆盖情况尚待核实。")
	return true
}
