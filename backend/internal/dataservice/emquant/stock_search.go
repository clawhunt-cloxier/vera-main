package emquant

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// IsBareStockCode returns true for 6-digit numeric strings (A-share code without exchange suffix).
func IsBareStockCode(q string) bool {
	if len(q) != 6 {
		return false
	}
	for _, ch := range q {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// IsStockName returns true for strings that contain letter/CJK characters and only valid name chars.
func IsStockName(q string) bool {
	if len([]rune(q)) > 32 {
		return false
	}
	hasLetter := false
	for _, ch := range q {
		hasLetter = hasLetter || unicode.IsLetter(ch)
		if !unicode.IsLetter(ch) && !unicode.IsNumber(ch) && !strings.ContainsRune(" *.-·()（）", ch) {
			return false
		}
	}
	return hasLetter
}

// ExactStockMatch reports whether a candidate answers an explicit name or code.
func ExactStockMatch(entity dataservice.EntityRow, query string) bool {
	return entity.Kind == "stock" && (strings.EqualFold(entity.Name, query) ||
		strings.EqualFold(entity.Code, query) || IsBareStockCode(query) && strings.HasPrefix(entity.Code, query+"."))
}

// DiscoverStocks calls the Choice API for the query, upserts discovered entities, and
// returns the matched EntityRows. Only searches stocks; industry/market have no equivalent.
func (b *BriefService) DiscoverStocks(ctx context.Context, catalog *dataservice.Store, q string) ([]dataservice.EntityRow, error) {
	runtime := b.Runtime
	if runtime == nil {
		return nil, errors.New("stock search runtime unavailable")
	}

	var table Table
	colName, colCode := -1, -1
	queryCode := strings.ToUpper(q)
	qualified := IsListedStockCode(queryCode)
	codeLookup := qualified || IsBareStockCode(q)

	switch {
	case codeLookup:
		t, _, err := runtime.CollectStockCodeCandidates(ctx, queryCode[:6])
		if err != nil {
			return nil, err
		}
		table = t
		for i, col := range table.Columns {
			if strings.EqualFold(col, "FULLCODES") {
				colCode = i
			}
		}
	case IsStockName(q):
		t, _, err := runtime.CollectStockNameCandidates(ctx, q)
		if err != nil {
			return nil, err
		}
		table = t
		for i, col := range table.Columns {
			switch strings.ToUpper(col) {
			case "SECURITYSHORTNAME":
				colName = i
			case "SECUCODE":
				colCode = i
			}
		}
	default:
		return nil, nil
	}

	if table.Rows == 0 {
		return nil, nil
	}
	if len(table.Columns) == 0 || colCode < 0 || (!codeLookup && colName < 0) || table.Rows != len(table.Values)/len(table.Columns) || len(table.Values)%len(table.Columns) != 0 {
		return nil, ErrShape
	}

	numCols := len(table.Columns)
	var out []dataservice.EntityRow
	seen := map[string]bool{}

	for row := 0; row < table.Rows; row++ {
		base := row * numCols
		var name string
		if colName >= 0 && base+colName < len(table.Values) {
			name, _ = table.Values[base+colName].Value.(string)
		}
		rawCode, _ := table.Values[base+colCode].Value.(string)
		for _, code := range matchedSearchCodes(rawCode, queryCode, codeLookup) {
			if seen[code] {
				continue
			}
			seen[code] = true
			label := name
			if label == "" {
				label = code
			}
			e, err := catalog.UpsertSearchEntity(ctx, "stock", code, label, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, nil
}

func matchedSearchCodes(raw, query string, codeLookup bool) []string {
	var codes []string
	for _, part := range strings.Split(raw, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if IsListedStockCode(code) && (!codeLookup || query == code || query == code[:6]) {
			codes = append(codes, code)
		}
	}
	return codes
}

// IsListedStockCode checks that a code looks like a qualified A-share code (NNNNNN.XX).
func IsListedStockCode(code string) bool {
	parts := strings.SplitN(code, ".", 2)
	if len(parts) != 2 || len(parts[0]) != 6 {
		return false
	}
	for _, ch := range parts[0] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	switch parts[1] {
	case "SH", "SZ", "BJ":
		return true
	}
	return false
}
