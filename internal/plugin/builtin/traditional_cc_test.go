package builtin

import (
	"context"
	"testing"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
)

func TestTraditionalCCPluginConvertsFirstUserMessage(t *testing.T) {
	traditionalCCPlugin, err := NewTraditionalCCPlugin()
	if err != nil {
		t.Fatalf("NewTraditionalCCPlugin: %v", err)
	}
	turn := plugin.TurnContext{
		IsFirstTurn: true,
		Messages: []store.ChatMessage{
			{Role: ds4.RoleSystem, Content: "context"},
			{Role: ds4.RoleUser, Content: "开发者在后台理发"},
		},
	}

	got, err := traditionalCCPlugin.Handle(context.Background(), plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.Messages[0].Content != "context" {
		t.Fatalf("system message = %q, want unchanged", got.Messages[0].Content)
	}
	if got.Messages[1].Content != "開發者在後臺理髮" {
		t.Fatalf("user message = %q, want %q", got.Messages[1].Content, "開發者在後臺理髮")
	}
}

func TestTraditionalCCPluginSkipsNonFirstTurn(t *testing.T) {
	traditionalCCPlugin, err := NewTraditionalCCPlugin()
	if err != nil {
		t.Fatalf("NewTraditionalCCPlugin: %v", err)
	}
	turn := plugin.TurnContext{
		Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "开发者在后台理发"}},
	}

	got, err := traditionalCCPlugin.Handle(context.Background(), plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.Messages[0].Content != "开发者在后台理发" {
		t.Fatalf("user message = %q, want unchanged", got.Messages[0].Content)
	}
}
