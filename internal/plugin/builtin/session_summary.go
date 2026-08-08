// Package builtin implements the platform's example Plugins from the
// design doc: session summarization, storyline status tracking, and
// next-message option suggestions. Each is registered explicitly in main.go
// (no auto-discovery).
package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/gorm"
)

// SessionSummaryPlugin generates the initial Title from the session's first
// user message, then periodically refreshes its Title (<=20 chars) and Summary
// (<=300 chars) via a minor-model side call.
type SessionSummaryPlugin struct {
	db       *gorm.DB
	llm      *llm.Client
	minGap   time.Duration
	maxInput int
}

// NewSessionSummaryPlugin creates the plugin; it re-summarizes at most once
// per minGap of session activity to avoid a side LLM call on every turn.
func NewSessionSummaryPlugin(db *gorm.DB, llmClient *llm.Client, minGap time.Duration) *SessionSummaryPlugin {
	return &SessionSummaryPlugin{db: db, llm: llmClient, minGap: minGap, maxInput: 4000}
}

func (p *SessionSummaryPlugin) Name() string           { return "session_summary" }
func (p *SessionSummaryPlugin) Timeout() time.Duration { return 20 * time.Second }

type sessionSummaryState struct {
	LastSummarizedAt time.Time `json:"last_summarized_at"`
}

func (p *SessionSummaryPlugin) Handle(ctx context.Context, hook plugin.Hook, phase plugin.Phase, turn plugin.TurnContext) (plugin.TurnContext, error) {
	if hook != plugin.HookSessionSummaryRequested || phase != plugin.PhaseAfter {
		return turn, nil
	}

	var state sessionSummaryState
	found, err := store.LoadPluginState(p.db, turn.SessionID, p.Name(), &state)
	if err != nil {
		return turn, err
	}
	if found && time.Since(state.LastSummarizedAt) < p.minGap {
		return turn, nil
	}

	prompt := p.prompt(turn)
	result, err := transform.SessionTitleSummary(ctx, p.llm, prompt, 256)
	if err != nil {
		return turn, fmt.Errorf("session_summary: %w", err)
	}

	title, summary := splitTitleSummary(result)
	if err := p.db.Model(&store.Session{}).Where("id = ?", turn.SessionID).
		Updates(map[string]any{"title": title, "summary": summary}).Error; err != nil {
		return turn, fmt.Errorf("session_summary: save title/summary: %w", err)
	}

	if err := store.SavePluginState(p.db, turn.SessionID, p.Name(), sessionSummaryState{LastSummarizedAt: time.Now()}); err != nil {
		return turn, fmt.Errorf("session_summary: save state: %w", err)
	}
	turn.Metadata["session_summary_updated"] = true

	return turn, nil
}

func (p *SessionSummaryPlugin) prompt(turn plugin.TurnContext) string {
	transcript := renderTranscript(turn.Messages, p.maxInput)
	if turn.IsFirstTurn {
		return fmt.Sprintf(
			"First user message:\n%s\n\nConversation so far:\n%s\n\nRespond with exactly two lines: "+
				"a title based only on the first user message (max 20 characters), then a summary "+
				"of the conversation (max 300 characters). No other text.",
			turn.FirstUserMessage,
			transcript,
		)
	}
	return fmt.Sprintf(
		"Conversation so far:\n%s\n\nRespond with exactly two lines: a title "+
			"(max 20 characters) then a summary (max 300 characters). No other text.",
		transcript,
	)
}
