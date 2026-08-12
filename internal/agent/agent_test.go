package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// fakeLLM is a scriptable LLM stub satisfying the Runner's LLM interface.
type fakeLLM struct {
	mu        sync.Mutex
	responses []*ds4.ChatResponse
	err       error
	calls     []llmCall
}

type llmCall struct {
	messages []store.ChatMessage
	tools    []toolkit.Tool
	stream   bool
}

func (f *fakeLLM) Call(_ context.Context, _ registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool) (*ds4.ChatResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, llmCall{messages: messages, tools: toolset, stream: false})
	resp, err := f.next()
	f.mu.Unlock()
	return resp, err
}

func (f *fakeLLM) CallStream(_ context.Context, _ registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool, _ func(llm.StreamDelta)) (*ds4.ChatResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, llmCall{messages: messages, tools: toolset, stream: true})
	resp, err := f.next()
	f.mu.Unlock()
	return resp, err
}

func (f *fakeLLM) next() (*ds4.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) == 0 {
		return &ds4.ChatResponse{Choices: []ds4.Choice{{Message: ds4.Message{Role: ds4.RoleAssistant, Content: ""}, FinishReason: ds4.FinishReasonStop}}}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *fakeLLM) toolsLastCall() []toolkit.Tool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1].tools
}

// echoTool is a deterministic tool that records its execution.
type echoTool struct {
	name   string
	scopes []toolkit.Scope
	execs  *[]string
}

func (t echoTool) Name() string               { return t.name }
func (t echoTool) Description() string        { return "" }
func (t echoTool) Parameters() map[string]any { return nil }
func (t echoTool) Execute(_ context.Context, _ map[string]any) *toolkit.ToolResult {
	if t.execs != nil {
		*t.execs = append(*t.execs, t.name)
	}
	return toolkit.NewToolResult("ran " + t.name)
}

// Scopes reports all scopes when unset, so plain echoTool runs everywhere.
func (t echoTool) Scopes() []toolkit.Scope {
	if t.scopes == nil {
		return []toolkit.Scope{toolkit.ScopeSession, toolkit.ScopeWorkflow}
	}
	return t.scopes
}

func testRunner(llm LLM, interactions *interaction.Manager) *Runner {
	return NewRunner(llm, plugin.NewRegistry(notify.New("")), interactions, nil, notify.New(""))
}

func respWith(toolCalls []ds4.ToolCall, content string) *ds4.ChatResponse {
	finish := ds4.FinishReasonStop
	if len(toolCalls) > 0 {
		finish = ds4.FinishReasonToolCalls
	}
	return &ds4.ChatResponse{
		Choices: []ds4.Choice{{
			Message:      ds4.Message{Role: ds4.RoleAssistant, Content: content, ToolCalls: toolCalls},
			FinishReason: finish,
		}},
	}
}

func toolCall(id, name, args string) ds4.ToolCall {
	return ds4.ToolCall{ID: id, Type: "function", Function: ds4.FunctionCall{Name: name, Arguments: args}}
}

func sessionRequest(llm *fakeLLM, toolset []toolkit.Tool, turn plugin.TurnContext) Request {
	return Request{
		Identity: registry.Identity{Name: "default"},
		Toolset:  toolset,
		Plugins:  nil,
		Turn:     turn,
		Scope:    toolkit.ScopeSession,
		Audit:    AuditOwner{SessionID: &turn.SessionID},
		OwnerID:  turn.SessionID,
	}
}

func TestRun_NormalCompletion(t *testing.T) {
	fake := &fakeLLM{responses: []*ds4.ChatResponse{respWith(nil, "hello")}}
	runner := testRunner(fake, nil)

	run := uint(7)
	turn := plugin.TurnContext{SessionID: run, Scope: toolkit.ScopeSession, Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "hi"}}, Metadata: map[string]any{}}
	result, err := runner.Run(context.Background(), sessionRequest(fake, nil, turn))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected one assistant message, got %+v", result.Messages)
	}
	if result.Messages[0].Role != ds4.RoleAssistant || result.Messages[0].Content != "hello" {
		t.Fatalf("unexpected message: %+v", result.Messages[0])
	}
	if n := len(result.Turn.Messages); n == 0 || result.Turn.Messages[n-1].Content != "hello" {
		t.Fatalf("expected final turn message to be the reply, got %+v", result.Turn.Messages)
	}
}

func TestRun_ToolCallCycle(t *testing.T) {
	var execs []string
	fake := &fakeLLM{responses: []*ds4.ChatResponse{
		respWith([]ds4.ToolCall{toolCall("call-1", "echo", "{}")}, ""),
		respWith(nil, "final answer"),
	}}
	runner := testRunner(fake, nil)

	run := uint(7)
	turn := plugin.TurnContext{SessionID: run, Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "go"}}, Metadata: map[string]any{}}
	result, err := runner.Run(context.Background(), sessionRequest(fake, []toolkit.Tool{echoTool{name: "echo", execs: &execs}}, turn))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(execs) != 1 || execs[0] != "echo" {
		t.Fatalf("expected echo tool to run once, got %v", execs)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("expected assistant-toolcall, tool, assistant messages, got %+v", result.Messages)
	}
	if result.Messages[0].Role != ds4.RoleAssistant || len(result.Messages[0].ToolCalls) == 0 {
		t.Fatalf("expected assistant tool-call message, got %+v", result.Messages[0])
	}
	if result.Messages[1].Role != ds4.RoleTool || result.Messages[1].Content != "ran echo" {
		t.Fatalf("expected tool result message, got %+v", result.Messages[1])
	}
	if result.Messages[2].Role != ds4.RoleAssistant || result.Messages[2].Content != "final answer" {
		t.Fatalf("expected final assistant message, got %+v", result.Messages[2])
	}
}

func TestRun_ConsecutiveCallLimitWithoutInteraction(t *testing.T) {
	// 13 identical consecutive tool calls exceed the limit with no approval
	// channel, which must abort the run.
	var calls []ds4.ToolCall
	for i := 0; i < 13; i++ {
		calls = append(calls, toolCall(fmt.Sprintf("call-%d", i), "echo", "{}"))
	}
	fake := &fakeLLM{responses: []*ds4.ChatResponse{respWith(calls, "")}}
	runner := testRunner(fake, nil)

	run := uint(7)
	turn := plugin.TurnContext{SessionID: run, Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "go"}}, Metadata: map[string]any{}}
	_, err := runner.Run(context.Background(), sessionRequest(fake, []toolkit.Tool{echoTool{name: "echo"}}, turn))
	if err == nil {
		t.Fatal("expected consecutive tool-call limit error without interaction")
	}
}

func TestRun_FiltersSessionOnlyToolsInWorkflowScope(t *testing.T) {
	sessionOnly := echoTool{name: "chat_history_search", scopes: []toolkit.Scope{toolkit.ScopeSession}}
	safe := echoTool{name: "read_file"}
	fake := &fakeLLM{responses: []*ds4.ChatResponse{respWith(nil, "done")}}
	runner := testRunner(fake, nil)

	turn := plugin.TurnContext{Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "go"}}, Metadata: map[string]any{}}
	req := sessionRequest(fake, []toolkit.Tool{sessionOnly, safe}, turn)
	req.Scope = toolkit.ScopeWorkflow
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tools := fake.toolsLastCall()
	if len(tools) != 1 || tools[0].Name() != "read_file" {
		t.Fatalf("expected session-only tool filtered from workflow scope, got %+v", tools)
	}
}

func TestRun_SessionScopeKeepsSessionOnlyTools(t *testing.T) {
	sessionOnly := echoTool{name: "chat_history_search", scopes: []toolkit.Scope{toolkit.ScopeSession}}
	fake := &fakeLLM{responses: []*ds4.ChatResponse{respWith(nil, "done")}}
	runner := testRunner(fake, nil)

	turn := plugin.TurnContext{Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "go"}}, Metadata: map[string]any{}}
	if _, err := runner.Run(context.Background(), sessionRequest(fake, []toolkit.Tool{sessionOnly}, turn)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tools := fake.toolsLastCall()
	if len(tools) != 1 || tools[0].Name() != "chat_history_search" {
		t.Fatalf("expected session-only tool kept in session scope, got %+v", tools)
	}
}

func TestExecuteTool_RejectsToolOutsideExpandedSet(t *testing.T) {
	runner := testRunner(&fakeLLM{}, nil)
	result := runner.executeTool(context.Background(), Request{}, map[string]toolkit.Tool{}, ds4.ToolCall{
		Function: ds4.FunctionCall{Name: "shell"},
	}, nil)
	if !result.IsError {
		t.Fatal("expected disabled tool to be rejected")
	}
}

func TestTrackConsecutiveToolCall_RejectsRepeatedCallWithoutInteractiveApproval(t *testing.T) {
	runner := testRunner(&fakeLLM{}, nil)
	req := Request{} // OnInteraction nil
	lastToolName := ""
	consecutiveToolCalls := 0
	for range maxConsecutiveToolCalls {
		if err := runner.trackConsecutiveToolCall(context.Background(), req, &lastToolName, &consecutiveToolCalls, "search"); err != nil {
			t.Fatalf("expected call within limit to succeed: %v", err)
		}
	}
	if err := runner.trackConsecutiveToolCall(context.Background(), req, &lastToolName, &consecutiveToolCalls, "search"); err == nil {
		t.Fatal("expected repeated call beyond the limit to be rejected")
	}
}

func TestTrackConsecutiveToolCall_ApprovalResetsCounter(t *testing.T) {
	manager := interaction.NewManager()
	runner := testRunner(&fakeLLM{}, manager)
	events := make(chan *interaction.Request, 1)
	req := Request{OwnerID: 7, OnInteraction: func(request *interaction.Request) {
		events <- request
	}}
	lastToolName := "shell"
	consecutiveToolCalls := maxConsecutiveToolCalls
	done := make(chan error, 1)

	go func() {
		done <- runner.trackConsecutiveToolCall(context.Background(), req, &lastToolName, &consecutiveToolCalls, "shell")
	}()

	request := <-events
	if request.SessionID != 7 || request.Title != "Continue repeated tool calls?" {
		t.Fatalf("unexpected permission request: %+v", request)
	}
	if err := manager.Respond(7, true); err != nil {
		t.Fatalf("approve repeated calls: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("expected approved call to succeed: %v", err)
	}
	if consecutiveToolCalls != 1 {
		t.Fatalf("expected approval to reset counter to 1, got %d", consecutiveToolCalls)
	}
}

func TestBeginToolAudit_SkipsNonAuditedTool(t *testing.T) {
	runner := testRunner(&fakeLLM{}, nil)
	if id := runner.beginToolAudit(AuditOwner{}, echoTool{name: "echo"}, ds4.ToolCall{}, map[string]any{}); id != 0 {
		t.Fatalf("expected non-audited tool to skip audit, got audit id %d", id)
	}
}
