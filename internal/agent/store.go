package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/store"
)

// StoreMessageFromDS4 converts a provider message into a store message,
// serializing any tool_calls payload into the store's JSONB column.
func StoreMessageFromDS4(m ds4.Message) (store.ChatMessage, error) {
	out := store.ChatMessage{
		Role:             m.Role,
		Content:          m.Content,
		ReasoningContent: m.ReasoningContent,
		ToolCallID:       m.ToolCallID,
		Status:           store.MessageStatusComplete,
		Timestamp:        time.Now(),
	}
	if len(m.ToolCalls) > 0 {
		data, err := json.Marshal(m.ToolCalls)
		if err != nil {
			return store.ChatMessage{}, fmt.Errorf("agent: marshal tool_calls: %w", err)
		}
		out.ToolCalls = data
	}
	return out, nil
}
