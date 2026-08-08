package tools

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type fakeWebFetchProvider struct {
	name  string
	text  string
	err   error
	calls int
}

func (p *fakeWebFetchProvider) Name() string { return p.name }
func (p *fakeWebFetchProvider) Fetch(context.Context, *url.URL) (string, error) {
	p.calls++
	return p.text, p.err
}

func TestWebFetchRejectsLoopback(t *testing.T) {
	provider := &fakeWebFetchProvider{name: "test", text: "unused"}
	result := newWebFetchToolForTest(0, 0, provider, nil, nil).Execute(context.Background(), map[string]any{"url": "http://127.0.0.1/"})
	if !result.IsError || !strings.Contains(result.ForLLM, "private or local") {
		t.Fatalf("expected loopback rejection, got %+v", result)
	}
	if provider.calls != 0 {
		t.Fatal("provider was called for rejected URL")
	}
}

func TestWebFetchFallsBack(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "primary", err: errors.New("unavailable")}
	fallback := &fakeWebFetchProvider{name: "fallback", text: "fallback content"}
	result := newWebFetchToolForTest(0, 0, primary, fallback, nil).Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if result.IsError || result.ForLLM != "fallback content" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("unexpected calls: primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestWebFetchDoesNotFallbackAfterSuccess(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "primary", text: "primary content"}
	fallback := &fakeWebFetchProvider{name: "fallback", text: "fallback content"}
	result := newWebFetchToolForTest(0, 0, primary, fallback, nil).Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if result.IsError || result.ForLLM != "primary content" || fallback.calls != 0 {
		t.Fatalf("unexpected result: %+v, fallback calls=%d", result, fallback.calls)
	}
}

func TestWebFetchReportsBothProviderFailures(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "firecrawl", err: errors.New("rate limited")}
	fallback := &fakeWebFetchProvider{name: "local", err: errors.New("browser missing")}
	result := newWebFetchToolForTest(0, 0, primary, fallback, nil).Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if !result.IsError || !strings.Contains(result.ForLLM, "firecrawl failed: rate limited") || !strings.Contains(result.ForLLM, "local fallback failed: browser missing") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWebFetchCapsRequestMaxChars(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "primary", text: strings.Repeat("a", 300)}
	result := newWebFetchToolForTest(200, 0, primary, nil, nil).Execute(context.Background(), map[string]any{
		"url":       "https://example.com",
		"max_chars": float64(250),
	})
	if result.IsError || result.ForLLM != strings.Repeat("a", 200)+"\n[TRUNCATED]" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWebFetchSummarizesLargeContent(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "primary", text: strings.Repeat("a", 50)}
	var gotTarget int
	summarizer := func(_ context.Context, _ string, maxOutputLen int) (string, error) {
		gotTarget = maxOutputLen
		return "condensed summary", nil
	}
	result := newWebFetchToolForTest(100, 10, primary, nil, summarizer).Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if result.IsError || result.ForLLM != "condensed summary" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotTarget != 10 {
		t.Fatalf("summarizer target = %d, want 10", gotTarget)
	}
}

func TestWebFetchSkipsSummarizeForShortContent(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "primary", text: "short"}
	called := false
	summarizer := func(_ context.Context, _ string, _ int) (string, error) {
		called = true
		return "", nil
	}
	result := newWebFetchToolForTest(100, 50, primary, nil, summarizer).Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if result.IsError || result.ForLLM != "short" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if called {
		t.Fatal("summarizer should not be called for short content")
	}
}

func TestWebFetchDegradesToTruncationOnSummarizeError(t *testing.T) {
	primary := &fakeWebFetchProvider{name: "primary", text: strings.Repeat("a", 50)}
	summarizer := func(_ context.Context, _ string, _ int) (string, error) {
		return "", errors.New("summarizer down")
	}
	result := newWebFetchToolForTest(100, 10, primary, nil, summarizer).Execute(context.Background(), map[string]any{"url": "https://example.com"})
	if result.IsError || result.ForLLM != strings.Repeat("a", 50) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestLocalWebFetchProviderLive(t *testing.T) {
	if os.Getenv("HEPHAESTUS_RUN_LIVE_TESTS") == "" {
		t.Skip("set HEPHAESTUS_RUN_LIVE_TESTS=1 to run the Chrome-backed web-fetch test")
	}
	chromePath := os.Getenv("HEPHAESTUS_WEB_FETCH_CHROME_PATH")
	if chromePath == "" {
		var err error
		chromePath, err = exec.LookPath("google-chrome")
		if err != nil {
			t.Skip("google-chrome is not installed")
		}
	}
	target, _ := url.Parse("https://example.com")
	text, err := newLocalWebFetchProvider(chromePath).Fetch(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Example Domain") {
		t.Fatalf("unexpected page text: %q", text)
	}
}

func TestWebFetchRuneTruncation(t *testing.T) {
	got := truncateWebText("你好吗", 2)
	if got != "你好\n[TRUNCATED]" {
		t.Fatalf("unexpected rune truncation: %q", got)
	}
}
