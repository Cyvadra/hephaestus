package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDeepSeekSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/tools/search" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", request.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["query"] != "macFUSE FSKit" || payload["count"] != float64(10) {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		_, _ = response.Write([]byte(`{"results":[{"title":"macFUSE 5","url":"https://example.com/a","snippet":"FSKit backend","summary":"long"},{"title":"","url":""}]}`))
	}))
	defer server.Close()

	provider := deepseekSearch{client: server.Client(), apiKey: "secret", baseURL: server.URL}
	results, err := provider.Search(context.Background(), "macFUSE FSKit", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "macFUSE 5" || results[0].URL != "https://example.com/a" || results[0].Snippet != "FSKit backend" {
		t.Fatalf("unexpected results %+v", results)
	}
}

func TestDeepSeekSearchTruncatesQueryAndReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		if n := len([]rune(payload["query"].(string))); n != deepseekMaxQueryRunes {
			t.Fatalf("query length = %d", n)
		}
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit","param":null,"code":"429"}}`))
	}))
	defer server.Close()

	provider := deepseekSearch{client: server.Client(), apiKey: "secret", baseURL: server.URL}
	_, err := provider.Search(context.Background(), strings.Repeat("搜", 200), 10)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429: rate limited") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeepSeekWebFetchProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/tools/fetch" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["url"] != "https://example.com/article" {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		_, _ = response.Write([]byte(`{"title":"Article","content":"Body text"}`))
	}))
	defer server.Close()

	provider := newDeepSeekWebFetchProvider("secret", server.URL)
	target, _ := url.Parse("https://example.com/article")
	text, err := provider.Fetch(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if text != "# Article\n\nBody text" {
		t.Fatalf("unexpected content: %q", text)
	}
}

func TestWebSearchUsesDeepSeekWhenKeyConfigured(t *testing.T) {
	tool := NewWebSearchTool(WebSearchConfig{DeepSeekAPIKey: "secret"})
	if got := providerNames(tool.fixed); len(got) != 1 || got[0] != "deepseek" {
		t.Fatalf("fixed providers = %v, want [deepseek]", got)
	}
}
