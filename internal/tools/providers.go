package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	serpapi "github.com/serpapi/serpapi-golang"
)

// APIKeyPool round-robins a set of provider API keys so a single bad key
// doesn't fail every request.
type APIKeyPool struct {
	keys    []string
	current atomic.Uint32
}

func NewAPIKeyPool(keys []string) *APIKeyPool { return &APIKeyPool{keys: keys} }

func (p *APIKeyPool) Next() (string, bool) {
	if len(p.keys) == 0 {
		return "", false
	}
	index := p.current.Add(1) - 1
	return p.keys[index%uint32(len(p.keys))], true
}

func (p *APIKeyPool) Len() int { return len(p.keys) }

// API-backed providers: brave, tavily, searxng. They share decodeSearch to
// turn a JSON response into structured results.

type braveSearch struct {
	client *http.Client
	keys   *APIKeyPool
}

func (braveSearch) Name() string { return "brave" }

func (p braveSearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	if p.keys.Len() == 0 {
		return nil, fmt.Errorf("Brave API key is not configured")
	}
	var lastErr error
	for range p.keys.Len() {
		key, _ := p.keys.Next()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.search.brave.com/res/v1/web/search?q="+url.QueryEscape(query)+fmt.Sprintf("&count=%d", count), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Subscription-Token", key)
		results, err := decodeSearch(p.client, req, "web.results", count)
		if err == nil {
			return results, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type tavilySearch struct {
	client *http.Client
	keys   *APIKeyPool
}

func (tavilySearch) Name() string { return "tavily" }

func (p tavilySearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	if p.keys.Len() == 0 {
		return nil, fmt.Errorf("Tavily API key is not configured")
	}
	var lastErr error
	for range p.keys.Len() {
		key, _ := p.keys.Next()
		payload, _ := json.Marshal(map[string]any{"api_key": key, "query": query, "max_results": count})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		results, err := decodeSearch(p.client, req, "results", count)
		if err == nil {
			return results, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type serpapiSearch struct {
	keys      *APIKeyPool
	engine    string
	transport http.RoundTripper
}

func (serpapiSearch) Name() string { return "serpapi" }

func (p serpapiSearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	if p.keys.Len() == 0 {
		return nil, fmt.Errorf("SerpApi API key is not configured")
	}
	engine := strings.ToLower(strings.TrimSpace(p.engine))
	if engine == "" {
		engine = "google_light"
	}
	transport := p.transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	var lastErr error
	for range p.keys.Len() {
		key, _ := p.keys.Next()
		setting := serpapi.NewSerpApiClientSetting(key)
		setting.Engine = engine
		setting.Timeout = 15 * time.Second
		client := serpapi.NewClient(setting)
		client.HttpSearch.Transport = contextRoundTripper{ctx: ctx, next: transport}

		response, err := client.Search(serpapiParameters(engine, query))
		if err == nil {
			return searchResults(response, "organic_results", count), nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func serpapiParameters(engine, query string) map[string]string {
	parameters := map[string]string{
		"engine":   engine,
		"q":        query,
		"location": "United Kingdom",
	}
	switch engine {
	case "google_light":
		parameters["gl"] = "uk"
		parameters["hl"] = "en"
	case "bing":
		parameters["cc"] = "gb"
		parameters["mkt"] = "en-GB"
	}
	return parameters
}

type contextRoundTripper struct {
	ctx  context.Context
	next http.RoundTripper
}

func (t contextRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.next.RoundTrip(request.Clone(t.ctx))
}

type searxngSearch struct {
	client  *http.Client
	baseURL string
}

func (searxngSearch) Name() string { return "searxng" }

func (p searxngSearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	if p.baseURL == "" {
		return nil, fmt.Errorf("SearXNG base URL is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/search?q="+url.QueryEscape(query)+"&format=json", nil)
	if err != nil {
		return nil, err
	}
	return decodeSearch(p.client, req, "results", count)
}

// decodeSearch fetches request and turns the JSON records at path into
// structured results, capped at count.
func decodeSearch(client *http.Client, request *http.Request, path string, count int) ([]SearchResult, error) {
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return searchResults(value, path, count), nil
}

func searchResults(value any, path string, count int) []SearchResult {
	items := lookupJSON(value, path)
	results := make([]SearchResult, 0, count)
	for _, item := range items {
		if len(results) >= count {
			break
		}
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		r := SearchResult{
			Title:   stringValue(record, "title", "Title", "Text"),
			URL:     stringValue(record, "url", "link", "URL", "FirstURL"),
			Snippet: stringValue(record, "description", "content", "snippet", "Snippet"),
		}
		if r.Title != "" || r.URL != "" {
			results = append(results, r)
		}
	}
	return results
}

func lookupJSON(value any, path string) []any {
	current := value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[part]
	}
	items, _ := current.([]any)
	return items
}

func stringValue(record map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := record[name].(string); ok {
			return value
		}
	}
	return ""
}
