package subagentexec

import (
	"encoding/json"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
)

func TestForkSeedPairsOpenToolCalls(t *testing.T) {
	calls, _ := json.Marshal([]map[string]any{{"id": "fork-1"}, {"id": "spawn-2"}})
	seed, err := ForkSeed([]store.ChatMessage{{Role: "user", Content: "research"}, {Role: "assistant", ToolCalls: datatypes.JSON(calls)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) != 4 {
		t.Fatalf("seed length = %d, want 4", len(seed))
	}
	if seed[2].ToolCallID != "fork-1" || seed[3].ToolCallID != "spawn-2" {
		t.Fatalf("unexpected synthetic results: %+v", seed[2:])
	}
	if seed[2].Role != "tool" || seed[2].Content == "" {
		t.Fatalf("invalid synthetic result: %+v", seed[2])
	}
}

func TestForkSeedLeavesBalancedHistoryAlone(t *testing.T) {
	original := []store.ChatMessage{{Role: "user", Content: "hello"}}
	seed, err := ForkSeed(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) != 1 || seed[0].Content != "hello" {
		t.Fatalf("seed = %+v", seed)
	}
}

func TestChildPromptParentIDUsesLastPersistedSeed(t *testing.T) {
	seed := []store.ChatMessage{{ID: 10}, {ID: 11}}
	parentID := childPromptParentID(seed)
	if parentID == nil || *parentID != 11 {
		t.Fatalf("prompt parent id = %v, want 11", parentID)
	}
	if parentID := childPromptParentID(nil); parentID != nil {
		t.Fatalf("empty seed parent id = %v, want nil", parentID)
	}
}
