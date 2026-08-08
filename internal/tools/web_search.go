package tools

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"golang.org/x/net/publicsuffix"
)

const (
	providerResultLimit = 10
	finalResultLimit    = 7
)

var (
	blockedSearchDomains = []string{"taobao", "alibaba", "aliyun", "zhihu.com", "csdn.net", "weibo.com"}
	hanText              = regexp.MustCompile(`\p{Han}`)
	letterText           = regexp.MustCompile(`\pL`)
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
func RenderSearchResults(providerNames []string, query string, results []SearchResult) string {
	providers := strings.Join(providerNames, ", ")
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s (via %s)", query, providers)
	}
	lines := []string{fmt.Sprintf("Results for: %s (via %s)", query, providers)}
	for i, r := range results {
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, r.Title, r.URL))
		if r.Snippet != "" {
			lines = append(lines, "   "+r.Snippet)
		}
	}
	return strings.Join(lines, "\n")
}

// WebSearchConfig configures optional providers. DuckDuckGo and Sogou are
// always enabled and need no configuration.
type WebSearchConfig struct {
	SearXNGBaseURL                           string
	SerpAPIEngine                            string
	BraveAPIKeys, TavilyAPIKeys, SerpAPIKeys []string
}

type searchRandom interface {
	Float64() float64
	IntN(int) int
	Shuffle(int, func(int, int))
}

type lockedRandom struct {
	mu     sync.Mutex
	random *rand.Rand
}

func newLockedRandom() *lockedRandom {
	seed := uint64(time.Now().UnixNano())
	return &lockedRandom{random: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func (r *lockedRandom) Float64() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.random.Float64()
}

func (r *lockedRandom) IntN(limit int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.random.IntN(limit)
}

func (r *lockedRandom) Shuffle(count int, swap func(int, int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.random.Shuffle(count, swap)
}

// WebSearchTool selects provider groups for each request, searches the
// selected providers concurrently, then filters and samples their results.
type WebSearchTool struct {
	fixed      []Provider
	primary    []Provider
	occasional []Provider
	random     searchRandom
}

// NewWebSearchTool builds the provider groups used for per-request selection.
// DuckDuckGo and Sogou are always present; API-backed providers are optional.
func NewWebSearchTool(config WebSearchConfig) *WebSearchTool {
	client := &http.Client{Timeout: 15 * time.Second}
	tool := &WebSearchTool{
		fixed: []Provider{
			duckDuckGoSearch{client: client},
			sogouSearch{client: client, endpoint: "https://wap.sogou.com/web/searchList.jsp"},
		},
		random: newLockedRandom(),
	}
	if len(config.BraveAPIKeys) > 0 {
		tool.primary = append(tool.primary, braveSearch{client: client, keys: NewAPIKeyPool(config.BraveAPIKeys)})
	}
	if len(config.TavilyAPIKeys) > 0 {
		tool.primary = append(tool.primary, tavilySearch{client: client, keys: NewAPIKeyPool(config.TavilyAPIKeys)})
	}
	if len(config.SerpAPIKeys) > 0 {
		tool.occasional = append(tool.occasional, serpapiSearch{keys: NewAPIKeyPool(config.SerpAPIKeys), engine: config.SerpAPIEngine})
	}
	if base := strings.TrimRight(strings.TrimSpace(config.SearXNGBaseURL), "/"); base != "" {
		tool.occasional = append(tool.occasional, searxngSearch{client: client, baseURL: base})
	}
	return tool
}

func (WebSearchTool) Name() string { return "web_search" }
func (WebSearchTool) Description() string {
	return "Searches multiple web providers concurrently and returns up to seven diverse results."
}
func (WebSearchTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query": map[string]any{"type": "string"},
	}, "required": []string{"query"}}
}
func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return toolkit.ErrorResult("web_search: query is required")
	}
	providers, results, err := t.search(ctx, query)
	if err != nil {
		return toolkit.ErrorResult("web_search: " + err.Error())
	}
	return toolkit.SilentResult(RenderSearchResults(providers, query, results))
}

type providerResponse struct {
	name    string
	results []SearchResult
	err     error
}

// selectedProviders applies the request-level selection policy: all fixed
// providers, one configured primary, and occasionally one configured extra.
func (t *WebSearchTool) selectedProviders() []Provider {
	providers := append([]Provider(nil), t.fixed...)
	if len(t.primary) > 0 {
		providers = append(providers, t.primary[t.random.IntN(len(t.primary))])
	}
	if len(t.occasional) > 0 && t.random.Float64() < 0.2 {
		providers = append(providers, t.occasional[t.random.IntN(len(t.occasional))])
	}
	return providers
}

// search queries the selected providers concurrently. Individual provider
// failures are tolerated as long as at least one provider yields usable hits.
func (t *WebSearchTool) search(ctx context.Context, query string) ([]string, []SearchResult, error) {
	providers := t.selectedProviders()
	responses := make([]providerResponse, len(providers))
	var wait sync.WaitGroup
	for index, provider := range providers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results, err := provider.Search(ctx, query, providerResultLimit)
			responses[index] = providerResponse{name: provider.Name(), results: results, err: err}
		}()
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	var failures []string
	var successfulProviders []string
	var aggregated []SearchResult
	for _, response := range responses {
		if response.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", response.name, response.err))
			continue
		}
		if len(response.results) == 0 {
			failures = append(failures, response.name+": no results")
			continue
		}
		successfulProviders = append(successfulProviders, response.name)
		aggregated = append(aggregated, response.results[:min(len(response.results), providerResultLimit)]...)
	}
	if len(aggregated) == 0 {
		return nil, nil, fmt.Errorf("all providers failed: %s", strings.Join(failures, "; "))
	}

	filtered := filterSearchResults(aggregated, t.random)
	if len(filtered) == 0 {
		return nil, nil, fmt.Errorf("all search results were filtered")
	}
	return successfulProviders, selectSearchResults(filtered, t.random), nil
}

// filterSearchResults randomizes provider precedence, removes blocked or
// duplicate URLs, and limits each registrable root domain to two results.
func filterSearchResults(results []SearchResult, random searchRandom) []SearchResult {
	shuffled := append([]SearchResult(nil), results...)
	random.Shuffle(len(shuffled), func(first, second int) { shuffled[first], shuffled[second] = shuffled[second], shuffled[first] })
	seenURLs := make(map[string]bool)
	domainCounts := make(map[string]int)
	filtered := make([]SearchResult, 0, len(shuffled))
	for _, result := range shuffled {
		canonicalURL, hostname, ok := canonicalSearchURL(result.URL)
		if !ok || blockedSearchDomain(hostname) || seenURLs[canonicalURL] {
			continue
		}
		rootDomain := hostname
		if registrable, err := publicsuffix.EffectiveTLDPlusOne(hostname); err == nil {
			rootDomain = registrable
		}
		if domainCounts[rootDomain] >= 2 {
			continue
		}
		seenURLs[canonicalURL] = true
		domainCounts[rootDomain]++
		filtered = append(filtered, result)
	}
	return filtered
}

// canonicalSearchURL removes query parameters and fragments for deduplication
// while preserving path identity and returning a normalized hostname.
func canonicalSearchURL(rawURL string) (string, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return "", "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	return parsed.String(), hostname, true
}

func blockedSearchDomain(hostname string) bool {
	for _, blocked := range blockedSearchDomains {
		if strings.Contains(hostname, blocked) {
			return true
		}
	}
	return false
}

type searchLanguage int

const (
	searchEnglish searchLanguage = iota
	searchOther
	searchChinese
)

// inferSearchLanguage uses title and snippet scripts as a lightweight signal:
// Han text is Chinese, non-ASCII letters are other languages, and the rest English.
func inferSearchLanguage(result SearchResult) searchLanguage {
	text := result.Title + " " + result.Snippet
	if hanText.MatchString(text) {
		return searchChinese
	}
	for _, letter := range letterText.FindAllString(text, -1) {
		if []rune(letter)[0] > 127 {
			return searchOther
		}
	}
	return searchEnglish
}

// selectSearchResults shuffles each language group, applies the 3:2:2 quota,
// then fills shortages from all remaining results up to the final limit.
func selectSearchResults(results []SearchResult, random searchRandom) []SearchResult {
	groups := map[searchLanguage][]SearchResult{}
	for _, result := range results {
		language := inferSearchLanguage(result)
		groups[language] = append(groups[language], result)
	}
	for language := range groups {
		group := groups[language]
		random.Shuffle(len(group), func(first, second int) { group[first], group[second] = group[second], group[first] })
	}

	quotas := []struct {
		language searchLanguage
		count    int
	}{{searchEnglish, 3}, {searchOther, 2}, {searchChinese, 2}}
	selected := make([]SearchResult, 0, min(finalResultLimit, len(results)))
	var remaining []SearchResult
	for _, quota := range quotas {
		group := groups[quota.language]
		take := min(quota.count, len(group))
		selected = append(selected, group[:take]...)
		remaining = append(remaining, group[take:]...)
	}
	random.Shuffle(len(remaining), func(first, second int) { remaining[first], remaining[second] = remaining[second], remaining[first] })
	selected = append(selected, remaining[:min(finalResultLimit-len(selected), len(remaining))]...)

	return selected
}
