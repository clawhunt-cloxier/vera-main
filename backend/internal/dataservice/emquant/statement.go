package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const statementIndicators = "IncomeStatement_9,IncomeStatement_61"

// StatementFields is a partial, unverified vendor response for a requested
// report period and statement type, not a published or PIT financial record.
// In particular, the response alone does not prove which filing/version the
// values came from, nor when they were disclosed.
type StatementFields struct {
	Security        string
	RequestedPeriod string
	RequestedType   int
	RevenueCNY      *string
	NetProfitCNY    *string
	Source          ArchiveRef
	Locators        map[string]string
}

// CollectStatementFields collects the two confirmed monetary fields, archives
// the response and retains its exact request provenance. Missing cells remain
// nil. A caller must not infer disclosure date or revision from the request.
func (c Collector) CollectStatementFields(ctx context.Context, code, reportDate string, statementType int) (StatementFields, error) {
	options, err := statementOptions(code, reportDate, statementType)
	if err != nil {
		return StatementFields{}, err
	}
	cube, ref, err := c.CollectCSS(ctx, code, statementIndicators, options)
	if err != nil {
		return StatementFields{}, err
	}
	return mapStatementFields(cube, ref, code, reportDate, statementType)
}

func statementOptions(code, reportDate string, statementType int) (string, error) {
	if code == "" || strings.TrimSpace(code) != code || strings.ContainsAny(code, ",\x00") {
		return "", errors.New("emquant: invalid security")
	}
	period, err := time.Parse("2006-01-02", reportDate)
	if err != nil || (period.Format("0102") != "0331" && period.Format("0102") != "0630" && period.Format("0102") != "0930" && period.Format("0102") != "1231") || statementType < 1 || statementType > 4 {
		return "", errors.New("emquant: invalid report period or statement type")
	}
	return fmt.Sprintf("ReportDate=%s,Type=%d", period.Format("20060102"), statementType), nil
}

func mapStatementFields(cube Cube, ref ArchiveRef, code, reportDate string, statementType int) (StatementFields, error) {
	options, err := statementOptions(code, reportDate, statementType)
	if err != nil {
		return StatementFields{}, err
	}
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return StatementFields{}, errors.New("emquant: missing statement archive provenance")
	}
	req, err := json.Marshal(request{Method: "css", Codes: code, Indicators: statementIndicators, Options: options})
	if err != nil || ref.RequestFingerprint != digest(req) {
		return StatementFields{}, errors.New("emquant: statement request provenance mismatch")
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v2", RequestFingerprint: ref.RequestFingerprint, ReceivedAt: ref.ReceivedAt.UTC(), Data: cube})
	if err != nil || digest(body) != ref.BodySHA256 {
		return StatementFields{}, errors.New("emquant: statement response provenance mismatch")
	}
	if len(cube.Codes) != 1 || !strings.EqualFold(cube.Codes[0], code) || len(cube.Indicators) != 2 || len(cube.Dates) != 1 || len(cube.Values) != 2 {
		return StatementFields{}, fmt.Errorf("%w: statement axes", ErrShape)
	}
	out := StatementFields{Security: cube.Codes[0], RequestedPeriod: reportDate, RequestedType: statementType, Source: ref, Locators: make(map[string]string, 2)}
	for i, name := range cube.Indicators {
		name = strings.ToUpper(name)
		if (name != "INCOMESTATEMENT_9" && name != "INCOMESTATEMENT_61") || out.Locators[name] != "" {
			return StatementFields{}, fmt.Errorf("%w: statement indicators", ErrShape)
		}
		cell, err := cube.At(0, i, 0)
		if err != nil {
			return StatementFields{}, err
		}
		out.Locators[name] = fmt.Sprintf("$.data.Values[%d]", i)
		if cell.Type == 0 && cell.Value == nil {
			continue
		}
		amount, err := decimalCell(cell)
		if err != nil {
			return StatementFields{}, fmt.Errorf("%w: invalid statement value", ErrShape)
		}
		value := amount.String()
		if name == "INCOMESTATEMENT_9" {
			out.RevenueCNY = &value
		} else {
			out.NetProfitCNY = &value
		}
	}
	return out, nil
}
