package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeepSeek tool APIs: https://api-docs.deepseek.com/zh-cn/guides/search
const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	// deepseekMaxQueryRunes is the API's upper bound for the query field.
	deepseekMaxQueryRunes = 128
	maxDeepSeekResponse   = 4 * 1024 * 1024
)

// deepseekSearch calls DeepSeek's hosted search API. It replaces the scraped
// DuckDuckGo/Sogou pair as the always-on provider when a key is configured.
type deepseekSearch struct {
	client  *http.Client
	apiKey  string
	baseURL string
}

func (deepseekSearch) Name() string { return "deepseek" }

func (p deepseekSearch) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, fmt.Errorf("DeepSeek API key is not configured")
	}
	if runes := []rune(query); len(runes) > deepseekMaxQueryRunes {
		query = string(runes[:deepseekMaxQueryRunes])
	}
	var response struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	payload := map[string]any{"query": query, "count": min(max(count, 1), 50)}
	if err := deepseekPost(ctx, p.client, p.baseURL, p.apiKey, "/v1/tools/search", payload, &response); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(response.Results))
	for _, r := range response.Results {
		if r.Title != "" || r.URL != "" {
			results = append(results, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
		}
	}
	return results, nil
}

// deepseekWebFetchProvider extracts page Markdown through DeepSeek's fetch API.
type deepseekWebFetchProvider struct {
	client  *http.Client
	apiKey  string
	baseURL string
}

func newDeepSeekWebFetchProvider(apiKey, baseURL string) *deepseekWebFetchProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDeepSeekBaseURL
	}
	return &deepseekWebFetchProvider{apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 65 * time.Second}}
}

func (*deepseekWebFetchProvider) Name() string { return "deepseek" }

func (p *deepseekWebFetchProvider) Fetch(ctx context.Context, target *url.URL) (string, error) {
	var response struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := deepseekPost(ctx, p.client, p.baseURL, p.apiKey, "/v1/tools/fetch", map[string]any{"url": target.String()}, &response); err != nil {
		return "", err
	}
	content := strings.TrimSpace(response.Content)
	if content == "" {
		return "", fmt.Errorf("page has no readable content")
	}
	if title := strings.TrimSpace(response.Title); title != "" && !strings.HasPrefix(content, "# ") {
		content = "# " + title + "\n\n" + content
	}
	return content, nil
}

// deepseekPost sends an authenticated JSON request and decodes the reply,
// surfacing the OpenAI-style error message on non-2xx responses.
func deepseekPost(ctx context.Context, client *http.Client, baseURL, apiKey, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if baseURL == "" {
		baseURL = defaultDeepSeekBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDeepSeekResponse+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxDeepSeekResponse {
		return fmt.Errorf("response exceeds %d-byte limit", maxDeepSeekResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return nil
}
