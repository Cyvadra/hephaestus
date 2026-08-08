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
	"sync"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
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
	config := WebSearchConfig{}
	if keys := os.Getenv("HEPHAESTUS_WEB_SEARCH_BRAVE_API_KEYS"); strings.TrimSpace(keys) != "" {
		config.BraveAPIKeys = splitTestKeys(keys)
	}
	if keys := os.Getenv("HEPHAESTUS_WEB_SEARCH_TAVILY_API_KEYS"); strings.TrimSpace(keys) != "" {
		config.TavilyAPIKeys = splitTestKeys(keys)
	}
	if keys := splitTestKeys(os.Getenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS")); len(keys) > 0 {
		config.SerpAPIKeys = keys
		config.SerpAPIEngine = os.Getenv("HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE")
	}
	if baseURL := os.Getenv("HEPHAESTUS_WEB_SEARCH_SEARXNG_BASE_URL"); strings.TrimSpace(baseURL) != "" {
		config.SearXNGBaseURL = baseURL
	}
	tool := NewWebSearchTool(config)
	result := tool.Execute(context.Background(), map[string]any{"query": "open source artificial intelligence"})
	if result.IsError {
		t.Fatalf("provider request failed: %s", result.ForLLM)
	}
	if found := len(searchResultLine.FindAllString(result.ForLLM, -1)); found == 0 || found > finalResultLimit {
		t.Fatalf("expected 1-%d search results, found %d: %s", finalResultLimit, found, result.ForLLM)
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

func TestWebSearchProviderGroups(t *testing.T) {
	tool := NewWebSearchTool(WebSearchConfig{BraveAPIKeys: []string{"k1"}, TavilyAPIKeys: []string{"k2"}, SerpAPIKeys: []string{"k3"}, SearXNGBaseURL: "https://search.test"})
	if got, want := providerNames(tool.fixed), []string{"duckduckgo", "sogou"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fixed providers = %v, want %v", got, want)
	}
	if got, want := providerNames(tool.primary), []string{"brave", "tavily"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("primary providers = %v, want %v", got, want)
	}
	if got, want := providerNames(tool.occasional), []string{"serpapi", "searxng"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("occasional providers = %v, want %v", got, want)
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
	rendered := RenderSearchResults([]string{"duckduckgo", "sogou"}, "golang", []SearchResult{
		{Title: "Go", URL: "https://go.dev", Snippet: "The Go language"},
	})
	if !strings.Contains(rendered, "1. Go") || !strings.Contains(rendered, "https://go.dev") || !strings.Contains(rendered, "via duckduckgo, sogou") {
		t.Fatalf("unexpected rendering: %q", rendered)
	}
	if rendered := RenderSearchResults([]string{"duckduckgo"}, "golang", nil); !strings.Contains(rendered, "No results") {
		t.Fatalf("expected empty-state rendering, got %q", rendered)
	}
}

func TestWebSearchToleratesPartialProviderFailure(t *testing.T) {
	failing := &stubProvider{name: "first", err: fmt.Errorf("blocked")}
	working := &stubProvider{name: "second", results: []SearchResult{{Title: "Hit", URL: "https://example.test"}}}
	tool := &WebSearchTool{fixed: []Provider{failing, working}, random: &stubRandom{}}
	result := tool.Execute(context.Background(), map[string]any{"query": "q"})
	if result.IsError || !strings.Contains(result.ForLLM, "Hit") {
		t.Fatalf("expected successful aggregation, got %+v", result)
	}
	if !strings.Contains(result.ForLLM, "via second") {
		t.Fatalf("expected successful provider in result, got %q", result.ForLLM)
	}
}

type stubProvider struct {
	name    string
	results []SearchResult
	err     error
	started chan<- string
	release <-chan struct{}
	mu      sync.Mutex
	calls   int
	count   int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Search(ctx context.Context, _ string, count int) ([]SearchResult, error) {
	s.mu.Lock()
	s.calls++
	s.count = count
	s.mu.Unlock()
	if s.started != nil {
		s.started <- s.name
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.results, s.err
}

type stubRandom struct {
	float64s []float64
	ints     []int
}

func (r *stubRandom) Float64() float64 {
	if len(r.float64s) == 0 {
		return 1
	}
	value := r.float64s[0]
	r.float64s = r.float64s[1:]
	return value
}

func (r *stubRandom) IntN(limit int) int {
	if len(r.ints) == 0 {
		return 0
	}
	value := r.ints[0]
	r.ints = r.ints[1:]
	return value % limit
}

func (*stubRandom) Shuffle(int, func(int, int)) {}

func TestWebSearchSelectsProvidersByGroup(t *testing.T) {
	fixedOne := &stubProvider{name: "duckduckgo"}
	fixedTwo := &stubProvider{name: "sogou"}
	brave := &stubProvider{name: "brave"}
	tavily := &stubProvider{name: "tavily"}
	serpapi := &stubProvider{name: "serpapi"}
	searxng := &stubProvider{name: "searxng"}
	tool := &WebSearchTool{
		fixed:      []Provider{fixedOne, fixedTwo},
		primary:    []Provider{brave, tavily},
		occasional: []Provider{serpapi, searxng},
		random:     &stubRandom{ints: []int{1, 1}, float64s: []float64{0.19}},
	}
	if got, want := providerNames(tool.selectedProviders()), []string{"duckduckgo", "sogou", "tavily", "searxng"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("selected providers = %v, want %v", got, want)
	}
	tool.random = &stubRandom{float64s: []float64{0.2}}
	if got, want := providerNames(tool.selectedProviders()), []string{"duckduckgo", "sogou", "brave"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("selected providers at threshold = %v, want %v", got, want)
	}
}

func TestWebSearchRunsProvidersConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	first := &stubProvider{name: "first", results: []SearchResult{{Title: "One", URL: "https://one.test"}}, started: started, release: release}
	second := &stubProvider{name: "second", results: []SearchResult{{Title: "Two", URL: "https://two.test"}}, started: started, release: release}
	tool := &WebSearchTool{fixed: []Provider{first, second}, random: &stubRandom{}}
	done := make(chan *toolkit.ToolResult, 1)
	go func() { done <- tool.Execute(context.Background(), map[string]any{"query": "q"}) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("providers did not start concurrently")
		}
	}
	close(release)
	result := <-done
	if result.IsError {
		t.Fatalf("unexpected search error: %s", result.ForLLM)
	}
	for _, provider := range []*stubProvider{first, second} {
		provider.mu.Lock()
		if provider.calls != 1 || provider.count != providerResultLimit {
			t.Fatalf("%s calls=%d count=%d", provider.name, provider.calls, provider.count)
		}
		provider.mu.Unlock()
	}
}

func TestWebSearchParametersOnlyRequireQuery(t *testing.T) {
	parameters := WebSearchTool{}.Parameters()
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected parameters: %#v", parameters)
	}
	if _, ok := properties["query"]; !ok || len(properties) != 1 {
		t.Fatalf("expected query-only schema, got %#v", properties)
	}
}

func TestWebSearchCapsEachProviderAtTenResults(t *testing.T) {
	results := make([]SearchResult, 12)
	for index := range results {
		results[index] = SearchResult{Title: fmt.Sprintf("Result %d", index), URL: fmt.Sprintf("https://site%d.test/page", index)}
	}
	provider := &stubProvider{name: "many", results: results}
	tool := &WebSearchTool{fixed: []Provider{provider}, random: &stubRandom{}}
	_, filtered, err := tool.search(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != finalResultLimit {
		t.Fatalf("final results = %d, want %d", len(filtered), finalResultLimit)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.count != providerResultLimit {
		t.Fatalf("provider count = %d, want %d", provider.count, providerResultLimit)
	}
}

func TestCanonicalSearchURLRemovesParameters(t *testing.T) {
	canonical, hostname, ok := canonicalSearchURL("HTTPS://Docs.Example.COM/path?utm_source=test#section")
	if !ok || canonical != "https://docs.example.com/path" || hostname != "docs.example.com" {
		t.Fatalf("canonical URL = %q, hostname = %q, ok = %v", canonical, hostname, ok)
	}
	if _, _, ok := canonicalSearchURL("not a URL"); ok {
		t.Fatal("expected URL without scheme and hostname to be rejected")
	}
}

func TestFilterSearchResultsAppliesURLDomainAndBlacklistRules(t *testing.T) {
	results := []SearchResult{
		{Title: "First", URL: "https://a.example.co.uk/page?one=1"},
		{Title: "Duplicate", URL: "https://a.example.co.uk/page?two=2#fragment"},
		{Title: "Second root hit", URL: "https://b.example.co.uk/other"},
		{Title: "Third root hit", URL: "https://c.example.co.uk/third"},
		{Title: "Blocked", URL: "https://news.zhihu.com/question"},
		{Title: "Allowed", URL: "https://allowed.test/page"},
		{Title: "Invalid", URL: "relative/path"},
	}
	filtered := filterSearchResults(results, &stubRandom{})
	if len(filtered) != 3 {
		t.Fatalf("filtered results = %+v", filtered)
	}
	rootCount := 0
	for _, result := range filtered {
		if strings.Contains(result.URL, "example.co.uk") {
			rootCount++
		}
		if strings.Contains(result.URL, "zhihu.com") || result.Title == "Duplicate" {
			t.Fatalf("unexpected filtered result: %+v", result)
		}
	}
	if rootCount != 2 {
		t.Fatalf("example.co.uk results = %d, want 2", rootCount)
	}
}

func TestInferSearchLanguage(t *testing.T) {
	testCases := []struct {
		name   string
		result SearchResult
		want   searchLanguage
	}{
		{name: "english", result: SearchResult{Title: "Open source project"}, want: searchEnglish},
		{name: "other", result: SearchResult{Title: "Projet logiciel français"}, want: searchOther},
		{name: "chinese wins", result: SearchResult{Title: "开源 project"}, want: searchChinese},
		{name: "unknown defaults english", result: SearchResult{Title: "1234"}, want: searchEnglish},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := inferSearchLanguage(testCase.result); got != testCase.want {
				t.Fatalf("language = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestSelectSearchResultsUsesLanguageQuotasAndFillsShortages(t *testing.T) {
	var results []SearchResult
	for index := range 5 {
		results = append(results, SearchResult{Title: fmt.Sprintf("English %d", index), URL: fmt.Sprintf("https://en%d.test", index)})
	}
	for index := range 3 {
		results = append(results, SearchResult{Title: fmt.Sprintf("Français %d", index), URL: fmt.Sprintf("https://fr%d.test", index)})
	}
	for index := range 4 {
		results = append(results, SearchResult{Title: fmt.Sprintf("中文 %d", index), URL: fmt.Sprintf("https://zh%d.test", index)})
	}
	selected := selectSearchResults(results, &stubRandom{})
	if len(selected) != finalResultLimit {
		t.Fatalf("selected results = %d, want %d", len(selected), finalResultLimit)
	}
	counts := map[searchLanguage]int{}
	for _, result := range selected {
		counts[inferSearchLanguage(result)]++
	}
	if counts[searchEnglish] != 3 || counts[searchOther] != 2 || counts[searchChinese] != 2 {
		t.Fatalf("language counts = %#v", counts)
	}

	selected = selectSearchResults(results[:6], &stubRandom{})
	if len(selected) != 6 {
		t.Fatalf("short result set = %d, want all 6", len(selected))
	}
	if counts := countLanguages(selected); counts[searchEnglish] != 5 || counts[searchOther] != 1 {
		t.Fatalf("filled language counts = %#v", counts)
	}
}

func countLanguages(results []SearchResult) map[searchLanguage]int {
	counts := make(map[searchLanguage]int)
	for _, result := range results {
		counts[inferSearchLanguage(result)]++
	}
	return counts
}

func TestWebSearchReturnsErrorWhenAllProvidersFail(t *testing.T) {
	tool := &WebSearchTool{
		fixed: []Provider{
			&stubProvider{name: "failed", err: errors.New("blocked")},
			&stubProvider{name: "empty"},
		},
		random: &stubRandom{},
	}
	result := tool.Execute(context.Background(), map[string]any{"query": "q"})
	if !result.IsError || !strings.Contains(result.ForLLM, "failed: blocked") || !strings.Contains(result.ForLLM, "empty: no results") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// largeStubResults builds seven results with verbose snippets so the rendered
// list exceeds a small summary cap.
func largeStubResults() []SearchResult {
	results := make([]SearchResult, 0, 7)
	for i := 1; i <= 7; i++ {
		results = append(results, SearchResult{
			Title:   fmt.Sprintf("Result %d", i),
			URL:     fmt.Sprintf("https://example%d.test/page", i),
			Snippet: strings.Repeat(fmt.Sprintf("snippet %d ", i), 40),
		})
	}
	return results
}

func TestWebSearchSummarizesLargeResultList(t *testing.T) {
	provider := &stubProvider{name: "stub", results: largeStubResults()}
	var gotTarget int
	summarizer := func(_ context.Context, _ string, maxOutputLen int) (string, error) {
		gotTarget = maxOutputLen
		return "condensed results", nil
	}
	tool := &WebSearchTool{fixed: []Provider{provider}, random: &stubRandom{}, summaryMaxChars: 100, summarizer: summarizer}
	result := tool.Execute(context.Background(), map[string]any{"query": "q"})
	if result.IsError || result.ForLLM != "condensed results" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotTarget != 100 {
		t.Fatalf("summarizer target = %d, want 100", gotTarget)
	}
}

func TestWebSearchSkipsSummarizeForShortResultList(t *testing.T) {
	provider := &stubProvider{name: "stub", results: []SearchResult{{Title: "Hit", URL: "https://example.test"}}}
	called := false
	summarizer := func(_ context.Context, _ string, _ int) (string, error) {
		called = true
		return "", nil
	}
	tool := &WebSearchTool{fixed: []Provider{provider}, random: &stubRandom{}, summaryMaxChars: 5000, summarizer: summarizer}
	result := tool.Execute(context.Background(), map[string]any{"query": "q"})
	if result.IsError || !strings.Contains(result.ForLLM, "Hit") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if called {
		t.Fatal("summarizer should not be called for short result list")
	}
}

func TestWebSearchDegradesToRawListOnSummarizeError(t *testing.T) {
	provider := &stubProvider{name: "stub", results: largeStubResults()}
	summarizer := func(_ context.Context, _ string, _ int) (string, error) {
		return "", errors.New("summarizer down")
	}
	tool := &WebSearchTool{fixed: []Provider{provider}, random: &stubRandom{}, summaryMaxChars: 100, summarizer: summarizer}
	result := tool.Execute(context.Background(), map[string]any{"query": "q"})
	if result.IsError || !strings.Contains(result.ForLLM, "Result 1") {
		t.Fatalf("unexpected result: %+v", result)
	}
}
