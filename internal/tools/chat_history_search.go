package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/gorm"
)

// ChatHistorySearchTool lets the LLM look up specific prior chat history by
// keyword/regex, deliberately scoped to "find this particular exchange"
// rather than general-purpose memory recall.
//
// Matching is done entirely in Go (substring/regexp over messages loaded via
// parameterized GORM queries) rather than pushed into SQL, per the
// platform's "no SQL statements" constraint.
type ChatHistorySearchTool struct {
	db       *gorm.DB
	sessions *session.Service
}

// NewChatHistorySearchTool creates the tool.
func NewChatHistorySearchTool(db *gorm.DB, sessions *session.Service) *ChatHistorySearchTool {
	return &ChatHistorySearchTool{db: db, sessions: sessions}
}

func (ChatHistorySearchTool) Name() string { return "chat_history_search" }
func (ChatHistorySearchTool) Description() string {
	return "Searches this session's (or, if scope=all, every session's) active chat history " +
		"by keyword and/or regex, returning matches with surrounding context messages."
}

func (ChatHistorySearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"keywords": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Case-insensitive substrings to match against message content.",
			},
			"regex": map[string]any{
				"type":        "string",
				"description": "Optional regular expression to match against message content.",
			},
			"num_neighbour_messages": map[string]any{
				"type":        "integer",
				"description": "How many messages before/after each match to include for context. Default 2.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"session", "all"},
				"description": "'session' (default) searches only the calling session; 'all' searches every non-archived session.",
			},
		},
	}
}

type chatHistorySearchArgs struct {
	Keywords             []string `json:"keywords"`
	Regex                string   `json:"regex"`
	NumNeighbourMessages int      `json:"num_neighbour_messages"`
	Scope                string   `json:"scope"`
}

type chatHistoryMatch struct {
	SessionID uint                `json:"session_id"`
	MatchedID uint                `json:"matched_message_id"`
	Context   []store.ChatMessage `json:"context"`
}

const maxChatHistorySearchResults = 20

func (t ChatHistorySearchTool) Execute(ctx context.Context, rawArgs map[string]any) *toolkit.ToolResult {
	args, err := parseChatHistorySearchArgs(rawArgs)
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: %s", err))
	}

	var re *regexp.Regexp
	if args.Regex != "" {
		compiled, err := regexp.Compile(args.Regex)
		if err != nil {
			return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: invalid regex: %s", err))
		}
		re = compiled
	}
	if len(args.Keywords) == 0 && re == nil {
		return toolkit.ErrorResult("chat_history_search: at least one of keywords or regex is required")
	}

	sessions, err := t.targetSessions(ctx, args.Scope)
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: %s", err))
	}

	var matches []chatHistoryMatch
	for _, sess := range sessions {
		path, err := t.sessions.ActivePath(sess)
		if err != nil {
			return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: load active path for session %d: %s", sess.ID, err))
		}
		for i, m := range path {
			if !matchesMessage(m, args.Keywords, re) {
				continue
			}
			lo := max(0, i-args.NumNeighbourMessages)
			hi := min(len(path), i+args.NumNeighbourMessages+1)
			matches = append(matches, chatHistoryMatch{
				SessionID: sess.ID,
				MatchedID: m.ID,
				Context:   path[lo:hi],
			})
			if len(matches) >= maxChatHistorySearchResults {
				break
			}
		}
		if len(matches) >= maxChatHistorySearchResults {
			break
		}
	}

	out, err := json.Marshal(matches)
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: marshal results: %s", err))
	}
	return toolkit.SilentResult(string(out))
}

func parseChatHistorySearchArgs(raw map[string]any) (chatHistorySearchArgs, error) {
	var args chatHistorySearchArgs
	encoded, err := json.Marshal(raw)
	if err != nil {
		return args, fmt.Errorf("encode arguments: %w", err)
	}
	if err := json.Unmarshal(encoded, &args); err != nil {
		return args, fmt.Errorf("parse arguments: %w", err)
	}
	if args.NumNeighbourMessages <= 0 {
		args.NumNeighbourMessages = 2
	}
	return args, nil
}

func (t ChatHistorySearchTool) targetSessions(ctx context.Context, scope string) ([]store.Session, error) {
	if scope == "all" {
		var sessions []store.Session
		if err := t.db.Where("flag_archived = ?", false).Find(&sessions).Error; err != nil {
			return nil, fmt.Errorf("chat_history_search: list sessions: %w", err)
		}
		return sessions, nil
	}

	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("chat_history_search: no session in context for scope=session")
	}
	var sess store.Session
	if err := t.db.First(&sess, sessionID).Error; err != nil {
		return nil, fmt.Errorf("chat_history_search: load session %d: %w", sessionID, err)
	}
	return []store.Session{sess}, nil
}

func matchesMessage(m store.ChatMessage, keywords []string, re *regexp.Regexp) bool {
	if re != nil && re.MatchString(m.Content) {
		return true
	}
	lower := strings.ToLower(m.Content)
	for _, kw := range keywords {
		if kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
