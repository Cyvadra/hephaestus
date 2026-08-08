package tools

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

type localWebFetchProvider struct {
	chromePath string
}

func newLocalWebFetchProvider(chromePath string) *localWebFetchProvider {
	return &localWebFetchProvider{chromePath: chromePath}
}

func (*localWebFetchProvider) Name() string { return "local" }

func (p *localWebFetchProvider) Fetch(ctx context.Context, target *url.URL) (string, error) {
	proxy, err := newBrowserProxy(net.DefaultResolver)
	if err != nil {
		return "", fmt.Errorf("start browser proxy: %w", err)
	}
	defer proxy.Close()

	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options,
		chromedp.ProxyServer(proxy.URL()),
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/131 Safari/537.36"),
	)
	if strings.TrimSpace(p.chromePath) != "" {
		options = append(options, chromedp.ExecPath(p.chromePath))
	}
	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(ctx, options...)
	defer cancelAllocator()
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()
	requestContext, cancelRequest := context.WithTimeout(browserContext, 60*time.Second)
	defer cancelRequest()

	response, err := chromedp.RunResponse(requestContext, chromedp.Navigate(target.String()))
	if err != nil {
		return "", fmt.Errorf("navigate: %w", err)
	}
	if response == nil {
		return "", fmt.Errorf("navigation returned no response")
	}
	if response.Status < 200 || response.Status >= 400 {
		return "", fmt.Errorf("server returned HTTP %d", response.Status)
	}
	var finalURL, text string
	if err := chromedp.Run(requestContext,
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Poll(`document.readyState === "complete" && document.body.innerText.trim().length > 0`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Location(&finalURL),
		chromedp.Text("body", &text, chromedp.ByQuery),
	); err != nil {
		return "", fmt.Errorf("read page: %w", err)
	}
	parsedFinalURL, err := url.Parse(finalURL)
	if err != nil {
		return "", fmt.Errorf("invalid final URL: %w", err)
	}
	if err := validatePublicURL(requestContext, parsedFinalURL, net.DefaultResolver); err != nil {
		return "", fmt.Errorf("unsafe final URL: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("page has no readable text")
	}
	return text, nil
}
