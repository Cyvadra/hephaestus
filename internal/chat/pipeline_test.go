package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/datatypes"
)

func TestLastUserMessage_ReturnsTrailingUserMessage(t *testing.T) {
	messages := []store.ChatMessage{
		{Role: ds4.RoleAssistant, Content: "earlier"},
		{Role: ds4.RoleUser, Content: "pending"},
	}
	got, err := lastUserMessage(messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Content != "pending" {
		t.Fatalf("expected trailing user message, got %+v", got)
	}
}

func TestLastUserMessage_RejectsNonUserTrailingMessage(t *testing.T) {
	messages := []store.ChatMessage{
		{Role: ds4.RoleUser, Content: "pending"},
		{Role: ds4.RoleAssistant, Content: "a plugin appended this after the user turn"},
	}
	if _, err := lastUserMessage(messages); err == nil {
		t.Fatal("expected error when trailing message is not role user")
	}
}

func TestLastUserMessage_RejectsEmpty(t *testing.T) {
	if _, err := lastUserMessage(nil); err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func TestExecuteTool_RejectsToolOutsideExpandedSet(t *testing.T) {
	pipeline := &Pipeline{}
	result := pipeline.executeTool(context.Background(), 1, map[string]toolkit.Tool{}, ds4.ToolCall{
		Function: ds4.FunctionCall{Name: "shell"},
	}, nil)
	if !result.IsError {
		t.Fatal("expected disabled tool to be rejected")
	}
}

func TestApplyTurnOptionsOverridesReasoningAndFiltersOnlyDisabledTools(t *testing.T) {
	identity := registry.Identity{ReasoningEffort: registry.ReasoningLow}
	webSearch := namedTool{name: "web_search"}
	webFetch := namedTool{name: "web_fetch"}
	shell := namedTool{name: "shell"}

	gotIdentity, gotTools := applyTurnOptions(identity, []toolkit.Tool{webSearch, webFetch, shell}, TurnOptions{
		ReasoningEffort: registry.ReasoningMax,
		DisabledTools:   []string{"web_search", "web_fetch"},
	})

	if gotIdentity.ReasoningEffort != registry.ReasoningMax {
		t.Fatalf("expected max reasoning effort, got %q", gotIdentity.ReasoningEffort)
	}
	if identity.ReasoningEffort != registry.ReasoningLow {
		t.Fatalf("expected original identity to stay unchanged, got %q", identity.ReasoningEffort)
	}
	if len(gotTools) != 1 || gotTools[0].Name() != "shell" {
		t.Fatalf("expected only shell to remain, got %+v", gotTools)
	}
}

func TestApplyTurnOptionsWithoutOverridesPreservesDefaults(t *testing.T) {
	identity := registry.Identity{ReasoningEffort: registry.ReasoningHigh}
	tools := []toolkit.Tool{namedTool{name: "web_search"}}

	gotIdentity, gotTools := applyTurnOptions(identity, tools, TurnOptions{})

	if gotIdentity.ReasoningEffort != registry.ReasoningHigh || len(gotTools) != 1 || gotTools[0].Name() != "web_search" {
		t.Fatalf("expected defaults to remain unchanged, got identity=%+v tools=%+v", gotIdentity, gotTools)
	}
}

type namedTool struct {
	name string
}

func (tool namedTool) Name() string          { return tool.name }
func (namedTool) Description() string        { return "" }
func (namedTool) Parameters() map[string]any { return nil }
func (namedTool) Execute(context.Context, map[string]any) *toolkit.ToolResult {
	return &toolkit.ToolResult{}
}

func TestNewTurnContextPreservesFirstTurnMetadata(t *testing.T) {
	turn := newTurnContext(7, []store.ChatMessage{{Role: ds4.RoleUser, Content: "first"}}, true, "first")
	if !turn.IsFirstTurn || turn.FirstUserMessage != "first" || turn.Metadata == nil {
		t.Fatalf("unexpected turn context: %+v", turn)
	}
}

func TestTrackConsecutiveToolCall_RejectsRepeatedCallWithoutInteractiveApproval(t *testing.T) {
	pipeline := &Pipeline{}
	lastToolName := ""
	consecutiveToolCalls := 0
	for range maxConsecutiveToolCalls {
		if err := pipeline.trackConsecutiveToolCall(context.Background(), 1, &lastToolName, &consecutiveToolCalls, "search"); err != nil {
			t.Fatalf("expected call within limit to succeed: %v", err)
		}
	}
	if err := pipeline.trackConsecutiveToolCall(context.Background(), 1, &lastToolName, &consecutiveToolCalls, "search"); err == nil {
		t.Fatal("expected repeated call beyond the limit to be rejected")
	}
}

func TestTrackConsecutiveToolCall_AllowsUnlimitedAlternatingTools(t *testing.T) {
	pipeline := &Pipeline{}
	lastToolName := ""
	consecutiveToolCalls := 0
	for range maxConsecutiveToolCalls * 2 {
		if err := pipeline.trackConsecutiveToolCall(context.Background(), 1, &lastToolName, &consecutiveToolCalls, "search"); err != nil {
			t.Fatalf("expected alternating call to succeed: %v", err)
		}
		if err := pipeline.trackConsecutiveToolCall(context.Background(), 1, &lastToolName, &consecutiveToolCalls, "read"); err != nil {
			t.Fatalf("expected alternating call to succeed: %v", err)
		}
	}
}

func TestTrackConsecutiveToolCall_ApprovalResetsCounter(t *testing.T) {
	manager := interaction.NewManager()
	pipeline := &Pipeline{interactions: manager}
	events := make(chan *interaction.Request, 1)
	ctx := withInteractionReporter(context.Background(), func(request *interaction.Request) {
		events <- request
	})
	lastToolName := "shell"
	consecutiveToolCalls := maxConsecutiveToolCalls
	done := make(chan error, 1)

	go func() {
		done <- pipeline.trackConsecutiveToolCall(ctx, 7, &lastToolName, &consecutiveToolCalls, "shell")
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
	for range maxConsecutiveToolCalls - 1 {
		if err := pipeline.trackConsecutiveToolCall(ctx, 7, &lastToolName, &consecutiveToolCalls, "shell"); err != nil {
			t.Fatalf("expected fresh limit window after approval: %v", err)
		}
	}
}

func TestStreamToolCall_JSONIncludesStableIdentityAndStatus(t *testing.T) {
	toolCall := StreamToolCall{
		CallIndex: 2,
		Index:     1,
		ID:        "call-123",
		Name:      "shell",
		Arguments: `{}`,
		Status:    "calling",
	}

	data, err := json.Marshal(toolCall)
	if err != nil {
		t.Fatalf("marshal stream tool call: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal stream tool call: %v", err)
	}
	if got["call_index"] != float64(2) || got["index"] != float64(1) {
		t.Fatalf("expected stable call identity, got %s", data)
	}
	if got["name"] != "shell" || got["status"] != "calling" {
		t.Fatalf("expected tool name and status, got %s", data)
	}
}

func TestIncompleteMessages_AppendsPartialResponseAndMarksItIncomplete(t *testing.T) {
	partialErr := &llm.IncompleteResponseError{
		Message: ds4.Message{Role: ds4.RoleAssistant, Content: "partial reply"},
		Err:     errors.New("stream stopped"),
	}

	messages := incompleteMessages([]store.ChatMessage{{Role: ds4.RoleAssistant, Content: "tool request", Status: store.MessageStatusComplete}}, partialErr)
	if len(messages) != 2 {
		t.Fatalf("expected prior and partial assistant messages, got %+v", messages)
	}
	if messages[0].Status != store.MessageStatusComplete {
		t.Fatalf("expected prior message to stay complete, got %q", messages[0].Status)
	}
	if messages[1].Content != "partial reply" || messages[1].Status != store.MessageStatusIncomplete {
		t.Fatalf("expected incomplete partial response, got %+v", messages[1])
	}
}

func TestIncompleteMessages_StripsDanglingToolCallsFromInterruptedAssistantMessage(t *testing.T) {
	// Simulates the trackConsecutiveToolCall/limit failure path: the
	// assistant message requesting tool calls is already in toPersist, but
	// no matching tool-result messages were appended before the error.
	messages := []store.ChatMessage{
		{Role: ds4.RoleAssistant, Content: "visible reply", ReasoningContent: "reasoning", ToolCalls: []byte(`[{"id":"call-1"}]`), Status: store.MessageStatusComplete},
	}

	got := incompleteMessages(messages, errors.New("too many consecutive tool calls"))
	if len(got) != 1 {
		t.Fatalf("expected converse's already-collected messages to survive, got %+v", got)
	}
	if got[0].Status != store.MessageStatusIncomplete {
		t.Fatalf("expected message marked incomplete, got %q", got[0].Status)
	}
	if got[0].ToolCalls != nil {
		t.Fatalf("expected dangling tool_calls to be dropped, got %s", got[0].ToolCalls)
	}
	if got[0].Content != "visible reply" || got[0].ReasoningContent != "reasoning" {
		t.Fatalf("expected assistant content to survive, got %+v", got[0])
	}
}

func TestMaybeCompress_SkipsWhenContextWindowUnset(t *testing.T) {
	// A missing context window must not fire compression on every turn.
	pipeline := &Pipeline{}
	turn := plugin.TurnContext{
		SessionID: 1,
		Messages: []store.ChatMessage{
			{Role: ds4.RoleAssistant, Content: strings.Repeat("x", 10_000)},
			{Role: ds4.RoleUser, Content: "hello"},
		},
		Metadata: map[string]any{},
	}
	activePath := []store.ChatMessage{{ID: 1, Role: ds4.RoleAssistant, Content: strings.Repeat("x", 10_000)}}

	out, err := pipeline.maybeCompress(context.Background(), store.Session{ID: 1}, registry.Identity{ContextWindowTokens: 0}, activePath, nil, turn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Messages) != len(turn.Messages) {
		t.Fatalf("expected messages untouched when context window is unset, got %d", len(out.Messages))
	}
}

func TestMaybeCompress_DoesNotFireBelowThreshold(t *testing.T) {
	pipeline := &Pipeline{}
	turn := plugin.TurnContext{
		SessionID: 1,
		Messages:  []store.ChatMessage{{Role: ds4.RoleUser, Content: "short"}},
		Metadata:  map[string]any{},
	}
	activePath := []store.ChatMessage{{ID: 1, Role: ds4.RoleAssistant, Content: "hi"}}

	out, err := pipeline.maybeCompress(context.Background(), store.Session{ID: 1}, registry.Identity{ContextWindowTokens: 1000}, activePath, nil, turn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != "short" {
		t.Fatalf("expected messages untouched below threshold, got %+v", out.Messages)
	}
}

func TestEstimateMessageLengthCountsReasoningAndToolCalls(t *testing.T) {
	m := store.ChatMessage{
		Content:          "aaaa",
		ReasoningContent: "aaaa",
		ToolCalls:        []byte(`{"x":"aaaa"}`),
	}
	want := transform.EstimateLength(m.Content) + transform.EstimateLength(m.ReasoningContent) + transform.EstimateLength(string(m.ToolCalls))
	if got := estimateMessageLength(m); got != want {
		t.Fatalf("estimateMessageLength() = %d, want %d", got, want)
	}
	if transform.EstimateLength(m.ReasoningContent) == 0 || transform.EstimateLength(string(m.ToolCalls)) == 0 {
		t.Fatal("test requires non-zero reasoning/tool-call estimates to be meaningful")
	}
}

func TestResolveSettings_ValidSettingsUntouched(t *testing.T) {
	reg := registry.NewStore(&registry.Registry{
		Identities:  map[string]registry.Identity{"default": {Name: "default"}},
		Impressions: map[string]registry.Impression{"imp": {Name: "imp"}},
		ToolGroups:  map[string]registry.ToolGroup{"tg": {Name: "tg"}},
	})
	pipeline := &Pipeline{registries: reg}
	sess := &store.Session{
		Settings: datatypes.NewJSONType(store.SessionSettings{
			Identity:    "default",
			Impressions: []string{"imp"},
			ToolGroups:  []string{"tg"},
			Plugins:     []string{},
		}),
	}

	got, err := pipeline.resolveSettings(sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Identity != "default" || len(got.Impressions) != 1 || len(got.ToolGroups) != 1 {
		t.Fatalf("expected valid settings untouched, got %+v", got)
	}
	if sess.Settings.Data().Identity != "default" {
		t.Fatalf("expected no persistence for valid settings, got %+v", sess.Settings.Data())
	}
}

func TestKeepRegisteredDropsUnknownNames(t *testing.T) {
	dirty := false
	got := keepRegistered([]string{"known", "missing", "known2"}, map[string]struct{}{
		"known":  {},
		"known2": {},
	}, &dirty)
	if !dirty {
		t.Fatal("expected dirty to be set when a name is dropped")
	}
	if len(got) != 2 || got[0] != "known" || got[1] != "known2" {
		t.Fatalf("expected only known names kept in order, got %v", got)
	}
}

func TestShellAndCreateProjectAreAudited(t *testing.T) {
	shell := namedTool{name: "shell"}
	if _, ok := any(shell).(toolkit.Audited); ok {
		t.Fatal("namedTool must not be audited; this test relies on real tools")
	}
	// The capability is exercised through the real implementations in
	// internal/tools; here we only assert the pipeline rejects non-audited
	// tools without recording. beginToolAudit with a non-audited tool must
	// return 0.
	pipeline := &Pipeline{}
	if id := pipeline.beginToolAudit(1, shell, ds4.ToolCall{}, map[string]any{}); id != 0 {
		t.Fatalf("expected non-audited tool to skip audit, got audit id %d", id)
	}
}
