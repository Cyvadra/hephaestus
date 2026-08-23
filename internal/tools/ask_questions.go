package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// AskQuestionsTool lets a streaming session ask the user for information the
// model cannot safely infer.
type AskQuestionsTool struct{ interactions *interaction.Manager }

func NewAskQuestionsTool(interactions *interaction.Manager) *AskQuestionsTool {
	return &AskQuestionsTool{interactions: interactions}
}

func (AskQuestionsTool) Name() string { return "ask_questions" }
func (AskQuestionsTool) Description() string {
	return "Asks the user one to five focused questions when essential information is missing. Each question offers two to five choices and always permits custom text. Use only when the answer materially changes the next action; do not ask for information already available in the conversation."
}
func (AskQuestionsTool) Scopes() []toolkit.Scope { return []toolkit.Scope{toolkit.ScopeSession} }
func (AskQuestionsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"questions": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 5,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{
						"id":           map[string]any{"type": "string"},
						"prompt":       map[string]any{"type": "string"},
						"multi_select": map[string]any{"type": "boolean"},
						"options": map[string]any{
							"type": "array", "minItems": 2, "maxItems": 5,
							"items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
								"id":          map[string]any{"type": "string"},
								"title":       map[string]any{"type": "string"},
								"description": map[string]any{"type": "string"},
							}, "required": []string{"id", "title", "description"}},
						},
					}, "required": []string{"id", "prompt", "options"},
				},
			},
		}, "required": []string{"questions"},
	}
}

func (t *AskQuestionsTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	if t.interactions == nil {
		return toolkit.ErrorResult("ask_questions: interactions are not configured")
	}
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok || sessionID == 0 {
		return toolkit.ErrorResult("ask_questions: requires a streaming session")
	}
	if !interaction.HasReporter(ctx) {
		return toolkit.ErrorResult("ask_questions: requires the streaming Web chat endpoint")
	}
	questions, err := decodeQuestions(args["questions"])
	if err != nil {
		return toolkit.ErrorResult("ask_questions: " + err.Error())
	}
	answers, err := t.interactions.RequestQuestions(ctx, sessionID, questions)
	if err != nil {
		return toolkit.ErrorResult("ask_questions: " + err.Error())
	}
	encoded, err := json.Marshal(struct {
		Answers []interaction.Answer `json:"answers"`
	}{Answers: answers})
	if err != nil {
		return toolkit.ErrorResult("ask_questions: encode answers: " + err.Error())
	}
	return toolkit.NewToolResult(string(encoded))
}

func decodeQuestions(value any) ([]interaction.Question, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("questions must be an array")
	}
	var questions []interaction.Question
	if err := json.Unmarshal(encoded, &questions); err != nil {
		return nil, fmt.Errorf("questions must be an array of question objects")
	}
	for questionIndex := range questions {
		question := &questions[questionIndex]
		question.ID = strings.TrimSpace(question.ID)
		question.Prompt = strings.TrimSpace(question.Prompt)
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			option.ID = strings.TrimSpace(option.ID)
			option.Title = strings.TrimSpace(option.Title)
			option.Description = strings.TrimSpace(option.Description)
		}
	}
	return questions, nil
}
