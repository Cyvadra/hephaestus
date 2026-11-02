package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
)

// A compression boundary can land on the user message a regenerated reply
// answers, or on a message after it. Regenerate must clamp the checkpoint so
// the raw prompt is replayed instead of being swallowed by the coverage
// filter, while keeping the checkpoint as prior context.
func TestCompressionForRegenerateKeepsCoveredPrompt(t *testing.T) {
	pipeline, _, _, _ := compressionFixture(t, "unused")
	summary := datatypes.JSON(`[{"role":"user","content":"<compacted-summary>old history</compacted-summary>"}]`)
	path := []store.ChatMessage{
		{ID: 1, Role: ds4.RoleUser, Content: "first"},
		{ID: 2, Role: ds4.RoleAssistant, Content: "reply"},
		{ID: 3, Role: ds4.RoleUser, Content: "REGENERATED PROMPT"},
	}

	for _, boundary := range []uint{3, 4} {
		row := &store.Compression{ID: 1, FirstMessageID: 1, LastMessageID: boundary, Messages: summary}
		clamped := compressionForRegenerate(row, path)
		if row.LastMessageID != boundary {
			t.Fatalf("boundary %d: compressionForRegenerate mutated the source row", boundary)
		}
		if clamped == nil || clamped.LastMessageID != 2 {
			t.Fatalf("boundary %d: clamped row = %+v, want LastMessageID 2", boundary, clamped)
		}

		_, contextMessages, err := pipeline.buildContextParts(&registry.Registry{}, store.SessionSettings{}, path, clamped)
		if err != nil {
			t.Fatal(err)
		}
		if len(contextMessages) != 2 {
			t.Fatalf("boundary %d: context = %+v, want checkpoint + prompt", boundary, contextMessages)
		}
		if !strings.Contains(contextMessages[0].Content, "old history") {
			t.Fatalf("boundary %d: checkpoint not retained: %+v", boundary, contextMessages[0])
		}
		if last := contextMessages[len(contextMessages)-1]; last.Content != "REGENERATED PROMPT" {
			t.Fatalf("boundary %d: context ends with %+v, want the regenerated prompt", boundary, last)
		}
	}
}

// When the prompt is the first message on the path there is no predecessor to
// clamp to; the boundary must drop to zero so the prompt is still replayed.
func TestCompressionForRegenerateHandlesPromptAtPathStart(t *testing.T) {
	pipeline, _, _, _ := compressionFixture(t, "unused")
	summary := datatypes.JSON(`[{"role":"user","content":"<compacted-summary>old history</compacted-summary>"}]`)
	path := []store.ChatMessage{{ID: 1, Role: ds4.RoleUser, Content: "REGENERATED PROMPT"}}
	row := &store.Compression{ID: 1, FirstMessageID: 1, LastMessageID: 1, Messages: summary}

	clamped := compressionForRegenerate(row, path)
	if clamped == nil || clamped.LastMessageID != 0 {
		t.Fatalf("clamped row = %+v, want LastMessageID 0", clamped)
	}
	_, contextMessages, err := pipeline.buildContextParts(&registry.Registry{}, store.SessionSettings{}, path, clamped)
	if err != nil {
		t.Fatal(err)
	}
	if len(contextMessages) != 2 || contextMessages[1].Content != "REGENERATED PROMPT" {
		t.Fatalf("context = %+v, want checkpoint + prompt", contextMessages)
	}
}

// A boundary before the prompt is the normal case and must pass through.
func TestCompressionForRegenerateLeavesEarlierBoundaryAlone(t *testing.T) {
	row := &store.Compression{ID: 1, LastMessageID: 1}
	path := []store.ChatMessage{{ID: 1}, {ID: 2}, {ID: 3}}
	if got := compressionForRegenerate(row, path); got != row {
		t.Fatalf("compressionForRegenerate() = %+v, want the original row", got)
	}
	if got := compressionForRegenerate(nil, path); got != nil {
		t.Fatalf("compressionForRegenerate(nil) = %+v, want nil", got)
	}
}

// Reasoning content is replayed to the model, so it counts toward the context
// that a checkpoint frees. Without it in the shrink baseline, a reasoning-heavy
// span can never produce an accepted checkpoint.
func TestCompactHistory_ReasoningHeavyHistoryCompacts(t *testing.T) {
	pipeline, _, sess, _ := compressionFixture(t, strings.Repeat("s", 500))
	content := strings.Repeat("c", 200)    // 100 estimated units
	reasoning := strings.Repeat("r", 4000) // 2000 estimated units
	roles := []string{ds4.RoleUser, ds4.RoleAssistant, ds4.RoleUser, ds4.RoleAssistant}
	activePath := make([]store.ChatMessage, len(roles))
	for i, role := range roles {
		activePath[i] = store.ChatMessage{
			ID:               uint(i + 1),
			SessionID:        sess.ID,
			Role:             role,
			Content:          content,
			ReasoningContent: reasoning,
		}
	}

	result, err := pipeline.compactHistory(context.Background(), sess, registry.Identity{ContextWindowTokens: 100000}, activePath, nil, 8400, compactionManual)
	if err != nil {
		t.Fatalf("compactHistory: %v", err)
	}
	if !result.Compacted {
		t.Fatalf("reasoning-heavy history did not compact: %s", result.Reason)
	}
}
