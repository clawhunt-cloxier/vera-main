package tushare

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const dragonWindow = 20
const dragonPage = 10000 // Official per-request maximum for both daily endpoints.

// The roster and seat endpoints have separate entitlements. Preserve a verified
// roster when seat coverage is missing. Global authentication and archive errors
// still fail the enclosing sync. Start with the most recent trading sessions.
func (c *marketCollector) dragonTiger() error {
	var days []string
	for _, rec := range c.Records {
		if rec.DatasetID == "ref.trading_calendar" && rec.Payload["is_open"] == true && rec.Period != nil {
			days = append(days, rec.Period.Format(marketDay))
		}
	}
	slices.Sort(days)
	slices.Reverse(days)
	seatsEnabled := true
	for _, day := range days[:min(len(days), dragonWindow)] {
		list, err := c.rows("top_list", map[string]string{"trade_date": day}, "trade_date ts_code reason", dragonPage, false)
		if dragonEmpty(err) {
			continue
		}
		if dragonUnavailable(err) {
			c.Notes = append(c.Notes, "TuShare 龙虎榜上榜清单不可用，未核对的日期覆盖未知")
			break
		}
		if err != nil {
			return err
		}
		roster, err := c.dragonRoster(day, list)
		if err != nil {
			return err
		}
		if roster == nil {
			continue
		}
		var seats []Row
		if len(roster.reasons) > 0 && seatsEnabled {
			seats, err = c.rows("top_inst", map[string]string{"trade_date": day, "ts_code": c.code}, "trade_date ts_code reason side exalter buy sell", dragonPage, false)
			if dragonEmpty(err) {
				seats = nil
			} else if dragonUnavailable(err) {
				seatsEnabled = false
				c.Notes = append(c.Notes, "TuShare 龙虎榜席位接口不可用，已取得的上榜清单单独保留")
			} else if err != nil {
				return err
			}
		}
		if err := c.publishDragon(day, roster, seats); err != nil {
			return err
		}
	}
	return nil
}

func dragonUnavailable(err error) bool {
	var p *dataservice.ProviderError
	return errors.As(err, &p) && (p.Class == dataservice.ErrNotCovered || p.Class == dataservice.ErrRateLimited || p.Class == dataservice.ErrUpstreamUnavailable)
}

func dragonEmpty(err error) bool {
	var p *dataservice.ProviderError
	return errors.As(err, &p) && p.Class == dataservice.ErrEmptyValid
}

type dragonRoster struct {
	reasons  map[string]bool
	pages    map[string]marketInput
	received time.Time
}

func dragonText(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Each returned request manifest identifies the full original (unfiltered)
// roster page. An empty roster cannot establish that this exchange published.
func (c *marketCollector) dragonRoster(day string, rows []Row) (*dragonRoster, error) {
	out := &dragonRoster{reasons: map[string]bool{}, pages: map[string]marketInput{}}
	exchangeSeen := false
	var earliest time.Time
	_, exchange, _ := strings.Cut(c.code, ".")
	for _, r := range rows {
		code, reason := r.Values["ts_code"], r.Values["reason"]
		if !instrument.MatchString(code) || !dragonText(reason) {
			return nil, marketBad("top_list", "ts_code/reason")
		}
		if code == c.code {
			out.reasons[reason] = true
		}
		exchangeSeen = exchangeSeen || strings.HasSuffix(code, "."+exchange)
		if earliest.IsZero() || r.ReceivedAt.Before(earliest) {
			earliest = r.ReceivedAt
		}
		out.pages[r.RawSHA256] = marketInput{r.RawRef, r.RawSHA256, "response_ref#$.data.items"}
		if r.ReceivedAt.After(out.received) {
			out.received = r.ReceivedAt
		}
	}
	// Official daily publication is at 20:00 Beijing time. A partial
	// intraday release does not establish exchange-wide coverage.
	disclosure, err := time.ParseInLocation(marketDay, day, marketZone)
	if err != nil {
		return nil, marketBad("top_list", "trade_date")
	}
	if !exchangeSeen || earliest.Before(disclosure.Add(20*time.Hour)) {
		return nil, nil
	}
	return out, nil
}

type dragonSeat struct {
	row                     Row
	reason, side, name      string
	buy, sell, rankedAmount decimal.Decimal
}

var dragonMultipleDays = regexp.MustCompile(`([2-9]|[0-9]{2,}|[两二三四五六七八九十]+)(个)?(交易)?日`)

func dragonScope(reason string) string {
	if dragonMultipleDays.MatchString(reason) {
		return "multi_day"
	}
	for _, s := range []string{"连续", "累计", "累积", "期间", "多日"} {
		if strings.Contains(reason, s) {
			return "multi_day"
		}
	}
	for _, s := range []string{"单日", "当日", "日涨幅", "日跌幅", "日价格", "日收盘", "日换手", "日振幅", "日成交"} {
		if strings.Contains(reason, s) {
			return "single_day"
		}
	}
	return "unknown"
}

func dragonSeatType(name string) string {
	switch {
	case name == "机构专用":
		return "institution"
	case name == "沪股通专用" || name == "深股通专用":
		return "connect"
	case strings.Contains(name, "营业部"):
		return "branch"
	default:
		return "other"
	}
}

func dragonAmount(s string) (decimal.Decimal, bool) {
	d, err := decimal.NewFromString(s)
	return d, err == nil && !d.IsNegative() && d.Exponent() >= -30 && d.Exponent() <= 18
}

// The source supplies top-five membership and the side, not the rank. Rank
// by the amount of the named side. Resolve ties reproducibly by seat name
// and opposite amount; indistinguishable repeated rows invalidate the set.
func dragonSeats(rows []Row, reasons map[string]bool) ([]dragonSeat, bool) {
	var seats []dragonSeat
	counts := map[[2]string]int{}
	for _, r := range rows {
		v := r.Values
		side, ok := map[string]string{"0": "buy", "1": "sell"}[v["side"]]
		buy, hasBuy := dragonAmount(v["buy"])
		sell, hasSell := dragonAmount(v["sell"])
		if !ok || !reasons[v["reason"]] || !dragonText(v["exalter"]) || !hasBuy || !hasSell {
			return nil, false
		}
		ranked := buy
		if side == "sell" {
			ranked = sell
		}
		seats = append(seats, dragonSeat{r, v["reason"], side, v["exalter"], buy, sell, ranked})
		counts[[2]string{v["reason"], side}]++
	}
	for reason := range reasons {
		if counts[[2]string{reason, "buy"}] != 5 || counts[[2]string{reason, "sell"}] != 5 {
			return nil, false
		}
	}
	compare := func(a, b dragonSeat) int {
		return cmp.Or(cmp.Compare(a.reason, b.reason), cmp.Compare(a.side, b.side), b.rankedAmount.Cmp(a.rankedAmount), cmp.Compare(a.name, b.name), b.buy.Cmp(a.buy), b.sell.Cmp(a.sell))
	}
	slices.SortFunc(seats, compare)
	for i := 1; i < len(seats); i++ {
		if compare(seats[i-1], seats[i]) == 0 {
			return nil, false
		}
	}
	return seats, true
}

func (c *marketCollector) publishDragon(day string, roster *dragonRoster, rows []Row) error {
	date, err := marketDate(day)
	if err != nil {
		return err
	}
	received := roster.received
	for _, r := range rows {
		if received.Before(r.ReceivedAt) {
			received = r.ReceivedAt
		}
	}
	ordered, complete := dragonSeats(rows, roster.reasons)
	reasons := slices.Sorted(maps.Keys(roster.reasons))
	ranks := map[[2]string]int{}
	proof := map[string]marketInput{}
	seatPayloads := make([]map[string]any, 0, len(ordered))
	var seatRecords []dataservice.Record
	for _, s := range ordered {
		group := [2]string{s.reason, s.side}
		ranks[group]++
		reasonCode := fmt.Sprintf("%x", sha256.Sum256([]byte(s.reason)))
		key := fmt.Sprintf("%s|%s|%s|%s|%d", c.code, day, reasonCode, s.side, ranks[group])
		p := map[string]any{"trade_date": date.Format(time.DateOnly), "reason_code": reasonCode, "reason_text": s.reason, "period_scope": dragonScope(s.reason), "side": s.side, "rank": ranks[group], "seat_name": s.name, "seat_type": dragonSeatType(s.name), "buy_amount": s.buy.String(), "sell_amount": s.sell.String()}
		seatPayloads = append(seatPayloads, p)
		proof[key] = marketInput{s.row.RawRef, s.row.RawSHA256, s.row.RawLocator}
		seatRecords = append(seatRecords, marketRecord(s.row, "flow.dragon_tiger", c.code, key, "exchange", "L2", "TuShare 龙虎榜分榜前五，按本侧金额排序；同额按席位名称、另一侧金额排序", &date, p))
	}
	content, err := json.Marshal([]any{c.code, day, reasons, complete, seatPayloads})
	if err != nil {
		return err
	}
	edition := fmt.Sprintf("%x", sha256.Sum256(content))
	rawSeats := make([]marketInput, 0, len(rows))
	for _, r := range rows {
		rawSeats = append(rawSeats, marketInput{r.RawRef, r.RawSHA256, r.RawLocator})
	}
	manifest, err := json.Marshal(map[string]any{"ts_code": c.code, "trade_date": day, "edition_id": edition, "top_list_pages": roster.pages, "top_inst_rows": rawSeats, "seats": proof})
	if err != nil {
		return err
	}
	ref, sha, err := c.Archive(c.ctx, "joins", manifest)
	if err != nil {
		return err
	}
	for i := range seatRecords {
		rec := &seatRecords[i]
		rec.Payload["edition_id"] = edition
		rec.AvailableAt = received
		rec.RawRef, rec.RawSHA256, rec.RawLocator = ref, sha, fmt.Sprintf("$.seats[%q]", strings.TrimPrefix(rec.RecordKey, "tushare|flow.dragon_tiger|"))
	}
	cover := map[string]any{"trade_date": date.Format(time.DateOnly), "listed": len(reasons) > 0, "reason_count": len(reasons), "seats_complete": complete, "edition_id": edition}
	if len(reasons) > 0 {
		cover["reason_text"] = strings.Join(reasons, "\n")
	}
	header := marketRecord(Row{ReceivedAt: received, RawRef: ref, RawSHA256: sha, RawLocator: "$"}, "flow.dragon_tiger_day", c.code, c.code+"|"+day, "exchange", "L2", "TuShare 同交易所已披露龙虎榜每日清单，按交易日期核对是否上榜", &date, cover)
	c.Records = append(c.Records, header)
	c.Records = append(c.Records, seatRecords...)
	return nil
}
