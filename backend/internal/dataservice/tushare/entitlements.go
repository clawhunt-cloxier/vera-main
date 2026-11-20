package tushare

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// EndpointCheck contains only controlled identifiers and statuses, since upstream
// errors may include credentials and even one row may contain licensed content.
type EndpointCheck struct {
	API    string `json:"api"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ResearchAccess makes at most one one-row request per endpoint, all for the
// first configured security. Do not paginate, retry, archive or publish probes.
func (p *Provider) ResearchAccess(ctx context.Context) ([]EndpointCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	const layout = "20060102"
	end := p.Now().In(filingZone)
	code := p.codes[0]
	checks := make([]EndpointCheck, 0, 5)
	complete := true
	for _, api := range []string{"stock_basic", "anns_d", "income", "balancesheet", "cashflow"} {
		check := EndpointCheck{API: api}
		if ctx.Err() != nil {
			check.Status, check.Reason = "unattempted", "context_ended"
		} else {
			params := map[string]any{"ts_code": code, "limit": 1, "offset": 0}
			if api == "stock_basic" {
				params["list_status"] = "L"
			} else {
				params["start_date"], params["end_date"] = end.AddDate(-1, 0, 0).Format(layout), end.Format(layout)
				if api != "anns_d" {
					params["report_type"] = "1"
				}
			}
			err := p.client.probeOne(ctx, api, code, params)
			check.Status, check.Reason = accessStatus(err)
		}
		checks = append(checks, check)
		complete = complete && check.Status == "available"
	}
	if !complete {
		return checks, errors.New("TuShare 探测未在所有接口取得样本，请查看逐项状态")
	}
	return checks, nil
}

func (c *client) probeOne(ctx context.Context, api, code string, params map[string]any) error {
	body, err := json.Marshal(map[string]any{"api_name": api, "token": c.token, "params": params, "fields": "ts_code"})
	if err != nil {
		return err
	}
	return c.gate(ctx, func() error {
		_, data, _, err := c.request(ctx, api, body)
		if err != nil {
			return err
		}
		if len(data.Items) == 0 && !data.HasMore {
			return classified(ds.ErrEmptyValid, api)
		}
		rows, err := decodeRows(api, []string{"ts_code"}, data)
		if err != nil || len(rows) != 1 || rows[0]["ts_code"] != code {
			return classified(ds.ErrSchemaChanged, api)
		}
		return nil
	})
}

func accessStatus(err error) (status, reason string) {
	if err == nil {
		return "available", ""
	}
	var e *ds.ProviderError
	if !errors.As(err, &e) {
		return "transient", "probe_interrupted"
	}
	switch e.Class {
	case ds.ErrEmptyValid:
		return "empty", "no_matching_row"
	case ds.ErrNotCovered:
		return "denied", "endpoint_permission"
	case ds.ErrAuthFailed:
		return "denied", "authentication"
	case ds.ErrRateLimited:
		return "transient", "rate_limited"
	case ds.ErrUpstreamUnavailable:
		return "transient", "upstream_unavailable"
	default:
		return "invalid_response", "unexpected_schema_or_subject"
	}
}
