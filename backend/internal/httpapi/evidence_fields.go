package httpapi

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// Present canonical decimal strings without passing them through a float.
func evidenceField(dataset string, field dataservice.Field, value any) string {
	label := field.Description
	raw, ok := value.(string)
	if !ok || field.Type != "decimal" {
		return fmt.Sprintf("%s：%v", label, value)
	}
	unit := map[string]string{"CNY": "元", "shares": "股", "times": "倍", "index_point": "点"}[field.Unit]
	switch {
	case dataset == "mkt.daily_bar" && (field.Name == "volume" || field.Name == "amount"):
		shift := yuanToWanShift
		label, unit = "成交额", "万元"
		if field.Name == "volume" {
			label, unit, shift = "成交量", "手", sharesToLotsShift
		}
		display, err := dailyTradeDisplayValue(raw, shift, false)
		if err != nil {
			return label + "：数值无效"
		}
		raw = display
	case field.Unit == "ratio":
		n, err := decimal.NewFromString(raw)
		if err != nil {
			return label + "：数值无效"
		}
		return label + "：" + n.Shift(2).String() + "%"
	case dataset == "mkt.daily_bar" && field.Unit == "CNY", dataset == "flow.block_trade" && field.Name == "price", dataset == "mkt.intraday_quote" && field.Name == "last_price":
		unit = "元/股"
	case dataset == "corp.dividend" && field.Name == "cash_per_10_shares":
		unit = "元/10股"
	}
	if unit != "" {
		raw += " " + unit
	}
	return label + "：" + raw
}
