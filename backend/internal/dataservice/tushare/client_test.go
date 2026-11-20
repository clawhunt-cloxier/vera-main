package tushare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type memoryArchive map[string]string

func (a memoryArchive) Put(_ context.Context, hash string, body []byte) (string, error) {
	a[hash] = string(body)
	return "test/" + hash, nil
}

func testClient(t *testing.T, pages ...string) (*client, memoryArchive) {
	t.Helper()
	var calls int
	archive := memoryArchive{}
	transport := testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != apiURL || r.Method != "POST" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		if calls >= len(pages) {
			t.Fatal("pagination did not terminate")
		}
		var q struct {
			Token  string
			Params map[string]any
			Fields string
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Fatal(err)
		}
		if q.Token != "secret-placeholder" || int(q.Params["offset"].(float64)) != calls {
			t.Fatalf("wrong token/offset for page %d", calls)
		}
		body := pages[calls]
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return &client{token: "secret-placeholder", http: &http.Client{Transport: transport}, archive: archive, gate: func(_ context.Context, f func() error) error { return f() }}, archive
}

func TestAuthenticatedPaginationAndArchivedPrecision(t *testing.T) {
	c, archive := testClient(t, `{"code":0,"data":{"fields":["close","ts_code"],"items":[[1234567891234567.890001,"600519.SH"]],"has_more":true}}`, `{"code":0,"data":{"fields":["ts_code","close"],"items":[["600519.SH",null]]}}`, `{"code":0,"data":{"fields":[],"items":[]}}`)
	rows, err := c.Rows(context.Background(), "daily", map[string]string{"ts_code": "600519.SH"}, []string{"ts_code", "close"}, 1)
	if err != nil || len(rows) != 2 {
		t.Fatalf("pages: %d %v", len(rows), err)
	}
	if rows[0].Values["close"] != "1234567891234567.890001" {
		t.Fatal("lost upstream precision")
	}
	if _, ok := rows[1].Values["close"]; ok {
		t.Fatal("null became a value")
	}
	for _, r := range rows {
		var manifest struct {
			Hash   string `json:"response_sha256"`
			API    string `json:"api_name"`
			Params map[string]any
		}
		if err := json.Unmarshal([]byte(archive[r.RawSHA256]), &manifest); err != nil || manifest.API != "daily" || !strings.Contains(archive[manifest.Hash], `"items"`) || !strings.HasSuffix(r.RawLocator, "items[0]") {
			t.Fatalf("broken lineage: %v", err)
		}
	}
	for _, body := range archive {
		if strings.Contains(body, c.token) {
			t.Fatal("archived credential")
		}
	}
}

func TestEmptyHistoryPagePreservesRequestEvidence(t *testing.T) {
	const empty = `{"code":0,"data":{"fields":[],"items":[]}}`
	c, archive := testClient(t, empty)
	rows, err := c.Rows(t.Context(), "margin_detail", map[string]string{"ts_code": "600519.SH"}, []string{"rzye"}, 100)
	if err != nil || len(rows) != 0 || len(archive) != 2 {
		t.Fatalf("empty observation: rows=%d objects=%d err=%v", len(rows), len(archive), err)
	}
	for _, body := range archive {
		if body == empty {
			continue
		}
		var request struct {
			API    string `json:"api_name"`
			Hash   string `json:"response_sha256"`
			Params map[string]any
		}
		if json.Unmarshal([]byte(body), &request) != nil || request.API != "margin_detail" || request.Params["ts_code"] != "600519.SH" || archive[request.Hash] != empty || strings.Contains(body, c.token) {
			t.Fatal("missing credential-free request or empty response")
		}
	}
}

func TestErrorsDoNotLeakTokenOrPublishBadRows(t *testing.T) {
	cases := []struct {
		body  string
		class ds.ErrClass
	}{
		{`{"code":2002,"msg":"没有访问该接口的权限"}`, ds.ErrNotCovered},
		{`{"code":-2001,"msg":"token无效: secret-placeholder"}`, ds.ErrAuthFailed},
		{`{"code":0,"data":{"fields":["other"],"items":[["bad shape"]]}}`, ds.ErrSchemaChanged},
		{`{"code":0,"data":{"fields":["close"],"items":[[1,2]]}}`, ds.ErrSchemaChanged},
		{`{"data":{"fields":["close"],"items":[[1]]}}`, ds.ErrSchemaChanged},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			c, archive := testClient(t, tc.body)
			_, err := c.Rows(context.Background(), "daily", nil, []string{"close"}, 1)
			var pe *ds.ProviderError
			if !errors.As(err, &pe) || pe.Class != tc.class || len(archive) != 0 || strings.Contains(err.Error(), c.token) {
				t.Fatalf("wrong error class or archive state: %v", err)
			}
		})
	}
	body := `{"code":0,"data":{"fields":["close"],"items":[[123]]}}`
	c, _ := testClient(t, body, body)
	if rows, err := c.Rows(context.Background(), "daily", nil, []string{"close"}, 1); err == nil || len(rows) != 0 {
		t.Fatal("repeating page accepted")
	}
	if _, _, err := c.Archive(context.Background(), "sample", []byte("secret-placeholder")); err == nil {
		t.Fatal("credential accepted for archival")
	}
}
