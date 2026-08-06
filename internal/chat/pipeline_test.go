package chat

import (
	"context"
	"testing"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/tools"
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
	_, err := pipeline.executeTool(context.Background(), 1, map[string]tools.Tool{}, ds4.ToolCall{
		Function: ds4.FunctionCall{Name: "echo"},
	})
	if err == nil {
		t.Fatal("expected disabled tool to be rejected")
	}
}
