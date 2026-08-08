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

func TestFirecrawlWebFetchProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/scrape" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header: %q", request.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["url"] != "https://example.com/article" || payload["onlyMainContent"] != true {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"data":{"markdown":"# Article\n\nContent"}}`))
	}))
	defer server.Close()

	provider := newFirecrawlWebFetchProvider("secret", server.URL)
	target, _ := url.Parse("https://example.com/article")
	text, err := provider.Fetch(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if text != "# Article\n\nContent" {
		t.Fatalf("unexpected markdown: %q", text)
	}
}

func TestFirecrawlWebFetchProviderErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "rate limited", statusCode: http.StatusTooManyRequests, body: `{"error":"limit"}`, want: "HTTP 429"},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`, want: "invalid JSON"},
		{name: "provider failure", statusCode: http.StatusOK, body: `{"success":false,"error":"blocked"}`, want: "scrape failed: blocked"},
		{name: "empty markdown", statusCode: http.StatusOK, body: `{"success":true,"data":{"markdown":""}}`, want: "empty markdown"},
		{name: "private redirect", statusCode: http.StatusOK, body: `{"success":true,"data":{"markdown":"content","metadata":{"url":"http://127.0.0.1/private"}}}`, want: "unsafe final URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.statusCode)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			provider := newFirecrawlWebFetchProvider("secret", server.URL)
			target, _ := url.Parse("https://example.com")
			_, err := provider.Fetch(context.Background(), target)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}
