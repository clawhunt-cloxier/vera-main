package conversation

import (
	"regexp"
	"strconv"
	"strings"
)

// This validates an explicit tool parameter after routing. It does not
// interpret the research intent or convert calendar periods.
var tradingDayCount = regexp.MustCompile(`([+\-−负]?[0-9０-９零〇一二两三四五六七八九十百千]+(?:[[:space:]　]*[.．～~至到－-][[:space:]　]*[0-9０-９零〇一二两三四五六七八九十百千]+)?)[[:space:]　]*(?:个[[:space:]　]*)?交易日`)
var calendarPeriod = regexp.MustCompile(`[0-9０-９]{4}[-年/]|[0-9０-９零〇一二两三四五六七八九十百]+[[:space:]　]*(?:个[[:space:]　]*)?(?:自然日|日|天|周|月|季度|年)|(?:上|本|这)(?:一?周|个月|月|半年|季度|年)|[昨前]天|去年|至今|到现在|上市以来|从上市`)

var writtenWindows = map[string]int{
	"一": 1, "五": 5, "十": 10, "二十": 20, "六十": 60, "一百二十": 120, "二百五十": 250, "两百五十": 250,
}

func matchesTradingDays(text string, requested, effective int) bool {
	if calendarPeriod.MatchString(tradingDayCount.ReplaceAllString(text, "")) {
		return false
	}
	matches := tradingDayCount.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return requested <= 0
	}
	for _, match := range matches {
		number := strings.Map(func(r rune) rune {
			if r >= '０' && r <= '９' {
				return '0' + r - '０'
			}
			return r
		}, match[1])
		days, err := strconv.Atoi(number)
		if err != nil {
			days = writtenWindows[number]
		}
		if days <= 0 || days != effective {
			return false
		}
	}
	return true
}
