package builtin

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/gorm"
)

// storylineSuffixPattern strips a "[STATE] ..." suffix the model may have
// self-generated, so the plugin's own authoritative suffix always wins.
var storylineSuffixPattern = regexp.MustCompile(`(?s)\n?\[STATE\].*$`)

// StorylineStatusPlugin maintains a compact, readable status line (e.g.
// "HP: 100, MP: 57, quest 5/20") appended to every assistant reply, kept
// authoritative in PluginState and never writable by the Agent itself.
type StorylineStatusPlugin struct {
	llm *llm.Client
	db  *gorm.DB
}

// NewStorylineStatusPlugin creates the plugin.
func NewStorylineStatusPlugin(db *gorm.DB, llmClient *llm.Client) *StorylineStatusPlugin {
	return &StorylineStatusPlugin{db: db, llm: llmClient}
}

func (p *StorylineStatusPlugin) Name() string           { return "storyline_status" }
func (p *StorylineStatusPlugin) Timeout() time.Duration { return 20 * time.Second }

type storylineState struct {
	StatusLine string `json:"status_line"`
}

func (p *StorylineStatusPlugin) Handle(ctx context.Context, hook plugin.Hook, phase plugin.Phase, turn plugin.TurnContext) (plugin.TurnContext, error) {
	if hook != plugin.HookAssistantMessageCompletion || phase != plugin.PhaseAfter || len(turn.Messages) == 0 {
		return turn, nil
	}

	lastIdx := len(turn.Messages) - 1
	last := turn.Messages[lastIdx]
	cleaned := storylineSuffixPattern.ReplaceAllString(last.Content, "")

	var state storylineState
	if _, err := store.LoadPluginState(p.db, turn.SessionID, p.Name(), &state); err != nil {
		return turn, err
	}

	prompt := fmt.Sprintf(
		"Previous status: %s\n\nLatest assistant reply:\n%s\n\n"+
			"Respond with ONLY the updated, one-line status. No other text.",
		state.StatusLine, cleaned,
	)
	newStatus, err := transform.StorylineStatus(ctx, p.llm, prompt, 128)
	if err != nil {
		return turn, fmt.Errorf("storyline_status: %w", err)
	}
	newStatus = strings.TrimSpace(newStatus)

	if err := store.SavePluginState(p.db, turn.SessionID, p.Name(), storylineState{StatusLine: newStatus}); err != nil {
		return turn, fmt.Errorf("storyline_status: save state: %w", err)
	}

	last.Content = strings.TrimRight(cleaned, "\n") + "\n[STATE] " + newStatus
	turn.Messages[lastIdx] = last
	return turn, nil
}
