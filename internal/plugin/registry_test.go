package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/store"
)

// stubPlugin is a test Plugin whose Handle behavior is supplied by the test.
type stubPlugin struct {
	name    string
	timeout time.Duration
	handle  func(ctx context.Context, hook Hook, phase Phase, turn TurnContext) (TurnContext, error)
}

func (p stubPlugin) Name() string           { return p.name }
func (p stubPlugin) Timeout() time.Duration { return p.timeout }
func (p stubPlugin) Handle(ctx context.Context, hook Hook, phase Phase, turn TurnContext) (TurnContext, error) {
	return p.handle(ctx, hook, phase, turn)
}

// A plugin that mutates the TurnContext it was handed and then fails must
// not leave any trace in the caller's turn: Registry.Run must have given it
// a private clone, not the shared Messages/Metadata.
func TestRegistry_Run_FailingPluginDoesNotCorruptCallerState(t *testing.T) {
	reg := NewRegistry(notify.New(""))
	reg.Register(stubPlugin{
		name:    "corrupt-then-fail",
		timeout: time.Second,
		handle: func(_ context.Context, _ Hook, _ Phase, turn TurnContext) (TurnContext, error) {
			turn.Messages[0].Content = "corrupted"
			turn.Metadata["poison"] = true
			return TurnContext{}, fmt.Errorf("boom")
		},
	})

	original := TurnContext{
		SessionID: 1,
		Messages:  []store.ChatMessage{{Content: "original"}},
		Metadata:  map[string]any{},
	}

	out := reg.Run(context.Background(), []string{"corrupt-then-fail"}, HookUserMessageIncoming, PhaseAfter, original)

	if out.Messages[0].Content != "original" {
		t.Fatalf("expected caller's message untouched, got %q", out.Messages[0].Content)
	}
	if _, poisoned := out.Metadata["poison"]; poisoned {
		t.Fatalf("expected caller's metadata untouched, got %v", out.Metadata)
	}
}

// A plugin that ignores context cancellation must not stall the pipeline
// past its declared Timeout(); Registry.Run must move on regardless.
func TestRegistry_Run_EnforcesTimeoutAgainstNonCooperativePlugin(t *testing.T) {
	reg := NewRegistry(notify.New(""))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	reg.Register(stubPlugin{
		name:    "ignores-context",
		timeout: 20 * time.Millisecond,
		handle: func(_ context.Context, _ Hook, _ Phase, turn TurnContext) (TurnContext, error) {
			<-release // deliberately never returns before the test unblocks it
			return turn, nil
		},
	})

	original := TurnContext{SessionID: 1, Metadata: map[string]any{}}

	start := time.Now()
	out := reg.Run(context.Background(), []string{"ignores-context"}, HookUserMessageIncoming, PhaseAfter, original)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Run did not honor plugin timeout, took %v", elapsed)
	}
	if len(out.Messages) != 0 {
		t.Fatalf("expected turn unchanged when plugin times out, got %+v", out.Messages)
	}
}

// An unknown plugin name is warned about and skipped without affecting turn.
func TestRegistry_Run_UnknownPluginSkipped(t *testing.T) {
	reg := NewRegistry(notify.New(""))
	original := TurnContext{SessionID: 1, Metadata: map[string]any{}}
	out := reg.Run(context.Background(), []string{"does-not-exist"}, HookUserMessageIncoming, PhaseAfter, original)
	if len(out.Metadata) != 0 {
		t.Fatalf("expected unchanged metadata, got %v", out.Metadata)
	}
}

// A successful plugin's returned TurnContext is adopted for the next plugin
// in the chain.
func TestRegistry_Run_SuccessfulPluginIsAdopted(t *testing.T) {
	reg := NewRegistry(notify.New(""))
	reg.Register(stubPlugin{
		name:    "tag",
		timeout: time.Second,
		handle: func(_ context.Context, _ Hook, _ Phase, turn TurnContext) (TurnContext, error) {
			turn.Metadata["tagged"] = true
			return turn, nil
		},
	})

	original := TurnContext{SessionID: 1, Metadata: map[string]any{}}
	out := reg.Run(context.Background(), []string{"tag"}, HookUserMessageIncoming, PhaseAfter, original)
	if out.Metadata["tagged"] != true {
		t.Fatalf("expected metadata tagged, got %v", out.Metadata)
	}
}

func TestRegistry_Run_MergesFixedAndSessionPluginsWithoutDuplicates(t *testing.T) {
	reg := NewRegistry(notify.New(""))
	for _, name := range []string{"fixed", "session"} {
		pluginName := name
		reg.Register(stubPlugin{
			name:    pluginName,
			timeout: time.Second,
			handle: func(_ context.Context, _ Hook, _ Phase, turn TurnContext) (TurnContext, error) {
				order, _ := turn.Metadata["order"].([]string)
				turn.Metadata["order"] = append(order, pluginName)
				return turn, nil
			},
		})
	}
	if err := reg.SetFixedPlugins([]string{"fixed", "fixed"}); err != nil {
		t.Fatalf("SetFixedPlugins: %v", err)
	}

	out := reg.Run(context.Background(), []string{"session", "fixed"}, HookUserMessageIncoming, PhaseAfter, TurnContext{Metadata: map[string]any{}})
	order, _ := out.Metadata["order"].([]string)
	if fmt.Sprint(order) != "[fixed session]" {
		t.Fatalf("expected fixed plugin first with duplicates removed, got %v", order)
	}
}

func TestRegistry_SetFixedPlugins_RejectsUnknownPlugin(t *testing.T) {
	reg := NewRegistry(notify.New(""))
	if err := reg.SetFixedPlugins([]string{"missing"}); err == nil {
		t.Fatal("expected unknown fixed plugin to be rejected")
	}
}
