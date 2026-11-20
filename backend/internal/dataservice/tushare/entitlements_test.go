package tushare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestResearchAccessChecksAreBoundedAndRedacted(t *testing.T) {
	responses := map[string]string{
		"stock_basic":  `{"code":0,"data":{"fields":["ts_code"],"items":[["000000.SZ"]],"has_more":true}}`,
		"anns_d":       `{"code":40203,"msg":"private upstream details"}`,
		"income":       `{"code":0,"data":{"fields":[],"items":[]}}`,
		"balancesheet": `{"code":0,"data":{"fields":["ts_code"],"items":[["000000.SZ"]]}}`,
		"cashflow":     `{"code":-1,"msg":"每分钟超过频率 private upstream details"}`,
	}
	code := "000000.SZ"
	gates := 0
	requests := map[string]int{}
	p := &Provider{codes: []string{code, "000001.SZ"}, client: &client{token: "test-placeholder"}}
	p.client.gate = func(_ context.Context, f func() error) error {
		gates++
		return f()
	}
	p.client.http = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != apiURL || r.Method != "POST" {
			t.Fatal("unexpected request destination")
		}
		var q struct {
			API    string `json:"api_name"`
			Token  string
			Params map[string]any
			Fields string
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Fatal(err)
		}
		if q.Token != p.client.token || q.Params["ts_code"] != code || q.Params["limit"] != float64(1) || q.Params["offset"] != float64(0) || q.Fields != "ts_code" {
			t.Fatal("unexpected diagnostic scope")
		}
		if q.API != "stock_basic" && (q.Params["start_date"] == nil || q.Params["end_date"] == nil || q.API != "anns_d" && q.Params["report_type"] != "1") {
			t.Fatal("missing diagnostic filters")
		}
		requests[q.API]++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responses[q.API]))}, nil
	})}
	want := []EndpointCheck{
		{"stock_basic", "available", ""}, {"anns_d", "denied", "endpoint_permission"},
		{"income", "empty", "no_matching_row"}, {"balancesheet", "available", ""}, {"cashflow", "transient", "rate_limited"},
	}
	checks, err := p.ResearchAccess(t.Context())
	if err == nil || !slices.Equal(checks, want) || gates != len(responses) {
		t.Fatalf("unexpected diagnostic result: %+v %v gates=%d", checks, err, gates)
	}
	out, _ := json.Marshal(checks)
	for _, value := range []string{p.client.token, code, "private upstream details"} {
		if strings.Contains(string(out)+err.Error(), value) {
			t.Fatal("diagnostic leaked request or response values")
		}
	}
	for api := range responses {
		if requests[api] != 1 {
			t.Fatal("unexpected repeats or missing probe", api)
		}
		responses[api] = responses["stock_basic"]
	}
	if _, err := p.ResearchAccess(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestProbeOneRejectsInvalidAndClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		body, state, reason string
	}{
		{`{"code":40101}`, "denied", "authentication"},
		{`{"code":999,"msg":"vendor unavailable"}`, "transient", "upstream_unavailable"},
		{`{"code":0,"data":{"fields":["ts_code"],"items":[["other"]]}}`, "invalid_response", "unexpected_schema_or_subject"},
		{`{"code":0,"data":{"fields":[],"items":[],"has_more":true}}`, "invalid_response", "unexpected_schema_or_subject"},
	} {
		c, objects := testClient(t, tc.body)
		s, r := accessStatus(c.probeOne(t.Context(), "income", "000000.SZ", map[string]any{"offset": 0, "limit": 1}))
		if s != tc.state || r != tc.reason || len(objects) != 0 {
			t.Fatal(s, r, len(objects))
		}
	}
	c, _ := testClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checks, err := (&Provider{codes: []string{"000000.SZ"}, client: c}).ResearchAccess(ctx)
	if err == nil || len(checks) != 5 {
		t.Fatal("incomplete cancellation report")
	}
	for _, c := range checks {
		if c.Status != "unattempted" {
			t.Fatal(c)
		}
	}
	if state, _ := accessStatus(errors.New("private gate details")); state != "transient" {
		t.Fatal(state)
	}
}
