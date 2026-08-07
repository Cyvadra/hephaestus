package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// SearchResult is one structured web-search hit. Providers return these;
// the tool renders them uniformly for the LLM.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Provider is a single web-search backend. Providers are thin adapters over
// an HTTP API or a page scrape and return structured results; formatting is
// the tool's job, not the provider's.
type Provider interface {
	// Name identifies the provider in results and error messages.
	Name() string
	// Search returns up to count structured results for query.
	Search(ctx context.Context, query string, count int) ([]SearchResult, error)
}

// RenderSearchResults formats structured hits the same way regardless of
// which provider produced them.
func RenderSearchResults(providerName, query string, results []SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s (via %s)", query, providerName)
	}
	lines := []string{fmt.Sprintf("Results for: %s (via %s)", query, providerName)}
	for i, r := range results {
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, r.Title, r.URL))
		if r.Snippet != "" {
			lines = append(lines, "   "+r.Snippet)
		}
	}
	return strings.Join(lines, "\n")
}

// WebSearchConfig selects which providers are available and, optionally,
// pins one provider. With Provider == "auto" (or empty) the tool uses every
// configured provider in priority order, falling back on error or empty
// results.
type WebSearchConfig struct {
	Provider, SearXNGBaseURL                 string
	SerpAPIEngine                            string
	BraveAPIKeys, TavilyAPIKeys, SerpAPIKeys []string
	SogouEnabled                             bool
}

// WebSearchTool runs web searches against an ordered list of providers,
// preferring the first provider that returns results.
type WebSearchTool struct {
	providers []Provider
}

// NewWebSearchTool builds a web search tool from config, validating the
// requested provider at construction time rather than failing at runtime.
// duckduckgo needs no configuration, so an auto config always resolves to
// at least one provider.
func NewWebSearchTool(config WebSearchConfig) (*WebSearchTool, error) {
	_, providers, err := resolveProviders(config)
	if err != nil {
		return nil, err
	}
	return &WebSearchTool{providers: providers}, nil
}

// resolveProviders turns config into an ordered provider list, rejecting an
// explicit provider that is not configured at startup.
func resolveProviders(config WebSearchConfig) (string, []Provider, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	available := map[string]Provider{}
	if len(config.BraveAPIKeys) > 0 {
		available["brave"] = braveSearch{client: client, keys: NewAPIKeyPool(config.BraveAPIKeys)}
	}
	if len(config.TavilyAPIKeys) > 0 {
		available["tavily"] = tavilySearch{client: client, keys: NewAPIKeyPool(config.TavilyAPIKeys)}
	}
	if len(config.SerpAPIKeys) > 0 {
		available["serpapi"] = serpapiSearch{keys: NewAPIKeyPool(config.SerpAPIKeys), engine: config.SerpAPIEngine}
	}
	if base := strings.TrimRight(strings.TrimSpace(config.SearXNGBaseURL), "/"); base != "" {
		available["searxng"] = searxngSearch{client: client, baseURL: base}
	}
	if config.SogouEnabled {
		available["sogou"] = sogouSearch{client: client, endpoint: "https://wap.sogou.com/web/searchList.jsp"}
	}
	// duckduckgo needs no configuration and is the always-present fallback.
	available["duckduckgo"] = duckDuckGoSearch{client: client}

	requested := strings.ToLower(strings.TrimSpace(config.Provider))
	if requested != "" && requested != "auto" {
		if _, ok := available[requested]; !ok {
			return "", nil, fmt.Errorf("web_search: provider %q requested but not configured (set its keys, or use auto)", requested)
		}
		return requested, []Provider{available[requested]}, nil
	}

	order := []string{"brave", "tavily", "serpapi", "searxng", "sogou", "duckduckgo"}
	providers := make([]Provider, 0, len(order))
	configured := make([]string, 0, len(order))
	for _, name := range order {
		if p, ok := available[name]; ok {
			providers = append(providers, p)
			configured = append(configured, name)
		}
	}
	return strings.Join(configured, ","), providers, nil
}

func (WebSearchTool) Name() string { return "web_search" }
func (WebSearchTool) Description() string {
	return "Searches the web for current information. Supports query and count."
}
func (WebSearchTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query": map[string]any{"type": "string"},
		"count": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
	}, "required": []string{"query"}}
}
func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return toolkit.ErrorResult("web_search: query is required")
	}
	count := 10
	if value, ok := args["count"].(float64); ok {
		count = int(value)
		if count < 1 {
			count = 1
		}
		if count > 10 {
			count = 10
		}
	}
	provider, results, err := t.search(ctx, query, count)
	if err != nil {
		return toolkit.ErrorResult("web_search: " + err.Error())
	}
	return toolkit.SilentResult(RenderSearchResults(provider, query, results))
}

// search tries providers in order, falling back to the next one when a
// provider errors or returns no results.
func (t *WebSearchTool) search(ctx context.Context, query string, count int) (string, []SearchResult, error) {
	var failures []string
	for _, p := range t.providers {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		results, err := p.Search(ctx, query, count)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Name(), err))
			continue
		}
		if len(results) == 0 {
			failures = append(failures, p.Name()+": no results")
			continue
		}
		return p.Name(), results, nil
	}
	if len(failures) == 0 {
		return "", nil, fmt.Errorf("no web search provider configured")
	}
	return "", nil, fmt.Errorf("all providers failed: %s", strings.Join(failures, "; "))
}
