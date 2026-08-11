package transform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/llm"
)

// testClient returns an *llm.Client pointed at a stub chat-completions
// server so Compress/Summarize can be exercised without real credentials.
func testClient(t *testing.T, handler http.HandlerFunc) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return llm.NewWithBaseURL("test-key", server.URL)
}

func TestEstimateLength(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"ab", 1},   // 2 runes / 2.0 divisor
		{"abcd", 2}, // 4 runes / 2.0 divisor
		{"你好", 1},   // 2 CJK runes / 2.0 divisor
	}
	for _, c := range cases {
		if got := EstimateLength(c.text); got != c.want {
			t.Errorf("EstimateLength(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestCompress(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[{\"role\":\"user\",\"content\":\"keep\"}]"},"finish_reason":"stop"}]}`))
	})

	out, err := Compress(context.Background(), client, []Message{{Role: "user", Content: "long history"}, {Role: "assistant", Content: "reply"}}, 10)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if len(out) != 1 || out[0].Role != "user" || out[0].Content != "keep" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestCompressRejectsDisallowedRole(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[{\"role\":\"system\",\"content\":\"x\"}]"},"finish_reason":"stop"}]}`))
	})
	if _, err := Compress(context.Background(), client, []Message{{Role: "user", Content: "x"}}, 10); err == nil {
		t.Fatal("Compress() expected error for disallowed role")
	}
}

func TestCompressRejectsNonJSON(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"not json"},"finish_reason":"stop"}]}`))
	})
	if _, err := Compress(context.Background(), client, []Message{{Role: "user", Content: "x"}}, 10); err == nil {
		t.Fatal("Compress() expected parse error")
	}
}

func TestSummarize(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":" condensed summary "},"finish_reason":"stop"}]}`))
	})
	got, err := Summarize(context.Background(), client, "long fetched text", 100)
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if got != "condensed summary" {
		t.Fatalf("Summarize() = %q, want trimmed content", got)
	}
}

func TestSummarizeRejectsEmpty(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"   "},"finish_reason":"stop"}]}`))
	})
	if _, err := Summarize(context.Background(), client, "text", 100); err == nil {
		t.Fatal("Summarize() expected empty-output error")
	}
}

func TestMaxOutputTokens(t *testing.T) {
	cases := []struct {
		target int
		want   int
	}{
		{10, 512},    // floor
		{1000, 3000}, // headroom multiplier
		{0, 512},     // floor again
	}
	for _, c := range cases {
		if got := maxOutputTokens(c.target); got != c.want {
			t.Errorf("maxOutputTokens(%d) = %d, want %d", c.target, got, c.want)
		}
	}
}

func TestSessionTitleSummary(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Title\nSummary"},"finish_reason":"stop"}]}`))
	})
	got, err := SessionTitleSummary(context.Background(), client, "prompt", 256)
	if err != nil {
		t.Fatalf("SessionTitleSummary() error = %v", err)
	}
	if got != "Title\nSummary" {
		t.Fatalf("SessionTitleSummary() = %q", got)
	}
}

func TestSuggestOptions(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[\"a\",\"b\"]"},"finish_reason":"stop"}]}`))
	})
	got, err := SuggestOptions(context.Background(), client, "prompt", 200)
	if err != nil {
		t.Fatalf("SuggestOptions() error = %v", err)
	}
	if got != `["a","b"]` {
		t.Fatalf("SuggestOptions() = %q", got)
	}
}

func TestStorylineStatus(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  HP: 100, MP: 57  "},"finish_reason":"stop"}]}`))
	})
	got, err := StorylineStatus(context.Background(), client, "prompt", 128)
	if err != nil {
		t.Fatalf("StorylineStatus() error = %v", err)
	}
	if got != "HP: 100, MP: 57" {
		t.Fatalf("StorylineStatus() = %q", got)
	}
}

func TestSummarizeSearchResults(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"1. Title\n   https://example.com"},"finish_reason":"stop"}]}`))
	})
	got, err := SummarizeSearchResults(context.Background(), client, "1. Title\n   https://example.com", 100)
	if err != nil {
		t.Fatalf("SummarizeSearchResults() error = %v", err)
	}
	if got != "1. Title\n   https://example.com" {
		t.Fatalf("SummarizeSearchResults() = %q", got)
	}
}
