package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

var searchResultLine = regexp.MustCompile(`(?m)^\d+\.\s+`)

// TestWebSearchProvidersLive exercises real providers. It is gated behind
// HEPHAESTUS_RUN_LIVE_TESTS because it needs network access and a local
// .env with provider keys; it would otherwise fail in a clean CI run.
func TestWebSearchProvidersLive(t *testing.T) {
	if os.Getenv("HEPHAESTUS_RUN_LIVE_TESTS") == "" {
		t.Skip("set HEPHAESTUS_RUN_LIVE_TESTS=1 to run live web-search tests (requires network and .env)")
	}
	loadRepositoryEnv(t)
	testCases := []struct {
		name   string
		config WebSearchConfig
		query  string
	}{
		{name: "sogou", config: WebSearchConfig{Provider: "sogou", SogouEnabled: true}, query: "人工智能 开源项目"},
		{name: "duckduckgo", config: WebSearchConfig{Provider: "duckduckgo"}, query: "open source artificial intelligence"},
	}
	if keys := os.Getenv("HEPHAESTUS_WEB_SEARCH_BRAVE_API_KEYS"); strings.TrimSpace(keys) != "" {
		testCases = append(testCases, struct {
			name   string
			config WebSearchConfig
			query  string
		}{name: "brave", config: WebSearchConfig{Provider: "brave", BraveAPIKeys: []string{keys}}, query: "open source artificial intelligence"})
	}
	if keys := os.Getenv("HEPHAESTUS_WEB_SEARCH_TAVILY_API_KEYS"); strings.TrimSpace(keys) != "" {
		testCases = append(testCases, struct {
			name   string
			config WebSearchConfig
			query  string
		}{name: "tavily", config: WebSearchConfig{Provider: "tavily", TavilyAPIKeys: []string{keys}}, query: "open source artificial intelligence"})
	}
	if keys := splitTestKeys(os.Getenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS")); len(keys) > 0 {
		testCases = append(testCases, struct {
			name   string
			config WebSearchConfig
			query  string
		}{name: "serpapi", config: WebSearchConfig{Provider: "serpapi", SerpAPIKeys: keys, SerpAPIEngine: os.Getenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE")}, query: "open source artificial intelligence"})
	}
	if baseURL := os.Getenv("HEPHAESTUS_WEB_SEARCH_SEARXNG_BASE_URL"); strings.TrimSpace(baseURL) != "" {
		testCases = append(testCases, struct {
			name   string
			config WebSearchConfig
			query  string
		}{name: "searxng", config: WebSearchConfig{Provider: "searxng", SearXNGBaseURL: baseURL}, query: "open source artificial intelligence"})
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			tool, err := NewWebSearchTool(testCase.config)
			if err != nil {
				t.Fatalf("construct provider: %s", err)
			}
			result := tool.Execute(context.Background(), map[string]any{"query": testCase.query, "count": float64(5)})
			if result.IsError {
				t.Fatalf("provider request failed: %s", result.ForLLM)
			}
			if found := len(searchResultLine.FindAllString(result.ForLLM, -1)); found <= 1 {
				t.Fatalf("expected more than one search result, found %d: %s", found, result.ForLLM)
			}
		})
	}
}

func splitTestKeys(value string) []string {
	var keys []string
	for _, key := range strings.Split(value, ",") {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func loadRepositoryEnv(t *testing.T) {
	t.Helper()
	path := filepath.Join("..", "..", ".env")
	if err := godotenv.Load(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("load %s: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("required live-test environment file %s is unavailable: %v", path, err)
	}
}

func TestDecodeSearchFormatsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Example","url":"https://example.test","content":"A result"}]}`))
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := decodeSearch(server.Client(), request, "results", 10)
	if err != nil || len(results) != 1 || results[0].Title != "Example" || results[0].URL != "https://example.test" || results[0].Snippet != "A result" {
		t.Fatalf("unexpected results %+v, error %v", results, err)
	}
}

func TestWebSearchRejectsUnconfiguredProvider(t *testing.T) {
	if _, err := NewWebSearchTool(WebSearchConfig{Provider: "brave"}); err == nil {
		t.Fatal("expected construction error for an explicit provider with no keys")
	}
}

func TestWebSearchRejectsUnconfiguredSerpAPI(t *testing.T) {
	if _, err := NewWebSearchTool(WebSearchConfig{Provider: "serpapi"}); err == nil {
		t.Fatal("expected construction error for SerpApi with no keys")
	}
}

func TestSogouRequiresExplicitEnablement(t *testing.T) {
	if _, err := NewWebSearchTool(WebSearchConfig{Provider: "sogou"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected unconfigured Sogou provider error, got %v", err)
	}
}

func TestWebSearchAutoResolvesWithoutKeys(t *testing.T) {
	tool, err := NewWebSearchTool(WebSearchConfig{})
	if err != nil {
		t.Fatalf("auto config should always resolve: %v", err)
	}
	if got := providerNames(tool.providers); len(got) != 1 || got[0] != "duckduckgo" {
		t.Fatalf("expected only duckduckgo with no keys, got %v", got)
	}
}

func TestWebSearchAutoPrefersKeyedProviders(t *testing.T) {
	tool, err := NewWebSearchTool(WebSearchConfig{BraveAPIKeys: []string{"k1"}, TavilyAPIKeys: []string{"k2"}, SerpAPIKeys: []string{"k3"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := providerNames(tool.providers), []string{"brave", "tavily", "serpapi", "duckduckgo"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider order = %v, want %v", got, want)
	}
}

func TestSerpAPISearchUsesConfiguredEngineAndLocation(t *testing.T) {
	testCases := []struct {
		name       string
		engine     string
		parameters map[string]string
	}{
		{name: "google light default", parameters: map[string]string{"engine": "google_light", "location": "United Kingdom", "gl": "uk", "hl": "en"}},
		{name: "bing", engine: "bing", parameters: map[string]string{"engine": "bing", "location": "United Kingdom", "cc": "gb", "mkt": "en-GB"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				query := request.URL.Query()
				if query.Get("q") != "golang" || query.Get("api_key") != "secret" {
					t.Fatalf("unexpected SerpApi query: %s", request.URL.RawQuery)
				}
				for name, want := range testCase.parameters {
					if got := query.Get(name); got != want {
						t.Fatalf("parameter %s = %q, want %q", name, got, want)
					}
				}
				body := `{"organic_results":[{"title":"One","link":"https://one.test","snippet":"First"},{"title":"Two","link":"https://two.test","snippet":"Second"}]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			provider := serpapiSearch{keys: NewAPIKeyPool([]string{"secret"}), engine: testCase.engine, transport: transport}
			results, err := provider.Search(context.Background(), "golang", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0] != (SearchResult{Title: "One", URL: "https://one.test", Snippet: "First"}) {
				t.Fatalf("unexpected SerpApi results: %+v", results)
			}
		})
	}
}

func TestSerpAPISearchPropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})
	provider := serpapiSearch{keys: NewAPIKeyPool([]string{"secret"}), transport: transport}
	_, err := provider.Search(ctx, "golang", 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func providerNames(providers []Provider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name()
	}
	return names
}

func TestSogouSearchParsesWAPResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("keyword") != "heph" || request.URL.Query().Get("p") != "1" {
			t.Fatalf("unexpected Sogou query: %s", request.URL.RawQuery)
		}
		_, _ = w.Write([]byte(strings.Repeat(" ", 200) + `<a class="resultLink" href="/link?url=https%3A%2F%2Fexample.test" id="sogou_vr_1_1">Example &amp; Result</a><div class="clamp2">A <b>snippet</b></div>`))
	}))
	defer server.Close()
	provider := sogouSearch{client: server.Client(), endpoint: server.URL}
	results, err := provider.Search(context.Background(), "heph", 1)
	if err != nil || len(results) != 1 {
		t.Fatalf("unexpected Sogou results %+v, error %v", results, err)
	}
	if results[0].Title != "Example & Result" || results[0].URL != "https://example.test" || results[0].Snippet != "A snippet" {
		t.Fatalf("unexpected Sogou result %+v", results[0])
	}
}

func TestExtractDuckDuckGoStripsRutToken(t *testing.T) {
	document := `<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage%3Fa%3D1%26b%3D2&rut=abc123">Example</a><a class="result__snippet">Some snippet</a>`
	results := extractDuckDuckGoResults(document, 5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %+v", results)
	}
	if results[0].URL != "https://example.com/page?a=1&b=2" {
		t.Fatalf("expected rut token stripped, got %q", results[0].URL)
	}
}

func TestRenderSearchResults(t *testing.T) {
	rendered := RenderSearchResults("duckduckgo", "golang", []SearchResult{
		{Title: "Go", URL: "https://go.dev", Snippet: "The Go language"},
	})
	if !strings.Contains(rendered, "1. Go") || !strings.Contains(rendered, "https://go.dev") || !strings.Contains(rendered, "via duckduckgo") {
		t.Fatalf("unexpected rendering: %q", rendered)
	}
	if rendered := RenderSearchResults("duckduckgo", "golang", nil); !strings.Contains(rendered, "No results") {
		t.Fatalf("expected empty-state rendering, got %q", rendered)
	}
}

func TestWebSearchFallsBackAcrossProviders(t *testing.T) {
	failing := stubProvider{name: "first", err: fmt.Errorf("blocked")}
	empty := stubProvider{name: "second"}
	working := stubProvider{name: "third", results: []SearchResult{{Title: "Hit", URL: "https://example.test"}}}
	tool := &WebSearchTool{providers: []Provider{failing, empty, working}}
	result := tool.Execute(context.Background(), map[string]any{"query": "q"})
	if result.IsError || !strings.Contains(result.ForLLM, "Hit") {
		t.Fatalf("expected fallback to working provider, got %+v", result)
	}
	if !strings.Contains(result.ForLLM, "via third") {
		t.Fatalf("expected winning provider in result, got %q", result.ForLLM)
	}
}

type stubProvider struct {
	name    string
	results []SearchResult
	err     error
}

func (s stubProvider) Name() string { return s.name }
func (s stubProvider) Search(context.Context, string, int) ([]SearchResult, error) {
	return s.results, s.err
}
