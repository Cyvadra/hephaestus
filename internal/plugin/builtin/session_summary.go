// Package builtin implements the platform's example Plugins from the
// design doc: session summarization, storyline status tracking, and
// next-message option suggestions. Each is registered explicitly in main.go
// (no auto-discovery).
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/gorm"
)

// SessionSummaryPlugin generates the initial Title from the session's first
// user message, then periodically refreshes its Title (<=20 chars) and Summary
// (<=300 chars) via a minor-model side call.
type SessionSummaryPlugin struct {
	db     *gorm.DB
	llm    *llm.Client
	minGap time.Duration
}

// NewSessionSummaryPlugin creates the plugin; it re-summarizes at most once
// per minGap of session activity to avoid a side LLM call on every turn.
func NewSessionSummaryPlugin(db *gorm.DB, llmClient *llm.Client, minGap time.Duration) *SessionSummaryPlugin {
	return &SessionSummaryPlugin{db: db, llm: llmClient, minGap: minGap}
}

func (p *SessionSummaryPlugin) Name() string { return "session_summary" }
func (p *SessionSummaryPlugin) Description() string {
	return "自动生成并定期更新会话标题和摘要。"
}
func (p *SessionSummaryPlugin) Timeout() time.Duration { return 20 * time.Second }

// Scopes restricts this plugin to interactive sessions: session titles and
// summaries have no meaning for headless workflow runs.
func (p *SessionSummaryPlugin) Scopes() []toolkit.Scope {
	return []toolkit.Scope{toolkit.ScopeSession}
}

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

	messages := append([]store.ChatMessage(nil), turn.Messages...)
	messages = append(messages, store.ChatMessage{Role: ds4.RoleUser, Content: sessionSummaryInstruction})
	result, err := p.llm.CallWithoutThinking(ctx, turn.Identity, messages)
	if err != nil {
		return turn, fmt.Errorf("session_summary: %w", err)
	}

	title, summary, err := parseSessionSummary(result)
	if err != nil {
		return turn, fmt.Errorf("session_summary: parse response: %w", err)
	}
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

const sessionSummaryInstruction = `请为当前会话的正文（不包含背景设定）生成标题和内容简述。标题不超过 20 个字，简述不超过 200 个字，简洁即可。只返回合法 JSON： {"session":{"title":"xxx","summary":"xxx"}}`

type sessionSummaryResponse struct {
	Session struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"session"`
}

func parseSessionSummary(result string) (string, string, error) {
	start := strings.Index(result, "{")
	end := strings.LastIndex(result, "}")
	if start < 0 || end < start {
		return "", "", fmt.Errorf("JSON object not found")
	}

	var response sessionSummaryResponse
	if err := json.Unmarshal([]byte(result[start:end+1]), &response); err != nil {
		return "", "", err
	}
	title := clampRunes(strings.TrimSpace(response.Session.Title), 28)
	summary := clampRunes(strings.TrimSpace(response.Session.Summary), 300)
	if title == "" || summary == "" {
		return "", "", fmt.Errorf("title or summary is empty")
	}
	return title, summary, nil
}
