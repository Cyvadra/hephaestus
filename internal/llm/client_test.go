package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/datatypes"
)

// exampleTool exercises the toolkit.Example capability: buildChat must append
// the example to the tool's description when it is registered to a request.
type exampleTool struct{}

func (exampleTool) Name() string        { return "example_tool" }
func (exampleTool) Description() string { return "A tool with a usage example." }
func (exampleTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (exampleTool) Example() string {
	return `{"action": "run", "command": "uname -a"}` + "\n\u2192 Linux test 6.8.0 x86_64"
}
func (exampleTool) Execute(context.Context, map[string]any) *toolkit.ToolResult {
	return toolkit.NewToolResult("ok")
}

func TestCallAttachesToolExampleToDescription(t *testing.T) {
	var captured ds4.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := &Client{ds4: ds4.New("test").WithBaseURL(server.URL)}
	if _, err := client.Call(context.Background(), registry.Identity{}, []store.ChatMessage{}, []toolkit.Tool{exampleTool{}}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var found *ds4.Function
	for i := range captured.Tools {
		if captured.Tools[i].Function.Name == "example_tool" {
			found = &captured.Tools[i].Function
		}
	}
	if found == nil {
		t.Fatal("expected example_tool in request tools")
	}
	if !strings.Contains(found.Description, "Example:") || !strings.Contains(found.Description, "uname -a") || !strings.Contains(found.Description, "Linux test") {
		t.Errorf("expected example attached to description, got %q", found.Description)
	}
}

func TestCallRoutesLocalModelAlias(t *testing.T) {
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("official request path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
	}))
	defer official.Close()

	var localRequest ds4.ChatRequest
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
		case "/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer local-key" {
				t.Fatalf("local authorization = %q, want local key", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&localRequest); err != nil {
				t.Fatalf("decode local request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
		default:
			t.Fatalf("local request path = %q", r.URL.Path)
		}
	}))
	defer local.Close()

	client := NewWithLocalModel("official-key", local.URL, "local-key")
	client.ds4.WithBaseURL(official.URL)
	response, err := client.Call(context.Background(), registry.Identity{PreferredModel: "local-model"}, []store.ChatMessage{{Role: ds4.RoleUser, Content: "Hello"}}, nil)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if response.Content() != "done" {
		t.Fatalf("response content = %q, want done", response.Content())
	}
	if localRequest.Model != "local-model" {
		t.Fatalf("local request model = %q, want local-model", localRequest.Model)
	}
}

func TestCallWithoutThinkingReusesIdentityAndFullContext(t *testing.T) {
	var request ds4.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"custom-model"}]}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	temperature := 0.3
	topP := 0.8
	identity := registry.Identity{
		PreferredModel:  "custom-model",
		ReasoningEffort: registry.ReasoningHigh,
		MaxTokens:       321,
		Temperature:     &temperature,
		TopP:            &topP,
		SystemPrompt:    "system prompt",
		InjectedMessages: []registry.Message{
			{Role: ds4.RoleSystem, Content: "injected"},
		},
	}
	messages := []store.ChatMessage{
		{Role: ds4.RoleUser, Content: "first"},
		{Role: ds4.RoleAssistant, Content: "answer"},
		{Role: ds4.RoleUser, Content: "summary instruction"},
	}

	client := &Client{ds4: ds4.New("test").WithBaseURL(server.URL)}
	if _, err := client.CallWithoutThinking(context.Background(), identity, messages); err != nil {
		t.Fatalf("CallWithoutThinking() error = %v", err)
	}

	if request.Model != identity.PreferredModel || request.MaxTokens != identity.MaxTokens {
		t.Fatalf("model/max tokens = %q/%d, want %q/%d", request.Model, request.MaxTokens, identity.PreferredModel, identity.MaxTokens)
	}
	if request.Temperature == nil || *request.Temperature != temperature || request.TopP == nil || *request.TopP != topP {
		t.Fatalf("temperature/top_p = %v/%v, want %v/%v", request.Temperature, request.TopP, temperature, topP)
	}
	if request.Thinking == nil || request.Thinking.Type != "disabled" || request.ReasoningEffort != "" {
		t.Fatalf("thinking = %+v, reasoning_effort = %q", request.Thinking, request.ReasoningEffort)
	}
	wantContents := []string{"system prompt", "injected", "first", "answer", "summary instruction"}
	if len(request.Messages) != len(wantContents) {
		t.Fatalf("message count = %d, want %d: %+v", len(request.Messages), len(wantContents), request.Messages)
	}
	for index, want := range wantContents {
		if request.Messages[index].Content != want {
			t.Fatalf("message %d content = %q, want %q", index, request.Messages[index].Content, want)
		}
	}
}

func TestContinueStreamUsesAssistantPrefixCompletion(t *testing.T) {
	var request ds4.ChatRequest
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
			return
		}
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" continued\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()

	client := &Client{ds4: ds4.New("test").WithBaseURL(server.URL)}
	response, err := client.ContinueStream(
		context.Background(),
		registry.Identity{},
		[]store.ChatMessage{
			{Role: ds4.RoleUser, Content: "Tell a story"},
			{Role: ds4.RoleAssistant, Content: "", ToolCalls: datatypes.JSON(`[{"id":"call-1","type":"function","function":{"name":"shell","arguments":"{}"}}]`)},
			{Role: ds4.RoleTool, Content: "tool output", ToolCallID: "call-1"},
		},
		store.ChatMessage{Role: ds4.RoleAssistant, Content: "Once upon"},
		nil,
	)
	if err != nil {
		t.Fatalf("ContinueStream() error = %v", err)
	}
	if requestPath != "/beta/chat/completions" {
		t.Fatalf("request path = %q, want beta prefix endpoint", requestPath)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != ds4.RoleAssistant || last.Content != "Once upon" || !last.Prefix {
		t.Fatalf("expected final assistant prefix, got %+v", last)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("expected continuation request to disable tools, got %+v", request.Tools)
	}
	for _, message := range request.Messages {
		if message.Role == ds4.RoleTool || len(message.ToolCalls) > 0 {
			t.Fatalf("expected continuation request to exclude tool-call history, got %+v", message)
		}
	}
	if response.Content() != " continued" {
		t.Fatalf("response content = %q", response.Content())
	}
}

func TestRawCallRetriesWithProviderMaxTokens(t *testing.T) {
	var received []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"}]}`))
			return
		}
		var request ds4.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		received = append(received, request.MaxTokens)
		w.Header().Set("Content-Type", "application/json")

		if request.MaxTokens > 100 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 100]","param":"max_tokens"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"compressed"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := &Client{ds4: ds4.New("test").WithBaseURL(server.URL)}
	content, err := client.RawCall(context.Background(), "system", "user", 101)
	if err != nil {
		t.Fatalf("RawCall() error = %v", err)
	}
	if content != "compressed" {
		t.Errorf("RawCall() content = %q, want %q", content, "compressed")
	}
	if want := []int{101, 100}; !reflect.DeepEqual(received, want) {
		t.Errorf("max_tokens requests = %v, want %v", received, want)
	}
}

func TestMaxTokensUpperBoundRejectsUnrelatedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"non-range message", &ds4.APIError{StatusCode: http.StatusBadRequest, Message: "invalid request"}},
		{"non-client error", &ds4.APIError{StatusCode: http.StatusInternalServerError, Message: "valid range of max_tokens is [1, 100]"}},
		{"non-lowering bound", &ds4.APIError{StatusCode: http.StatusBadRequest, Message: "valid range of max_tokens is [1, 200]"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := maxTokensUpperBound(test.err, 100); ok {
				t.Fatal("maxTokensUpperBound() unexpectedly accepted error")
			}
		})
	}
}
