package server

import (
	"bufio"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/gin-gonic/gin"
)

func TestValidateGenerationOptions(t *testing.T) {
	req := sendMessageRequest{
		ReasoningEffort: "max",
		DisabledTools:   []string{"web_search", " web_fetch ", "web_search", ""},
	}
	if err := validateGenerationOptions(&req); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if len(req.DisabledTools) != 2 || req.DisabledTools[0] != "web_search" || req.DisabledTools[1] != "web_fetch" {
		t.Fatalf("expected normalized disabled tools, got %#v", req.DisabledTools)
	}

	req.ReasoningEffort = "low"
	if err := validateGenerationOptions(&req); err == nil {
		t.Fatal("expected low request override to be rejected")
	}
}

func TestListConciergesUsesPublishedRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registries := registry.NewStore(&registry.Registry{
		Identities: map[string]registry.Identity{"first": {Name: "first"}},
		Concierges: map[string]registry.Concierge{"first": {Name: "first", Identity: "first"}},
	})
	server := &Server{registries: registries}

	registries.Publish(&registry.Registry{
		Identities: map[string]registry.Identity{"updated": {Name: "updated", ReasoningEffort: registry.ReasoningHigh}},
		Concierges: map[string]registry.Concierge{"updated": {Name: "updated", Identity: "updated"}},
	})
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	server.listConcierges(context)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"name":"updated"`) || strings.Contains(recorder.Body.String(), `"name":"first"`) {
		t.Fatalf("unexpected concierge response: status %d, body %s", recorder.Code, recorder.Body.String())
	}
}

func TestBindMessageRequestParsesMultipartGenerationOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"text":                   "hello",
		"reasoning_effort":       "high",
		"active_leaf_message_id": "0",
	} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.WriteField("disabled_tools", "web_search"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("disabled_tools", "web_fetch"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	server := &Server{}
	var req sendMessageRequest

	if _, err := server.bindMessageRequest(context, &req); err != nil {
		t.Fatalf("bind multipart request: %v", err)
	}
	if req.Text != "hello" || req.ReasoningEffort != "high" {
		t.Fatalf("unexpected request fields: %+v", req)
	}
	if len(req.DisabledTools) != 2 || req.DisabledTools[0] != "web_search" || req.DisabledTools[1] != "web_fetch" {
		t.Fatalf("unexpected disabled tools: %#v", req.DisabledTools)
	}
	if req.ActiveLeafMessageID != nil {
		t.Fatalf("expected zero active leaf to be treated as unset, got %d", *req.ActiveLeafMessageID)
	}
}

func TestBindMessageRequestAllowsGenerationOnlyJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reasoning_effort":"high","disabled_tools":["web_search"]}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	server := &Server{}
	var req sendMessageRequest

	if _, err := server.bindMessageRequest(context, &req); err != nil {
		t.Fatalf("bind generation-only request: %v", err)
	}
	if req.Text != "" || req.ReasoningEffort != "high" || len(req.DisabledTools) != 1 || req.DisabledTools[0] != "web_search" {
		t.Fatalf("unexpected request fields: %+v", req)
	}
}

func TestSendMessageRequestNormalizesZeroActiveLeaf(t *testing.T) {
	zero := uint(0)
	req := sendMessageRequest{ActiveLeafMessageID: &zero}

	req.normalizeActiveLeaf()

	if req.ActiveLeafMessageID != nil {
		t.Fatalf("expected zero active leaf to mean no active leaf, got %d", *req.ActiveLeafMessageID)
	}
}

func TestSendMessageRequestSelectRootOverridesActiveLeaf(t *testing.T) {
	leaf := uint(42)
	req := sendMessageRequest{ActiveLeafMessageID: &leaf, SelectRoot: true}

	req.normalizeActiveLeaf()

	if req.ActiveLeafMessageID != nil {
		t.Fatalf("expected select_root to clear active leaf, got %d", *req.ActiveLeafMessageID)
	}
}

func TestStreamTurnFlushesProgressBeforeCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	server := &Server{commands: command.NewService(nil, nil, nil, nil, nil, nil, nil, nil)}
	engine := gin.New()
	engine.GET("/stream", func(c *gin.Context) {
		server.streamTurn(c, 1, func(_ context.Context, onDelta func(chat.StreamEvent)) (*chat.TurnResult, error) {
			onDelta(chat.StreamEvent{Type: "tool_output", ToolCall: &chat.StreamToolCall{
				CallIndex: 0,
				Index:     0,
				ID:        "call-1",
				Name:      "shell",
				Result:    "started\n",
				Status:    "calling",
			}})
			<-release
			return &chat.TurnResult{}, nil
		})
	})

	httpServer := httptest.NewServer(engine)
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected proxy buffering to be disabled, got %q", response.Header.Get("X-Accel-Buffering"))
	}

	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(response.Body).ReadString('\n')
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		if !strings.Contains(line, "event:tool_output") {
			t.Fatalf("expected tool_output before completion, got %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("tool output was not flushed before turn completion")
	}
	close(release)
}
