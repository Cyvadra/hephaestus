package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/liuzl/gocc"
)

// TraditionalCCPlugin converts the first user message from Simplified to
// Traditional Chinese before it is sent to the LLM or persisted.
type TraditionalCCPlugin struct {
	converter *gocc.OpenCC
}

func NewTraditionalCCPlugin() (*TraditionalCCPlugin, error) {
	converter, err := gocc.New("s2t")
	if err != nil {
		return nil, fmt.Errorf("traditional-cc: create converter: %w", err)
	}
	return &TraditionalCCPlugin{converter: converter}, nil
}

func (p *TraditionalCCPlugin) Name() string { return "traditional-cc" }

func (p *TraditionalCCPlugin) Description() string {
	return "将首条用户消息的简体中文转换为繁体中文。"
}

func (p *TraditionalCCPlugin) Timeout() time.Duration { return time.Second }

func (p *TraditionalCCPlugin) Handle(_ context.Context, hook plugin.Hook, phase plugin.Phase, turn plugin.TurnContext) (plugin.TurnContext, error) {
	if hook != plugin.HookUserMessageIncoming || phase != plugin.PhaseAfter || !turn.IsFirstTurn {
		return turn, nil
	}
	for index := len(turn.Messages) - 1; index >= 0; index-- {
		if turn.Messages[index].Role != ds4.RoleUser {
			continue
		}
		converted, err := p.converter.Convert(turn.Messages[index].Content)
		if err != nil {
			return turn, fmt.Errorf("traditional-cc: convert message: %w", err)
		}
		turn.Messages[index].Content = converted
		return turn, nil
	}
	return turn, nil
}
