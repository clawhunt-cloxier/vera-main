package harness

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
)

const seatDayDataset = "flow.dragon_tiger_day"

type seatDay struct {
	coverage dataservice.StoredRecord
	rows     []dataservice.StoredRecord
}

type seatDisclosures struct {
	days        []seatDay // resolved, in trading-calendar order
	window      int
	hasBlock    bool
	missingSeat bool
}

// A newer top_list result may remove a reason, seat, or the whole listing.
// Use the day as the manifest. A surviving row from an earlier edition
// cannot supply an amount or even establish that the stock was listed.
func (b *Bundle) seatDisclosures(entity uuid.UUID, n int) seatDisclosures {
	window := b.Window(n)
	d := seatDisclosures{window: len(window)}
	byDay := make(map[string]*seatDay)
	for _, day := range window {
		byDay[day] = nil
	}
	for _, r := range b.Rows(seatDayDataset, entity) {
		day := str(r, "trade_date")
		_, listed := r.Payload["listed"].(bool)
		if _, ok := byDay[day]; ok && listed && str(r, "edition_id") != "" {
			byDay[day] = &seatDay{coverage: r}
		}
	}
	for _, r := range b.Rows("flow.dragon_tiger", entity) {
		day := byDay[str(r, "trade_date")]
		if day == nil || !boolean(day.coverage, "listed") || !boolean(day.coverage, "seats_complete") ||
			day.coverage.ProviderID != r.ProviderID || str(day.coverage, "edition_id") != str(r, "edition_id") {
			continue
		}
		day.rows = append(day.rows, r)
	}
	for _, date := range window {
		day := byDay[date]
		if day == nil {
			continue
		}
		slices.SortFunc(day.rows, func(a, b dataservice.StoredRecord) int {
			return cmp.Or(strings.Compare(str(a, "reason_text"), str(b, "reason_text")), strings.Compare(str(a, "side"), str(b, "side")), cmp.Compare(num(a, "rank"), num(b, "rank")))
		})
		d.days = append(d.days, *day)
		if boolean(day.coverage, "listed") && len(day.rows) == 0 {
			d.missingSeat = true
		}
	}
	for _, r := range b.Rows("flow.block_trade", entity) {
		if _, ok := byDay[str(r, "trade_date")]; ok {
			if _, valid := dec(r, "amount"); valid {
				d.hasBlock = true
				break
			}
		}
	}
	return d
}

func (d seatDisclosures) availability() Availability {
	if len(d.days) == 0 && !d.hasBlock {
		return unavailable("窗口内缺少可核实的龙虎榜查询和大宗交易记录")
	}
	var gaps []string
	if len(d.days) < d.window || len(d.days) == 0 {
		gaps = append(gaps, "龙虎榜逐日查询未覆盖整个观察窗口")
	}
	if d.missingSeat {
		gaps = append(gaps, "部分上榜日缺少可核实的席位明细")
	}
	if !d.hasBlock {
		gaps = append(gaps, "窗口内没有可引用的大宗交易记录")
	}
	if len(gaps) > 0 {
		return limited(strings.Join(gaps, "；"))
	}
	return avail()
}

func (b *builder) addDisclosedSeats(disclosures seatDisclosures, e uuid.UUID, n int, group, section string) {
	if av := disclosures.availability(); av.Reason != nil {
		b.S.Limit(*av.Reason)
	}
	if len(disclosures.days) == 0 {
		return
	}
	subj := b.name(e)
	period := windowLabel(n, b.B.Window(n))
	listings := facts.Table{Title: subj + " 龙虎榜上榜披露", Columns: []string{"上榜交易日", "披露原因", "席位明细"}}
	seats := facts.Table{Title: subj + " 龙虎榜席位披露", Columns: []string{"上榜交易日", "披露原因", "统计期间", "榜别与金额排序", "披露席位", "买入额（万元）", "卖出额（万元）"}}
	var coverage []uuid.UUID
	for _, day := range disclosures.days {
		r := day.coverage
		b.source(r)
		coverage = append(coverage, r.ID)
		if !boolean(r, "listed") {
			continue
		}
		date := str(r, "trade_date")
		status := "已披露"
		if len(day.rows) == 0 {
			status = "未取得"
		}
		listings.Rows = append(listings.Rows, []*string{facts.Ptr(date), facts.Ptr(str(r, "reason_text")), facts.Ptr(status)})
		listings.Evidence = append(listings.Evidence, r.ID)
		b.sources(day.rows)
		for _, row := range day.rows {
			seats.Rows = append(seats.Rows, []*string{facts.Ptr(date), facts.Ptr(str(row, "reason_text")), facts.Ptr(seatScope(str(row, "period_scope"))),
				facts.Ptr(seatSide(str(row, "side")) + "第" + strconv.Itoa(num(row, "rank")) + "行"), facts.Ptr(str(row, "seat_name")), seatAmount(row, "buy_amount"), seatAmount(row, "sell_amount")})
			seats.Evidence = append(seats.Evidence, row.ID)
		}
		b.addInstitutionRanks(day, e, group)
	}
	label := "龙虎榜上榜交易日数"
	if len(coverage) < disclosures.window {
		label = "已核实交易日中的龙虎榜上榜日数"
	}
	b.S.Value(group, subj, label, period, facts.KindMarket, "交易所龙虎榜逐日查询", "count:个交易日", decimal.NewFromInt(int64(len(listings.Rows))), coverage...)
	for _, table := range []facts.Table{listings, seats} {
		if len(table.Rows) > 0 {
			b.S.Tables[section] = append(b.S.Tables[section], table)
		}
	}
}

func seatAmount(r dataservice.StoredRecord, field string) *string {
	value, valid := dec(r, field)
	if !valid {
		return nil
	}
	return facts.Ptr(value.Shift(-4).StringFixed(2))
}

func seatSide(side string) string {
	if side == "buy" {
		return "买入榜"
	}
	return "卖出榜"
}

func seatScope(scope string) string {
	switch scope {
	case "single_day":
		return "单日"
	case "multi_day":
		return "连续多日"
	default:
		return "期间未明确"
	}
}

// A seat can appear on both rankings and multiple disclosure reasons.
// Summation is confined to one unambiguous single-session reason, using
// only the matching side's amount on each ranking. Never infer net flow.
func (b *builder) addInstitutionRanks(day seatDay, entity uuid.UUID, group string) {
	if num(day.coverage, "reason_count") != 1 || len(day.rows) == 0 {
		return
	}
	for _, r := range day.rows {
		if str(r, "period_scope") != "single_day" || str(r, "reason_text") != str(day.coverage, "reason_text") {
			return
		}
	}
	date := str(day.coverage, "trade_date")
	for _, side := range []string{"buy", "sell"} {
		var inputs []facts.Input
		var seen [6]bool
		complete := true
		for _, r := range day.rows {
			if str(r, "side") != side {
				continue
			}
			rank := num(r, "rank")
			if rank < 1 || rank > 5 || seen[rank] {
				complete = false
				break
			}
			seen[rank] = true
			if str(r, "seat_type") == "institution" {
				v, ok := input(r, side+"_amount", "CNY")
				if !ok {
					complete = false
					break
				}
				inputs = append(inputs, v)
			}
		}
		if complete && len(inputs) > 0 {
			action := strings.TrimSuffix(seatSide(side), "榜")
			label := "龙虎榜" + seatSide(side) + "披露的机构专用席位" + action + "额合计"
			b.S.Computed(group, b.name(entity), label, date, facts.Sum("F27.event_sum", "CNY", inputs), entity.String()+"|ranked-seats|"+date+"|"+side)
		}
	}
}
