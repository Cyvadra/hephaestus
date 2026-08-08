package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultFirecrawlBaseURL = "https://api.firecrawl.dev/v2"
	maxFirecrawlResponse    = 10 * 1024 * 1024
)

type firecrawlWebFetchProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func newFirecrawlWebFetchProvider(apiKey, baseURL string) *firecrawlWebFetchProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultFirecrawlBaseURL
	}
	return &firecrawlWebFetchProvider{apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 65 * time.Second}}
}

func (*firecrawlWebFetchProvider) Name() string { return "firecrawl" }

func (p *firecrawlWebFetchProvider) Fetch(ctx context.Context, target *url.URL) (string, error) {
	payload, err := json.Marshal(map[string]any{"url": target.String(), "formats": []string{"markdown"}, "onlyMainContent": true, "timeout": 60_000})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/scrape", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hephaestus/1.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFirecrawlResponse+1))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxFirecrawlResponse {
		return "", fmt.Errorf("response exceeds %d-byte limit", maxFirecrawlResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			Markdown string `json:"markdown"`
			Metadata struct {
				URL string `json:"url"`
			} `json:"metadata"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("invalid JSON response: %w", err)
	}
	if !result.Success {
		if result.Error != "" {
			return "", fmt.Errorf("scrape failed: %s", result.Error)
		}
		return "", fmt.Errorf("scrape failed")
	}
	if strings.TrimSpace(result.Data.Markdown) == "" {
		return "", fmt.Errorf("empty markdown response")
	}
	if result.Data.Metadata.URL != "" {
		finalURL, err := url.Parse(result.Data.Metadata.URL)
		if err != nil {
			return "", fmt.Errorf("invalid final URL: %w", err)
		}
		if err := validatePublicURL(ctx, finalURL, net.DefaultResolver); err != nil {
			return "", fmt.Errorf("unsafe final URL: %w", err)
		}
	}
	return result.Data.Markdown, nil
}
