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
// platform's "no SQL statements" constraint. Results are rendered as a
// compact, de-duplicated transcript with per-message truncation so a search
// can never flood the model's context window.
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
	return "Searches every session in the same Project's chat history " +
		"by keyword and/or regex, returning matched messages with surrounding context."
}

// Scopes restricts this tool to interactive sessions: chat history search
// has no meaning for headless workflow runs that have no chat history.
func (ChatHistorySearchTool) Scopes() []toolkit.Scope {
	return []toolkit.Scope{toolkit.ScopeSession}
}

func (ChatHistorySearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_ids": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Optional session IDs from an earlier result to narrow the search.",
			},
			"keywords": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Case-insensitive substrings; a message matches if it contains any of them.",
			},
			"regex": map[string]any{
				"type":        "string",
				"description": "Optional case-insensitive regular expression to match against message content.",
			},
			"num_neighbour_messages": map[string]any{
				"type":        "integer",
				"description": "How many messages before/after each match to include for context. Default 2, max 5.",
			},
			"include_archived": map[string]any{
				"type":        "boolean",
				"description": "Whether to include archived sessions. Defaults to true.",
			},
		},
	}
}

type chatHistorySearchArgs struct {
	SessionIDs           []uint   `json:"session_ids"`
	Keywords             []string `json:"keywords"`
	Regex                string   `json:"regex"`
	NumNeighbourMessages *int     `json:"num_neighbour_messages"`
	IncludeArchived      *bool    `json:"include_archived"`
}

const (
	// maxChatHistorySearchResults bounds total matches across all sessions.
	maxChatHistorySearchResults = 20
	// maxMatchesPerSession keeps one chatty session from exhausting the
	// global budget when scope=all.
	maxMatchesPerSession = 5
	// maxNeighbourMessages bounds the caller-supplied context window.
	maxNeighbourMessages = 5
	// matchedSnippetLen / neighbourSnippetLen bound per-message output.
	matchedSnippetLen   = 600
	neighbourSnippetLen = 200
)

func (t ChatHistorySearchTool) Execute(ctx context.Context, rawArgs map[string]any) *toolkit.ToolResult {
	args, err := parseChatHistorySearchArgs(rawArgs)
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: %s", err))
	}

	var re *regexp.Regexp
	if args.Regex != "" {
		compiled, err := regexp.Compile("(?i)" + args.Regex)
		if err != nil {
			return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: invalid regex: %s", err))
		}
		re = compiled
	}
	if len(args.Keywords) == 0 && re == nil {
		return toolkit.ErrorResult("chat_history_search: at least one of keywords or regex is required")
	}

	sessions, err := t.targetSessions(ctx, args.IncludeArchived == nil || *args.IncludeArchived, args.SessionIDs)
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: %s", err))
	}
	if len(args.Keywords) > 0 && re == nil {
		sessions, err = t.sessionsContainingKeywords(sessions, args.Keywords)
		if err != nil {
			return toolkit.ErrorResult(fmt.Sprintf("chat_history_search: prefilter messages: %s", err))
		}
	}
	perSessionCap := maxChatHistorySearchResults
	if len(sessions) > 1 {
		perSessionCap = maxMatchesPerSession
	}

	var (
		out          strings.Builder
		totalShown   int
		totalMatched int
		skipped      []string
	)
	for _, sess := range sessions {
		path, err := t.sessions.ActivePath(sess)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("session %d: %s", sess.ID, err))
			continue
		}

		matched := matchedIndices(path, args.Keywords, re)
		totalMatched += len(matched)
		if len(matched) == 0 {
			continue
		}
		if totalShown >= maxChatHistorySearchResults {
			continue
		}

		budget := min(perSessionCap, maxChatHistorySearchResults-totalShown)
		shown := matched
		if len(shown) > budget {
			shown = shown[:budget]
		}
		totalShown += len(shown)

		writeSessionMatches(&out, sess, path, shown, len(matched), *args.NumNeighbourMessages, args.Keywords, re)
	}

	if totalMatched == 0 {
		if len(skipped) == len(sessions) && len(skipped) > 0 {
			return toolkit.ErrorResult("chat_history_search: " + strings.Join(skipped, "; "))
		}
		return toolkit.SilentResult("chat_history_search: no matching messages found.")
	}
	header := fmt.Sprintf("%d matching message(s) found", totalMatched)
	if totalShown < totalMatched {
		header += fmt.Sprintf(", showing %d (refine keywords/regex to narrow down)", totalShown)
	}
	if len(skipped) > 0 {
		out.WriteString("\nSkipped " + strings.Join(skipped, "; ") + ".\n")
	}
	return toolkit.SilentResult(header + ". Use chat_history_read with a message_id to inspect the full message and its context.\n\n" + strings.TrimRight(out.String(), "\n"))
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
	if args.NumNeighbourMessages == nil {
		defaultNeighbours := 2
		args.NumNeighbourMessages = &defaultNeighbours
	}
	if *args.NumNeighbourMessages < 0 {
		return args, fmt.Errorf("num_neighbour_messages cannot be negative")
	}
	if *args.NumNeighbourMessages > maxNeighbourMessages {
		*args.NumNeighbourMessages = maxNeighbourMessages
	}
	return args, nil
}

func (t ChatHistorySearchTool) targetSessions(ctx context.Context, includeArchived bool, sessionIDs []uint) ([]store.Session, error) {
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no session in context")
	}
	var caller store.Session
	if err := t.db.First(&caller, sessionID).Error; err != nil {
		return nil, fmt.Errorf("load session %d: %w", sessionID, err)
	}
	query := t.db.Where("project_id = ? AND parent_subagent_run_id IS NULL", caller.ProjectID)
	if len(sessionIDs) > 0 {
		query = query.Where("id IN ?", sessionIDs)
	}
	if !includeArchived {
		query = query.Where("flag_archived = ?", false)
	}
	var sessions []store.Session
	if err := query.Order("last_message_time DESC, id DESC").Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

func (t ChatHistorySearchTool) sessionsContainingKeywords(sessions []store.Session, keywords []string) ([]store.Session, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	sessionIDs := make([]uint, len(sessions))
	for index := range sessions {
		sessionIDs[index] = sessions[index].ID
	}
	var predicates []string
	var predicateArgs []any
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		predicates = append(predicates, "strpos(lower(content), lower(?)) > 0")
		predicateArgs = append(predicateArgs, keyword)
	}
	if len(predicates) == 0 {
		return sessions, nil
	}
	query := t.db.Model(&store.ChatMessage{}).Distinct("session_id").
		Where("session_id IN ?", sessionIDs).
		Where("("+strings.Join(predicates, " OR ")+")", predicateArgs...)
	var candidateIDs []uint
	if err := query.Pluck("session_id", &candidateIDs).Error; err != nil {
		return nil, err
	}
	candidates := make(map[uint]struct{}, len(candidateIDs))
	for _, sessionID := range candidateIDs {
		candidates[sessionID] = struct{}{}
	}
	filtered := make([]store.Session, 0, len(candidateIDs))
	for _, sess := range sessions {
		if _, ok := candidates[sess.ID]; ok {
			filtered = append(filtered, sess)
		}
	}
	return filtered, nil
}

// matchedIndices returns the indices of messages in path that match the
// query. Tool-role messages are searched too, since past tool output is a
// legitimate recall target.
func matchedIndices(path []store.ChatMessage, keywords []string, re *regexp.Regexp) []int {
	var out []int
	for i, m := range path {
		if matchesMessage(m, keywords, re) {
			out = append(out, i)
		}
	}
	return out
}

func matchesMessage(m store.ChatMessage, keywords []string, re *regexp.Regexp) bool {
	if m.Content == "" {
		return false
	}
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

// writeSessionMatches renders one session's matches as a compact transcript.
// Overlapping context windows of adjacent matches are merged into contiguous
// ranges so no message is printed twice.
func writeSessionMatches(out *strings.Builder, sess store.Session, path []store.ChatMessage, shown []int, total, neighbours int, keywords []string, re *regexp.Regexp) {
	fmt.Fprintf(out, "## Session %d — %s\n", sess.ID, sessionTitle(sess))
	if total > len(shown) {
		fmt.Fprintf(out, "(%d matches in this session, showing first %d)\n", total, len(shown))
	}

	isMatch := make(map[int]bool, len(shown))
	for _, i := range shown {
		isMatch[i] = true
	}

	for _, r := range mergeWindows(shown, neighbours, len(path)) {
		if r.lo > 0 {
			out.WriteString("...\n")
		}
		for i := r.lo; i < r.hi; i++ {
			writeMessageLine(out, path[i], isMatch[i], keywords, re)
		}
		if r.hi < len(path) {
			out.WriteString("...\n")
		}
	}
	out.WriteString("\n")
}

type indexRange struct{ lo, hi int }

// mergeWindows expands each matched index by `neighbours` on both sides and
// merges overlapping/adjacent windows. Input indices must be ascending.
func mergeWindows(indices []int, neighbours, length int) []indexRange {
	var out []indexRange
	for _, i := range indices {
		lo := max(0, i-neighbours)
		hi := min(length, i+neighbours+1)
		if n := len(out); n > 0 && lo <= out[n-1].hi {
			if hi > out[n-1].hi {
				out[n-1].hi = hi
			}
			continue
		}
		out = append(out, indexRange{lo, hi})
	}
	return out
}

func writeMessageLine(out *strings.Builder, m store.ChatMessage, matched bool, keywords []string, re *regexp.Regexp) {
	marker := " "
	limit := neighbourSnippetLen
	if matched {
		marker = ">"
		limit = matchedSnippetLen
	}
	content := m.Content
	if content == "" {
		content = "(no text content)"
	}
	formatted := snippet(content, limit)
	if matched {
		formatted = matchedSnippet(content, keywords, re, limit)
	}
	fmt.Fprintf(out, "%s [#%d %s %s] %s\n",
		marker, m.ID, m.Role, m.Timestamp.Format("2006-01-02 15:04"),
		formatted)
}

func matchedSnippet(content string, keywords []string, re *regexp.Regexp, limit int) string {
	flat := strings.Join(strings.Fields(content), " ")
	location := firstMatchLocation(flat, keywords, re)
	if location == nil {
		return snippet(flat, limit)
	}
	runes := []rune(flat)
	matchStart := len([]rune(flat[:location[0]]))
	matchEnd := matchStart + len([]rune(flat[location[0]:location[1]]))
	start := max(0, matchStart-(limit-(matchEnd-matchStart))/2)
	end := min(len(runes), start+limit)
	start = max(0, end-limit)
	result := string(runes[start:end])
	if start > 0 {
		result = "… " + result
	}
	if end < len(runes) {
		result += fmt.Sprintf(" …(+%d chars)", len(runes)-end)
	}
	return result
}

func firstMatchLocation(content string, keywords []string, re *regexp.Regexp) []int {
	var first []int
	if re != nil {
		first = re.FindStringIndex(content)
	}
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		location := regexp.MustCompile("(?i)" + regexp.QuoteMeta(keyword)).FindStringIndex(content)
		if location != nil && (first == nil || location[0] < first[0]) {
			first = location
		}
	}
	return first
}

// snippet flattens whitespace and truncates content to at most limit runes,
// marking elided text.
func snippet(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf(" …(+%d chars)", len(runes)-limit)
}
