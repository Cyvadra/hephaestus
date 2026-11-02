package transform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Cyvadra/hephaestus/internal/llm"
)

// testClient returns an *llm.Client pointed at a stub chat-completions
// server so Compress/Summarize can be exercised without real credentials.
func testClient(t *testing.T, handler http.HandlerFunc) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash"}]}`))
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

func TestLimitTextBytesPreservesValidUTF8HeadAndTail(t *testing.T) {
	content := "alpha-你好-world-再见-omega"
	got := LimitTextBytes(content, 28)
	if !utf8.ValidString(got) {
		t.Fatalf("LimitTextBytes returned invalid UTF-8: %q", got)
	}
	if len(got) > 28 {
		t.Fatalf("length = %d, limit = 28: %q", len(got), got)
	}
	if got == content || got[0] != content[0] || got[len(got)-1] != content[len(content)-1] {
		t.Fatalf("did not preserve head and tail: %q", got)
	}
}

func TestLimitToolExchangeContentUsesStrictCombinedLimit(t *testing.T) {
	arguments := strings.Repeat("a", MaxToolExchangeBytes-10)
	got := LimitToolExchangeContent(arguments, "0123456789abcdef")
	if len(arguments)+len(got) >= MaxToolExchangeBytes {
		t.Fatalf("combined exchange size = %d, must be below %d", len(arguments)+len(got), MaxToolExchangeBytes)
	}
	if len(got) != 9 {
		t.Fatalf("limited content length = %d, want 9", len(got))
	}
}

func TestLimitToolExchangeContentStripsNulBytes(t *testing.T) {
	got := LimitToolExchangeContent("", "a\x00b\x00c")
	if strings.Contains(got, "\x00") {
		t.Fatalf("result = %q, must not contain NUL bytes", got)
	}
	if got != "abc" {
		t.Fatalf("result = %q, want %q", got, "abc")
	}
}

func TestLimitToolExchangeContentOmitsResultWhenArgumentsFillBudget(t *testing.T) {
	if got := LimitToolExchangeContent(strings.Repeat("a", MaxToolExchangeBytes-1), "result"); got != "" {
		t.Fatalf("result = %q, want empty", got)
	}
}

// compressClient returns a client whose chat-completions response carries
// response, plus a channel delivering the request body for prompt assertions.
func compressClient(t *testing.T, response string) (*llm.Client, <-chan string) {
	t.Helper()
	bodyCh := make(chan string, 1)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bodyCh <- string(raw)
		payload, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": response},
				"finish_reason": "stop",
			}},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	return client, bodyCh
}

// userPromptFromRequest extracts the user prompt from a captured
// chat-completions request body.
func userPromptFromRequest(t *testing.T, body string) string {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	for _, m := range request.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	t.Fatal("request has no user message")
	return ""
}

func TestCompress(t *testing.T) {
	checkpoint := "## Primary Request and Intent\n- Ship the change."
	client, bodyCh := compressClient(t, checkpoint)

	messages := []CompressMessage{
		{Role: "user", Content: strings.Repeat("old context ", 200)},
		{Role: "assistant", Content: strings.Repeat("assistant reply ", 200)},
	}
	out, err := Compress(context.Background(), client, nil, messages, 10)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if out.Role != "user" {
		t.Fatalf("checkpoint role = %q, want user", out.Role)
	}
	for _, want := range []string{checkpointPreamble, summaryOpenTag, checkpoint, summaryCloseTag} {
		if !strings.Contains(out.Content, want) {
			t.Errorf("checkpoint missing %q: %q", want, out.Content)
		}
	}

	prompt := userPromptFromRequest(t, <-bodyCh)
	for _, want := range []string{"Messages to compress:", "[user]", "[assistant]", "old context", "assistant reply"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %q", want, prompt)
		}
	}
	if strings.Contains(prompt, "Prior checkpoint") {
		t.Errorf("prompt has a prior section without prior messages: %q", prompt)
	}
}

func TestCompressMergesPriorCheckpoint(t *testing.T) {
	client, bodyCh := compressClient(t, "## Critical Context\n- merged")
	prior := []Message{{Role: "user", Content: "PRIOR-CHECKPOINT-TEXT"}}
	messages := []CompressMessage{{Role: "user", Content: strings.Repeat("new history ", 200)}}

	out, err := Compress(context.Background(), client, prior, messages, 100)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if out.Role != "user" || !strings.Contains(out.Content, "<compacted-summary>") {
		t.Fatalf("unexpected checkpoint: %+v", out)
	}

	prompt := userPromptFromRequest(t, <-bodyCh)
	for _, want := range []string{"Prior checkpoint to merge", "PRIOR-CHECKPOINT-TEXT", "new history"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %q", want, prompt)
		}
	}
}

func TestCompressIncludesToolCalls(t *testing.T) {
	client, bodyCh := compressClient(t, "## Files and Code\n- internal/chat/pipeline.go")
	messages := []CompressMessage{
		{Role: "assistant", Content: strings.Repeat("calling tool ", 100), ToolCalls: `[{"id":"call-1"}]`},
		{Role: "tool", Content: strings.Repeat("tool output ", 100), ToolCallID: "call-1"},
	}

	if _, err := Compress(context.Background(), client, nil, messages, 100); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	prompt := userPromptFromRequest(t, <-bodyCh)
	for _, want := range []string{`tool_calls: [{"id":"call-1"}]`, "[tool call_id=call-1]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %q", want, prompt)
		}
	}
}

func TestCompressRejectsEmptyOutput(t *testing.T) {
	client, _ := compressClient(t, "   ")
	messages := []CompressMessage{{Role: "user", Content: strings.Repeat("history ", 200)}}
	if _, err := Compress(context.Background(), client, nil, messages, 10); !errors.Is(err, ErrCompressionEmpty) {
		t.Fatalf("Compress() error = %v, want ErrCompressionEmpty", err)
	}
}

func TestCompressRejectsNonShrinkingOutput(t *testing.T) {
	client, _ := compressClient(t, strings.Repeat("oversized checkpoint ", 100))
	messages := []CompressMessage{{Role: "user", Content: "x"}}
	if _, err := Compress(context.Background(), client, nil, messages, 10); !errors.Is(err, ErrCompressionNotSmaller) {
		t.Fatalf("Compress() error = %v, want ErrCompressionNotSmaller", err)
	}
}

func TestCompressStripsNulBytes(t *testing.T) {
	client, _ := compressClient(t, "checkpoint\x00text")
	messages := []CompressMessage{{Role: "user", Content: strings.Repeat("history ", 200)}}
	out, err := Compress(context.Background(), client, nil, messages, 10)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if strings.Contains(out.Content, "\x00") {
		t.Fatalf("checkpoint contains a NUL byte: %q", out.Content)
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
