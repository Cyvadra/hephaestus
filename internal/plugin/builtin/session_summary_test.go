package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
)

func TestSessionSummaryPlugin_IgnoresAssistantMessageHook(t *testing.T) {
	p := &SessionSummaryPlugin{}
	turn := plugin.TurnContext{SessionID: 1}

	got, err := p.Handle(context.Background(), plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.SessionID != turn.SessionID {
		t.Fatalf("expected unchanged turn, got %+v", got)
	}
}

func TestSessionSummaryPrompt_FirstTurnUsesFirstUserMessageForTitle(t *testing.T) {
	p := &SessionSummaryPlugin{maxInput: 4000}
	turn := plugin.TurnContext{
		IsFirstTurn:      true,
		FirstUserMessage: "Help me design a backup plan",
		Messages: []store.ChatMessage{
			{Role: "user", Content: "Help me design a backup plan"},
			{Role: "assistant", Content: "Let's start with recovery targets."},
		},
	}

	prompt := p.prompt(turn)
	if !strings.Contains(prompt, "title based only on the first user message") {
		t.Fatalf("expected first-turn title constraint, got %q", prompt)
	}
	if !strings.Contains(prompt, turn.FirstUserMessage) {
		t.Fatalf("expected first user message in prompt, got %q", prompt)
	}
}

func TestSessionSummaryPrompt_LaterTurnUsesConversation(t *testing.T) {
	p := &SessionSummaryPlugin{maxInput: 4000}
	prompt := p.prompt(plugin.TurnContext{Messages: []store.ChatMessage{{Role: "user", Content: "latest topic"}}})
	if strings.Contains(prompt, "title based only on the first user message") {
		t.Fatalf("did not expect first-turn title constraint, got %q", prompt)
	}
}
