package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const dailyIndicators = "VOLUME,AMOUNT,TURN"
const dailyOptions = "Period=1,AdjustFlag=1,CurType=2,FillData=0"

// DailyLiquidity is a partial observation, not a complete mkt.daily_bar. The
// latter also requires verified price, adjustment and suspension semantics.
type DailyLiquidity struct {
	Security      string
	TradeDate     string
	VolumeShares  string
	AmountCNY     string
	TurnoverRatio *string
	Source        ArchiveRef
	Locators      map[string]string
}

func decimalCell(cell Cell) (decimal.Decimal, error) {
	var value string
	switch cell.Type {
	case 1, 3, 4, 6, 8:
		n, ok := cell.Value.(int64)
		if !ok {
			return decimal.Zero, ErrShape
		}
		value = strconv.FormatInt(n, 10)
	case 2, 5, 7, 9:
		n, ok := cell.Value.(uint64)
		if !ok {
			return decimal.Zero, ErrShape
		}
		value = strconv.FormatUint(n, 10)
	case 10, 11:
		n, ok := cell.Value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return decimal.Zero, ErrShape
		}
		bits := 64
		if cell.Type == 10 {
			bits = 32
		}
		value = strconv.FormatFloat(n, 'f', -1, bits)
	default:
		return decimal.Zero, ErrShape
	}
	result, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, ErrShape
	}
	return result, nil
}

// CollectDailyLiquidity only returns values after the exact SDK request has
// been archived and its response shape, source and units checked. An empty
// result does not prove the market was closed or the security was suspended.
func (c Collector) CollectDailyLiquidity(ctx context.Context, code, from, to string) ([]DailyLiquidity, error) {
	if code == "" || strings.TrimSpace(code) != code || strings.ContainsAny(code, ",\x00") {
		return nil, errors.New("emquant: invalid security")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, errors.New("emquant: invalid start date")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Before(start) || end.Sub(start) > 3660*24*time.Hour {
		return nil, errors.New("emquant: invalid end date")
	}
	cube, ref, err := c.CollectCSD(ctx, code, dailyIndicators, from, to, dailyOptions)
	if err != nil {
		return nil, err
	}
	return mapDailyLiquidity(cube, ref, code, start, end)
}

func mapDailyLiquidity(c Cube, ref ArchiveRef, code string, start, end time.Time) ([]DailyLiquidity, error) {
	if ref.BodyRef == "" || len(ref.BodySHA256) != 64 || ref.ReceivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return nil, errors.New("emquant: missing archive provenance")
	}
	req, _ := json.Marshal(request{Method: "csd", Codes: code, Indicators: dailyIndicators, From: start.Format("2006-01-02"), To: end.Format("2006-01-02"), Options: dailyOptions})
	if ref.RequestFingerprint != digest(req) {
		return nil, errors.New("emquant: daily request provenance mismatch")
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v2", RequestFingerprint: ref.RequestFingerprint, ReceivedAt: ref.ReceivedAt.UTC(), Data: c})
	if err != nil || digest(body) != ref.BodySHA256 {
		return nil, errors.New("emquant: daily response provenance mismatch")
	}
	if len(c.Codes) != 1 || !strings.EqualFold(c.Codes[0], code) || len(c.Indicators) != 3 || len(c.Dates) == 0 || len(c.Values) != len(c.Dates)*3 {
		return nil, fmt.Errorf("%w: daily axes", ErrShape)
	}
	indexes := map[string]int{}
	for i, name := range c.Indicators {
		name = strings.ToUpper(name)
		if name != "VOLUME" && name != "AMOUNT" && name != "TURN" {
			return nil, fmt.Errorf("%w: unexpected daily field", ErrShape)
		}
		if _, exists := indexes[name]; exists {
			return nil, fmt.Errorf("%w: duplicate daily field", ErrShape)
		}
		indexes[name] = i
	}
	out := make([]DailyLiquidity, 0, len(c.Dates))
	var previous time.Time
	for i, text := range c.Dates {
		day, err := parseVendorDate(text)
		if err != nil || day.Before(start) || day.After(end) || (!previous.IsZero() && !day.After(previous)) {
			return nil, fmt.Errorf("%w: daily date or order", ErrShape)
		}
		row := DailyLiquidity{Security: c.Codes[0], TradeDate: day.Format("2006-01-02"), Source: ref, Locators: make(map[string]string)}
		for _, name := range []string{"VOLUME", "AMOUNT", "TURN"} {
			idx := indexes[name]
			cell, err := c.At(0, idx, i)
			if err != nil {
				return nil, err
			}
			if cell.Type == 0 && name == "TURN" && cell.Value == nil {
				continue
			}
			n, err := decimalCell(cell)
			if err != nil || n.IsNegative() {
				return nil, fmt.Errorf("%w: invalid %s", ErrShape, name)
			}
			switch name {
			case "VOLUME":
				if !n.Equal(n.Truncate(0)) || (cell.Type == 10 && n.GreaterThanOrEqual(decimal.NewFromInt(16777216))) || (cell.Type == 11 && n.GreaterThanOrEqual(decimal.NewFromInt(9007199254740992))) {
					return nil, fmt.Errorf("%w: invalid volume precision", ErrShape)
				}
				row.VolumeShares = n.String()
			case "AMOUNT":
				row.AmountCNY = n.String()
			default:
				v := n.Shift(-2).String()
				row.TurnoverRatio = &v
			}
			row.Locators[name] = fmt.Sprintf("$.data.Values[%d]", i*len(c.Indicators)+idx)
		}
		out = append(out, row)
		previous = day
	}
	return out, nil
}
