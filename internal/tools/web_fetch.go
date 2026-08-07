package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyvadra/hephaestus/internal/htmltext"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

const defaultWebFetchChars = 50_000
const defaultWebFetchBytes = 10 * 1024 * 1024

var htmlScript = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
var htmlStyle = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
var htmlSpace = regexp.MustCompile(`[\t\r ]+`)

// WebFetchTool fetches a public HTTP(S) URL and extracts readable text.
type WebFetchTool struct {
	maxChars int
	maxBytes int64
	client   *http.Client
}

func NewWebFetchTool(maxChars int, maxBytes int64) *WebFetchTool {
	if maxChars <= 0 {
		maxChars = defaultWebFetchChars
	}
	if maxBytes <= 0 {
		maxBytes = defaultWebFetchBytes
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeDialContext((&net.Dialer{Timeout: 10 * time.Second}).DialContext)
	return &WebFetchTool{maxChars: maxChars, maxBytes: maxBytes, client: &http.Client{
		Timeout: 60 * time.Second, Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return validatePublicURL(req.URL)
		},
	}}
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
	if err := validatePublicURL(parsed); err != nil {
		return toolkit.ErrorResult("web_fetch: " + err.Error())
	}
	maxChars := t.maxChars
	if value, ok := args["max_chars"].(float64); ok && value >= 100 {
		maxChars = min(int(value), t.maxChars)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return toolkit.ErrorResult("web_fetch: " + err.Error())
	}
	req.Header.Set("User-Agent", "hephaestus/1.0")
	resp, err := t.client.Do(req)
	if err != nil {
		return toolkit.ErrorResult("web_fetch: request failed: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return toolkit.ErrorResult(fmt.Sprintf("web_fetch: server returned HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, t.maxBytes+1))
	if err != nil {
		return toolkit.ErrorResult("web_fetch: read response: " + err.Error())
	}
	if int64(len(body)) > t.maxBytes {
		return toolkit.ErrorResult(fmt.Sprintf("web_fetch: response exceeds %d-byte limit", t.maxBytes))
	}
	contentType := resp.Header.Get("Content-Type")
	if !isHTMLContent(contentType) && !utf8.Valid(body) {
		return toolkit.ErrorResult("web_fetch: response is not readable text content")
	}
	text := extractWebText(string(body), contentType)
	text = truncateWebText(text, maxChars)
	return toolkit.SilentResult(text)
}

func truncateWebText(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + "\n[TRUNCATED]"
}

func isHTMLContent(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "html")
}

func validatePublicURL(target *url.URL) error {
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
	return nil
}

func safeDialContext(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if isPrivateAddr(ip) {
				return nil, fmt.Errorf("private address blocked")
			}
		}
		var dialErr error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, dialErr
	}
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

func extractWebText(body, contentType string) string {
	if !isHTMLContent(contentType) {
		return body
	}
	text := htmlScript.ReplaceAllString(body, "")
	text = htmlStyle.ReplaceAllString(text, "")
	text = htmltext.CleanFragment(text)
	text = htmlSpace.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}
