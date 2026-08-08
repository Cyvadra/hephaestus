package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/transform"
)

// OptionsPlugin suggests short next-user-message alternatives after every
// assistant reply, surfaced via TurnContext.Metadata["suggested_replies"].
type OptionsPlugin struct {
	llm *llm.Client
}

// NewOptionsPlugin creates the plugin.
func NewOptionsPlugin(llmClient *llm.Client) *OptionsPlugin {
	return &OptionsPlugin{llm: llmClient}
}

func (p *OptionsPlugin) Name() string           { return "options" }
func (p *OptionsPlugin) Timeout() time.Duration { return 15 * time.Second }

func (p *OptionsPlugin) Handle(ctx context.Context, hook plugin.Hook, phase plugin.Phase, turn plugin.TurnContext) (plugin.TurnContext, error) {
	if hook != plugin.HookAssistantMessageCompletion || phase != plugin.PhaseAfter || len(turn.Messages) == 0 {
		return turn, nil
	}

	last := turn.Messages[len(turn.Messages)-1]
	prompt := fmt.Sprintf(
		"Assistant just replied:\n%s\n\nSuggest 3 short, distinct things the "+
			"user might say next. Respond with ONLY a JSON array of strings.",
		last.Content,
	)
	result, err := transform.SuggestOptions(ctx, p.llm, prompt, 200)
	if err != nil {
		return turn, fmt.Errorf("options: %w", err)
	}

	var options []string
	if err := json.Unmarshal([]byte(result), &options); err != nil {
		return turn, fmt.Errorf("options: parse suggestions: %w", err)
	}

	turn.Metadata["suggested_replies"] = options
	return turn, nil
}
