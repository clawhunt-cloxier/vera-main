package tushare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

const apiURL = "https://api.tushare.pro"
const maxPageBytes = 32 << 20

// No endpoint or query name can be supplied by a research request or model.
var registeredAPIs = strings.Fields("stock_basic trade_cal index_member_all daily adj_factor daily_basic index_daily sw_daily margin_secs margin_detail suspend_d stk_holdernumber moneyflow block_trade anns_d income balancesheet cashflow fina_mainbz major_news")

type client struct {
	token   string
	http    *http.Client
	archive objectArchive
	gate    func(context.Context, func() error) error
}

type objectArchive interface {
	Put(context.Context, string, []byte) (string, error)
}

// Use the configured HTTP transport for the isolated filing client as well.
// That client builds its own credential-free requests and disallows redirects.
func (c *client) FilingTransport() http.RoundTripper { return c.http.Transport }

func (c *client) Archive(ctx context.Context, kind string, data []byte) (string, string, error) {
	if len(data) == 0 || bytes.Contains(data, []byte(c.token)) {
		return "", "", errors.New("tushare: refused empty or credential-bearing archive")
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	ref, err := c.archive.Put(ctx, hash, data)
	return ref, hash, err
}

type responseData struct {
	Fields  []string            `json:"fields"`
	Items   [][]json.RawMessage `json:"items"`
	HasMore bool                `json:"has_more"`
}

func classified(class ds.ErrClass, api string) error {
	return &ds.ProviderError{Class: class, Err: fmt.Errorf("TuShare %s", api)}
}

// Only classified errors leave this layer. An upstream error may echo the POST body.
func responseError(api string, code int, msg string) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "每分钟"), strings.Contains(msg, "频次"), strings.Contains(msg, "频率"), strings.Contains(lower, "too many"):
		return classified(ds.ErrRateLimited, api)
	case strings.Contains(lower, "token"):
		return classified(ds.ErrAuthFailed, api)
	case code == 2002, strings.Contains(msg, "没有访问"), strings.Contains(msg, "权限"), strings.Contains(msg, "积分"):
		return classified(ds.ErrNotCovered, api)
	case code == -2001, strings.Contains(msg, "参数"):
		return classified(ds.ErrSchemaChanged, api)
	default:
		return classified(ds.ErrUpstreamUnavailable, api)
	}
}

func retryable(err error) bool {
	var e *ds.ProviderError
	return errors.As(err, &e) && (e.Class == ds.ErrRateLimited || e.Class == ds.ErrUpstreamUnavailable)
}

func (c *client) probeSecurity(ctx context.Context, code string) error {
	const api = "stock_basic"
	body, err := json.Marshal(map[string]any{"api_name": api, "token": c.token, "params": map[string]any{"ts_code": code, "list_status": "L", "limit": 1, "offset": 0}, "fields": "ts_code"})
	if err != nil {
		return err
	}
	return c.gate(ctx, func() error {
		_, data, _, err := c.request(ctx, api, body)
		if err != nil {
			return err
		}
		rows, err := decodeRows(api, []string{"ts_code"}, data)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0]["ts_code"] != code {
			return classified(ds.ErrNotCovered, api)
		}
		return nil
	})
}

func (c *client) request(ctx context.Context, api string, body []byte) ([]byte, *responseData, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, time.Time{}, ctx.Err()
		}
		return nil, nil, time.Time{}, classified(ds.ErrUpstreamUnavailable, api)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		class := ds.ErrSchemaChanged
		switch {
		case resp.StatusCode == 429:
			class = ds.ErrRateLimited
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			class = ds.ErrAuthFailed
		case resp.StatusCode >= 500:
			class = ds.ErrUpstreamUnavailable
		}
		return nil, nil, time.Time{}, classified(class, api)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, nil, time.Time{}, classified(ds.ErrUpstreamUnavailable, api)
	}
	at := time.Now().UTC()
	var envelope struct {
		Code *int          `json:"code"`
		Msg  string        `json:"msg"`
		Data *responseData `json:"data"`
	}
	if len(raw) > maxPageBytes || json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil {
		return nil, nil, at, classified(ds.ErrSchemaChanged, api)
	}
	if *envelope.Code != 0 {
		return nil, nil, at, responseError(api, *envelope.Code, envelope.Msg)
	}
	if envelope.Data == nil || envelope.Data.Items == nil {
		return nil, nil, at, classified(ds.ErrSchemaChanged, api)
	}
	return raw, envelope.Data, at, nil
}

func (c *client) Rows(ctx context.Context, api string, params map[string]string, fields []string, pageSize int) ([]Row, error) {
	if !slices.Contains(registeredAPIs, api) || pageSize < 1 || pageSize > 10000 || len(fields) == 0 || c.gate == nil {
		return nil, errors.New("tushare: invalid registered request")
	}
	p := make(map[string]any, len(params)+2)
	for k, v := range params {
		p[k] = v
	}
	var result []Row
	seen := map[[32]byte]bool{}
	for page := 0; page < 2000; page++ {
		p["limit"], p["offset"] = pageSize, len(result)
		body, err := json.Marshal(map[string]any{"api_name": api, "params": p, "fields": strings.Join(fields, ","), "token": c.token})
		if err != nil {
			return nil, err
		}
		var raw []byte
		var data *responseData
		var received time.Time
		for attempt := 0; attempt < 3; attempt++ {
			err = c.gate(ctx, func() error {
				var e error
				raw, data, received, e = c.request(ctx, api, body)
				return e
			})
			if !retryable(err) || attempt == 2 {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second << attempt):
			}
		}
		if err != nil {
			return nil, err
		}
		var pageRows []map[string]string
		if len(data.Items) != 0 {
			pageRows, err = decodeRows(api, fields, data)
			if err != nil {
				return nil, err
			}
			// Detect endpoints ignoring offset, without relying on request IDs or envelope hashes.
			cells, _ := json.Marshal(data.Items)
			digest := sha256.Sum256(cells)
			if seen[digest] {
				return nil, classified(ds.ErrSchemaChanged, api)
			}
			seen[digest] = true
		} else if data.HasMore {
			return nil, classified(ds.ErrSchemaChanged, api)
		}
		// Archive valid empty responses too: they document a checked range and
		// distinguish an empty final page from an incomplete collection.
		ref, hash, err := c.Archive(ctx, "response", raw)
		if err != nil {
			return nil, err
		}
		manifest, _ := json.Marshal(map[string]any{"provider": "tushare", "api_name": api, "params": p, "fields": fields, "response_ref": ref, "response_sha256": hash})
		// Link the response in a request manifest and address evidence by response row.
		bound, proof, err := c.Archive(ctx, "request", manifest)
		if err != nil {
			return nil, err
		}
		for i, values := range pageRows {
			result = append(result, Row{Values: values, RawRef: bound, RawSHA256: proof, RawLocator: "response_ref#$.data.items[" + strconv.Itoa(i) + "]", ReceivedAt: received})
		}
		if len(pageRows) < pageSize && !data.HasMore {
			return result, nil
		}
	}
	return nil, errors.New("tushare: pagination limit exceeded")
}

func decodeRows(api string, fields []string, data *responseData) ([]map[string]string, error) {
	want := map[string]bool{}
	got := map[string]bool{}
	for _, k := range fields {
		want[k] = true
	}
	for _, k := range data.Fields {
		got[k] = true
	}
	if len(fields) != len(want) || len(data.Fields) != len(got) || !maps.Equal(want, got) {
		return nil, classified(ds.ErrSchemaChanged, api)
	}
	out := make([]map[string]string, 0, len(data.Items))
	for _, cells := range data.Items {
		if len(cells) != len(data.Fields) {
			return nil, classified(ds.ErrSchemaChanged, api)
		}
		v := map[string]string{}
		for i, cell := range cells {
			var val any
			dec := json.NewDecoder(bytes.NewReader(cell))
			dec.UseNumber()
			if dec.Decode(&val) != nil {
				return nil, classified(ds.ErrSchemaChanged, api)
			}
			switch x := val.(type) {
			case nil:
			case string:
				if x != "" {
					v[data.Fields[i]] = x
				}
			case json.Number:
				v[data.Fields[i]] = x.String()
			default:
				return nil, classified(ds.ErrSchemaChanged, api)
			}
		}
		out = append(out, v)
	}
	return out, nil
}
