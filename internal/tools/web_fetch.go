package tools

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
)

const (
	// defaultWebFetchChars caps the raw captured page text fed to either a
	// summarizer or the calling agent.
	defaultWebFetchChars = 16_000
	// defaultWebFetchSummaryChars caps the LLM-condensed digest returned to
	// the calling agent when summarization is enabled.
	defaultWebFetchSummaryChars = 4_000
)

type webFetchProvider interface {
	Name() string
	Fetch(context.Context, *url.URL) (string, error)
}

type summarizeFunc func(ctx context.Context, text string, maxOutputLen int) (string, error)

type WebFetchConfig struct {
	Provider         string
	FirecrawlAPIKey  string
	ChromePath       string
	MaxChars         int
	FirecrawlBaseURL string
	// DeepSeekAPIKey and DeepSeekBaseURL configure the "deepseek" provider.
	DeepSeekAPIKey  string
	DeepSeekBaseURL string
	// LLMClient, when set, enables LLM summarization of large fetched pages:
	// content over SummaryMaxChars is condensed before it is returned to the
	// calling agent. When nil, large pages are returned truncated only.
	LLMClient       *llm.Client
	SummaryMaxChars int
}

// WebFetchTool fetches readable content from a public HTTP(S) URL.
type WebFetchTool struct {
	maxChars        int
	summaryMaxChars int
	primary         webFetchProvider
	// fallbacks are tried in order when the primary provider fails.
	fallbacks  []webFetchProvider
	summarizer summarizeFunc
}

func NewWebFetchTool(config WebFetchConfig) (*WebFetchTool, error) {
	maxChars := config.MaxChars
	if maxChars <= 0 {
		maxChars = defaultWebFetchChars
	}
	summaryMaxChars := config.SummaryMaxChars
	if summaryMaxChars <= 0 {
		summaryMaxChars = defaultWebFetchSummaryChars
	}
	var summarizer summarizeFunc
	if config.LLMClient != nil {
		client := config.LLMClient
		summarizer = func(ctx context.Context, text string, maxOutputLen int) (string, error) {
			return transform.Summarize(ctx, client, text, maxOutputLen)
		}
	}
	local := newLocalWebFetchProvider(config.ChromePath)
	// DeepSeek is the last resort whenever its key is configured, so a host
	// without Chrome can still fetch pages.
	var deepseek webFetchProvider
	if strings.TrimSpace(config.DeepSeekAPIKey) != "" {
		deepseek = newDeepSeekWebFetchProvider(config.DeepSeekAPIKey, config.DeepSeekBaseURL)
	}
	tool := &WebFetchTool{maxChars: maxChars, summaryMaxChars: summaryMaxChars, summarizer: summarizer}
	switch strings.ToLower(strings.TrimSpace(config.Provider)) {
	case "local":
		tool.primary = local
		tool.fallbacks = compactProviders(deepseek)
	case "", "firecrawl":
		if strings.TrimSpace(config.FirecrawlAPIKey) == "" {
			return nil, fmt.Errorf("Firecrawl API key is required")
		}
		tool.primary = newFirecrawlWebFetchProvider(config.FirecrawlAPIKey, config.FirecrawlBaseURL)
		tool.fallbacks = compactProviders(local, deepseek)
	case "deepseek":
		if deepseek == nil {
			return nil, fmt.Errorf("DeepSeek API key is required")
		}
		tool.primary = deepseek
		tool.fallbacks = compactProviders(local)
	default:
		return nil, fmt.Errorf("unsupported web fetch provider %q", config.Provider)
	}
	return tool, nil
}

func compactProviders(providers ...webFetchProvider) []webFetchProvider {
	var out []webFetchProvider
	for _, provider := range providers {
		if provider != nil {
			out = append(out, provider)
		}
	}
	return out
}

func newWebFetchToolForTest(maxChars, summaryMaxChars int, primary, fallback webFetchProvider, summarizer summarizeFunc) *WebFetchTool {
	if maxChars <= 0 {
		maxChars = defaultWebFetchChars
	}
	if summaryMaxChars <= 0 {
		summaryMaxChars = defaultWebFetchSummaryChars
	}
	tool := &WebFetchTool{maxChars: maxChars, summaryMaxChars: summaryMaxChars, primary: primary, summarizer: summarizer}
	if fallback != nil {
		tool.fallbacks = []webFetchProvider{fallback}
	}
	return tool
}

func (WebFetchTool) Name() string { return "web_fetch" }
func (WebFetchTool) Description() string {
	return "Fetches a public HTTP(S) URL and extracts readable text content."
}
func (WebFetchTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"url":       map[string]any{"type": "string"},
		"max_chars": map[string]any{"type": "integer", "minimum": 100},
	}, "required": []string{"url"}}
}
func (t *WebFetchTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	rawURL, _ := args["url"].(string)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return toolkit.ErrorResult("web_fetch: invalid URL: " + err.Error())
	}
	if err := validatePublicURL(ctx, parsed, net.DefaultResolver); err != nil {
		return toolkit.ErrorResult("web_fetch: " + err.Error())
	}
	text, err := t.fetch(ctx, t.primary, parsed)
	if err != nil {
		failures := []string{fmt.Sprintf("%s failed: %v", t.primary.Name(), err)}
		for _, fallback := range t.fallbacks {
			if text, err = t.fetch(ctx, fallback, parsed); err == nil {
				break
			}
			failures = append(failures, fmt.Sprintf("%s fallback failed: %v", fallback.Name(), err))
		}
		if err != nil {
			return toolkit.ErrorResult("web_fetch: " + strings.Join(failures, "; "))
		}
	}
	if strings.TrimSpace(text) == "" {
		return toolkit.ErrorResult("web_fetch: provider returned empty content")
	}
	maxChars := t.maxChars
	if value, ok := args["max_chars"].(float64); ok && value >= 100 {
		maxChars = min(int(value), t.maxChars)
	}
	text = truncateWebText(text, maxChars)
	// Condense large content with the LLM when enabled. Summarization is
	// best-effort: on any failure we degrade to the truncated raw text so
	// the calling agent still receives usable content.
	if t.summarizer != nil && len([]rune(text)) > t.summaryMaxChars {
		if summary, summaryErr := t.summarizer(ctx, text, t.summaryMaxChars); summaryErr == nil {
			return toolkit.SilentResult(summary)
		}
	}
	return toolkit.SilentResult(text)
}

func (t *WebFetchTool) fetch(ctx context.Context, provider webFetchProvider, target *url.URL) (string, error) {
	if err := sharedWebProviderQueues.wait(ctx, provider.Name()); err != nil {
		return "", err
	}
	return provider.Fetch(ctx, target)
}

func truncateWebText(text string, maxChars int) string {
	head, dropped := transform.TruncateRunes(text, maxChars)
	if dropped == 0 {
		return text
	}
	return head + "\n[TRUNCATED]"
}

type netIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func validatePublicURL(ctx context.Context, target *url.URL, resolver netIPResolver) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("only http/https URLs are allowed")
	}
	host := target.Hostname()
	if host == "" {
		return fmt.Errorf("missing domain in URL")
	}
	if isPrivateHost(host) {
		return fmt.Errorf("fetching private or local network hosts is not allowed")
	}
	if _, err := resolvePublicIPs(ctx, resolver, host); err != nil {
		return err
	}
	return nil
}

func resolvePublicIPs(ctx context.Context, resolver netIPResolver, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if isPrivateAddr(ip) {
			return nil, fmt.Errorf("fetching private or local network hosts is not allowed")
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve domain: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("domain has no IP addresses")
	}
	for _, ip := range ips {
		if isPrivateAddr(ip) {
			return nil, fmt.Errorf("fetching private or local network hosts is not allowed")
		}
	}
	return ips, nil
}

func isPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return isPrivateAddr(ip)
	}
	return false
}

func isPrivateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is4() {
		// RFC 6598 shared address space (CGNAT) is not covered by IsPrivate.
		if v4 := ip.As4(); v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}
