package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
)

func TestMetaphysicsPluginInsertsRichDataBeforeUserMessage(t *testing.T) {
	now := func() time.Time { return time.Date(2024, time.February, 10, 12, 0, 0, 0, time.UTC) }
	metaphysicsPlugin := NewMetaphysicsPlugin(MetaphysicsConfig{Timezone: "Asia/Shanghai", Now: now})
	turn := plugin.TurnContext{IsFirstTurn: true, Messages: []store.ChatMessage{{Role: "user", Content: "你好"}}}

	got, err := metaphysicsPlugin.Handle(context.Background(), plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != ds4.RoleSystem || !strings.Contains(got.Messages[0].Content, "农历:") || !strings.Contains(got.Messages[0].Content, "[奇门遁甲 snapshot begin]") {
		t.Fatalf("unexpected metaphysics message: %#v", got.Messages[0])
	}
	if strings.Contains(got.Messages[0].Content, "Time:") || strings.Contains(got.Messages[0].Content, "Weather:") {
		t.Fatalf("metaphysics message contains environment data: %q", got.Messages[0].Content)
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "你好" {
		t.Fatalf("unexpected user message: %#v", got.Messages[1])
	}
}
