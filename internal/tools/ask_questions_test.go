package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

func TestAskQuestionsToolReturnsAnswers(t *testing.T) {
	manager := interaction.NewManager()
	tool := NewAskQuestionsTool(manager)
	events := make(chan interaction.Event, 1)
	ctx := toolkit.WithSessionID(interaction.WithReporter(context.Background(), func(event interaction.Event) { events <- event }), 7)
	result := make(chan *toolkit.ToolResult, 1)
	go func() {
		result <- tool.Execute(ctx, map[string]any{"questions": []any{map[string]any{
			"id": "language", "prompt": "Choose", "options": []any{
				map[string]any{"id": "go", "title": "Go", "description": "Compiled"},
				map[string]any{"id": "ts", "title": "TypeScript", "description": "Typed"},
			},
		}}})
	}()
	event := <-events
	if err := manager.RespondQuestions(7, event.Request.ID, []interaction.Answer{{QuestionID: "language", SelectedOptionIDs: []string{"go"}}}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	select {
	case output := <-result:
		if output.IsError || !strings.Contains(output.ForLLM, `"selected_option_ids":["go"]`) {
			t.Fatalf("unexpected result: %+v", output)
		}
	case <-time.After(time.Second):
		t.Fatal("tool did not return")
	}
}

func TestAskQuestionsToolRejectsInvisibleContext(t *testing.T) {
	tool := NewAskQuestionsTool(interaction.NewManager())
	result := tool.Execute(toolkit.WithSessionID(context.Background(), 7), map[string]any{"questions": []any{}})
	if !result.IsError || !strings.Contains(result.ForLLM, "streaming Web") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAskQuestionsToolIsSessionScoped(t *testing.T) {
	if !toolkit.ScopeAllows(NewAskQuestionsTool(interaction.NewManager()), toolkit.ScopeSession) || toolkit.ScopeAllows(NewAskQuestionsTool(interaction.NewManager()), toolkit.ScopeWorkflow) {
		t.Fatal("ask_questions should be session scoped")
	}
}
