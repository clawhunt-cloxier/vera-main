package model

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type educationTransport func(*http.Request) (*http.Response, error)

func (f educationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const educationResponse = `{"model":"test-flash","usage":{
  "prompt_tokens":100,"completion_tokens":12,"server_tool_use":{"tool_search":1}
},"choices":[{"finish_reason":"stop","message":{
  "content":"示例说明",
  "annotations":[
    {"type":"url_citation","url_citation":{"url":"https://example.com/actual","title":"示例来源"}},
    {"type":"url_citation","url_citation":{"url":"https://example.com/incomplete"}},
    {"type":"url_citation","url_citation":{"url":"https://example.com/invented"}}
  ],
  "server_tool_calls":[{"calls":[
    {"type":"deepseek_web_search","kind":"search","status":"completed",
     "sources":[{"type":"url","url":"https://example.com/actual"}]},
    {"type":"deepseek_web_search","kind":"search","status":"failed",
     "sources":[{"type":"url","url":"https://example.com/incomplete"}]}
  ]}]
}}]}`

func TestOpenAIEducationSearch(t *testing.T) {
	profile := Profile{Name: "router", Adapter: "openai", BaseURL: "https://api.deepseek.com", ModelID: "deepseek-flash", AllowEducationSearch: true}
	call := Call{Profile: profile, EducationSearch: true, Messages: []Message{{Role: "user", Content: "什么是零息债券？"}}}
	t.Run("only cited sources returned by a completed search", func(t *testing.T) {
		client := &http.Client{Transport: educationTransport(func(r *http.Request) (*http.Response, error) {
			var body struct {
				Tools          []Tool          `json:"tools"`
				ResponseFormat json.RawMessage `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if r.Method != http.MethodPost || r.URL.String() != "https://api.deepseek.com/chat/completions" ||
				len(body.Tools) != 1 || body.Tools[0].Type != "deepseek_web_search" || body.ResponseFormat != nil {
				t.Fatalf("unexpected lookup request: %s %s %+v", r.Method, r.URL, body)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(educationResponse))}, nil
		})}
		out, err := (OpenAI{HTTP: client}).Do(t.Context(), call)
		want := []WebCitation{{URL: "https://example.com/actual", Title: "示例来源"}}
		if err != nil || out.Content != "示例说明" || !slices.Equal(out.WebCitations, want) {
			t.Fatalf("lookup returned %+v, %v", out, err)
		}
	})

	t.Run("does not follow a redirect", func(t *testing.T) {
		client := &http.Client{Transport: educationTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.deepseek.com" {
				t.Fatalf("unexpected redirect to %s", r.URL)
			}
			h := http.Header{"Location": {"https://example.com/other"}}
			return &http.Response{StatusCode: 307, Header: h, Body: http.NoBody}, nil
		})}
		if _, err := (OpenAI{HTTP: client}).Do(t.Context(), call); err == nil {
			t.Fatal("redirect accepted as a successful lookup")
		}
	})
}
