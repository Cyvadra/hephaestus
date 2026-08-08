package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/htmltext"
)

// Scraping providers (duckduckgo, sogou) parse search-engine HTML. They need
// no API keys and are always queried, but remain coupled to page markup and
// can be rate-limited or blocked.

var (
	sogouTitle   = regexp.MustCompile(`<a\s+class="?resultLink"?\s+href="([^"]+)"[^>]*id="sogou_vr_\d+_\d+"[^>]*>\s*(.*?)\s*</a>`)
	sogouSnippet = regexp.MustCompile(`<div class="clamp\d*[^"]*">\s*(.*?)\s*</div>`)
	sogouURL     = regexp.MustCompile(`url=([^&]+)`)
	ddgLink      = regexp.MustCompile(`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>([\s\S]*?)</a>`)
	ddgSnippet   = regexp.MustCompile(`<a class="result__snippet[^"]*".*?>([\s\S]*?)</a>`)
)

type duckDuckGoSearch struct{ client *http.Client }

func (duckDuckGoSearch) Name() string { return "duckduckgo" }

func (p duckDuckGoSearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	response, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return extractDuckDuckGoResults(string(body), count), nil
}

// extractDuckDuckGoResults parses the HTML result page. DuckDuckGo wraps
// outbound links in /l/?uddg=<url>&rut=<token> redirects; the rut token
// must be stripped so the model gets the real destination URL. The cut is
// done on the still-encoded string because the target's own '&' characters
// are %26-encoded, so the first literal '&' is always the rut separator.
func extractDuckDuckGoResults(document string, count int) []SearchResult {
	matches := ddgLink.FindAllStringSubmatch(document, count+5)
	if len(matches) == 0 {
		return nil
	}
	snippets := ddgSnippet.FindAllStringSubmatch(document, count+5)
	results := make([]SearchResult, 0, min(len(matches), count))
	for index, match := range matches[:min(len(matches), count)] {
		link := html.UnescapeString(match[1])
		if strings.Contains(link, "uddg=") {
			if _, encoded, ok := strings.Cut(link, "uddg="); ok {
				if cut := strings.IndexByte(encoded, '&'); cut >= 0 {
					encoded = encoded[:cut]
				}
				if decoded, err := url.QueryUnescape(encoded); err == nil {
					link = decoded
				}
			}
		}
		r := SearchResult{Title: cleanSearchText(match[2]), URL: link}
		if index < len(snippets) {
			r.Snippet = cleanSearchText(snippets[index][1])
		}
		results = append(results, r)
	}
	return results
}

type sogouSearch struct {
	client   *http.Client
	endpoint string
}

func (sogouSearch) Name() string { return "sogou" }

func (p sogouSearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	results := make([]SearchResult, 0, count)
	seenURLs := make(map[string]bool)
	maxPages := min(3, (count+1)/2+1)
	for page := 1; page <= maxPages && len(results) < count; page++ {
		params := url.Values{"keyword": {query}, "v": {"5"}, "p": {fmt.Sprintf("%d", page)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("Sogou returned status %d", resp.StatusCode)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		document := string(body)
		if len(document) < 200 {
			break
		}
		for _, match := range sogouTitle.FindAllStringSubmatch(document, -1) {
			if len(match) < 3 {
				continue
			}
			r := SearchResult{Title: cleanSearchText(match[2]), URL: extractSogouURL(match[1])}
			if r.Title == "" || r.URL == "" || seenURLs[r.URL] {
				continue
			}
			seenURLs[r.URL] = true
			if start := strings.Index(document, match[0]); start >= 0 {
				after := document[start+len(match[0]):]
				if len(after) > 2000 {
					after = after[:2000]
				}
				if snippetMatch := sogouSnippet.FindStringSubmatch(after); len(snippetMatch) > 1 {
					r.Snippet = cleanSearchText(snippetMatch[1])
				}
			}
			results = append(results, r)
			if len(results) >= count {
				break
			}
		}
	}
	return results, nil
}

// cleanSearchText strips tags and entities from an HTML fragment. Shared by
// every scraping provider so all extracted text is normalized the same way.
func cleanSearchText(value string) string {
	return htmltext.CleanFragment(value)
}

func extractSogouURL(href string) string {
	match := sogouURL.FindStringSubmatch(href)
	if len(match) < 2 {
		return ""
	}
	decoded, err := url.QueryUnescape(match[1])
	if err != nil {
		return ""
	}
	return decoded
}
