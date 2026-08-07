package tools

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestWebFetchRejectsLoopback(t *testing.T) {
	result := NewWebFetchTool(0, 0).Execute(context.Background(), map[string]any{"url": "http://127.0.0.1/"})
	if !result.IsError || !strings.Contains(result.ForLLM, "private or local") {
		t.Fatalf("expected loopback rejection, got %+v", result)
	}
}

func TestExtractWebText(t *testing.T) {
	text := extractWebText("<html><style>.hidden{}</style><body><script>bad()</script><h1>Hello</h1><p>world &amp; friends</p></body></html>", "text/html")
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "world & friends") || strings.Contains(text, "bad") {
		t.Fatalf("unexpected extracted text: %q", text)
	}
}

func TestSafeDialContextDialsResolvedAddress(t *testing.T) {
	var dialed string
	dial := safeDialContext(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, context.Canceled
	})
	_, _ = dial(context.Background(), "tcp", "example.com:443")
	if strings.Contains(dialed, "example.com") {
		t.Fatalf("expected validated IP address, got %q", dialed)
	}
}

func TestWebFetchRuneTruncation(t *testing.T) {
	got := truncateWebText("你好吗", 2)
	if got != "你好\n[TRUNCATED]" {
		t.Fatalf("unexpected rune truncation: %q", got)
	}
}
