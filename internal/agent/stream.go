package agent

import (
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/store"
)

// StreamEvent is one user-visible progress update emitted during a run.
type StreamEvent struct {
	Type        string               `json:"type"`
	Text        string               `json:"text,omitempty"`
	ToolCall    *StreamToolCall      `json:"tool_call,omitempty"`
	Session     *store.Session       `json:"session,omitempty"`
	Interaction *interaction.Request `json:"interaction,omitempty"`
}

// StreamToolCall identifies one tool invocation across incremental updates.
type StreamToolCall struct {
	CallIndex int    `json:"call_index"`
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	Status    string `json:"status"`
}
