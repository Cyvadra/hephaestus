package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/gorm"
)

type ChatHistoryReadTool struct {
	db       *gorm.DB
	sessions activePathLoader
}

type activePathLoader interface {
	ActivePath(store.Session) ([]store.ChatMessage, error)
}

func NewChatHistoryReadTool(db *gorm.DB, sessions activePathLoader) *ChatHistoryReadTool {
	return &ChatHistoryReadTool{db: db, sessions: sessions}
}

func (ChatHistoryReadTool) Name() string { return "chat_history_read" }

func (ChatHistoryReadTool) Description() string {
	return "Reads one chat message found by chat_history_search with surrounding messages from its active conversation path."
}

func (ChatHistoryReadTool) Scopes() []toolkit.Scope {
	return []toolkit.Scope{toolkit.ScopeSession}
}

func (ChatHistoryReadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message_id": map[string]any{
				"type":        "integer",
				"description": "Message ID returned by chat_history_search.",
			},
			"num_neighbour_messages": map[string]any{
				"type":        "integer",
				"description": "Messages before and after the target to include. Default 5, max 20.",
			},
			"max_chars": map[string]any{
				"type":        "integer",
				"description": "Maximum characters per message. Default 2000, max 10000.",
			},
		},
		"required": []string{"message_id"},
	}
}

type chatHistoryReadArgs struct {
	MessageID            uint `json:"message_id"`
	NumNeighbourMessages int  `json:"num_neighbour_messages"`
	MaxChars             int  `json:"max_chars"`
}

func (t ChatHistoryReadTool) Execute(ctx context.Context, rawArgs map[string]any) *toolkit.ToolResult {
	var args chatHistoryReadArgs
	encoded, err := json.Marshal(rawArgs)
	if err != nil {
		return toolkit.ErrorResult("chat_history_read: encode arguments: " + err.Error())
	}
	if err := json.Unmarshal(encoded, &args); err != nil {
		return toolkit.ErrorResult("chat_history_read: parse arguments: " + err.Error())
	}
	if args.MessageID == 0 {
		return toolkit.ErrorResult("chat_history_read: message_id is required")
	}
	if args.NumNeighbourMessages == 0 {
		args.NumNeighbourMessages = 5
	}
	if args.NumNeighbourMessages < 0 {
		return toolkit.ErrorResult("chat_history_read: num_neighbour_messages cannot be negative")
	}
	args.NumNeighbourMessages = min(args.NumNeighbourMessages, 20)
	if args.MaxChars == 0 {
		args.MaxChars = 2000
	}
	if args.MaxChars < 1 {
		return toolkit.ErrorResult("chat_history_read: max_chars must be positive")
	}
	args.MaxChars = min(args.MaxChars, 10000)

	callerSessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok {
		return toolkit.ErrorResult("chat_history_read: no session in context")
	}
	var caller store.Session
	if err := t.db.First(&caller, callerSessionID).Error; err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_read: load session %d: %s", callerSessionID, err))
	}
	var target store.ChatMessage
	if err := t.db.First(&target, args.MessageID).Error; err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_read: load message %d: %s", args.MessageID, err))
	}
	var targetSession store.Session
	if err := t.db.Where("id = ? AND project_id = ?", target.SessionID, caller.ProjectID).First(&targetSession).Error; err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_read: message %d is not in the current project", args.MessageID))
	}
	path, err := t.sessions.ActivePath(targetSession)
	if err != nil {
		return toolkit.ErrorResult("chat_history_read: load active path: " + err.Error())
	}
	targetIndex := -1
	for index := range path {
		if path[index].ID == args.MessageID {
			targetIndex = index
			break
		}
	}
	if targetIndex < 0 {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_read: message %d is not on the session's active path", args.MessageID))
	}

	start := max(0, targetIndex-args.NumNeighbourMessages)
	end := min(len(path), targetIndex+args.NumNeighbourMessages+1)
	var out strings.Builder
	fmt.Fprintf(&out, "## Session %d — %s\n", targetSession.ID, sessionTitle(targetSession))
	for index := start; index < end; index++ {
		marker := " "
		if index == targetIndex {
			marker = ">"
		}
		fmt.Fprintf(&out, "%s [#%d %s %s]\n%s\n", marker, path[index].ID, path[index].Role,
			path[index].Timestamp.Format("2006-01-02 15:04"), preserveSnippet(path[index].Content, args.MaxChars))
	}
	return toolkit.SilentResult(strings.TrimRight(out.String(), "\n"))
}

func sessionTitle(sess store.Session) string {
	if sess.Title == "" {
		return "(untitled)"
	}
	return sess.Title
}

func preserveSnippet(content string, limit int) string {
	if content == "" {
		return "(no text content)"
	}
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	return string(runes[:limit]) + fmt.Sprintf("\n…(+%d chars; increase max_chars to read more)", len(runes)-limit)
}
