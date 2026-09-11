package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
)

// snippetRuneBudget bounds how much context surrounds a full-text match in a
// MessageSearchResult, keeping list rows compact.
const snippetRuneBudget = 160

// MessageSearchResult is one chat-message hit from SearchMessages, with a
// snippet of content centered on the match for display in a result list.
type MessageSearchResult struct {
	MessageID    uint      `json:"message_id"`
	SessionID    uint      `json:"session_id"`
	SessionTitle string    `json:"session_title"`
	Role         string    `json:"role"`
	Timestamp    time.Time `json:"timestamp"`
	Snippet      string    `json:"snippet"`
}

// SearchByTitle returns sessions in projectID whose Title or Summary
// case-insensitively contains query, ordered like ListByProject. This is the
// fast tier of chat history search: it hits the small sessions table and is
// meant to run on every keystroke.
func (s *Service) SearchByTitle(projectID uint, query string, limit int) ([]store.Session, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	pattern := "%" + escapeLikePattern(query) + "%"
	predicate := "title ILIKE ? ESCAPE '\\' OR summary ILIKE ? ESCAPE '\\'"
	if s.db.Dialector.Name() == "sqlite" {
		predicate = "lower(title) LIKE lower(?) ESCAPE '\\' OR lower(summary) LIKE lower(?) ESCAPE '\\'"
	}
	var sessions []store.Session
	if err := s.db.Where("project_id = ? AND parent_subagent_run_id IS NULL", projectID).
		Where(predicate, pattern, pattern).
		Order("last_message_time desc, id desc").
		Limit(limit).
		Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("session: search by title: %w", err)
	}
	return sessions, nil
}

// SearchMessages full-text searches chat_messages.content across every
// session in projectID, newest match first. This is the slow tier of chat
// history search: it scans the (potentially large) messages table and is
// meant to be debounced or explicitly triggered rather than run on every
// keystroke.
func (s *Service) SearchMessages(projectID uint, query string, limit, offset int) ([]MessageSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	limit, offset = store.NormalizePagination(limit, offset)
	pattern := "%" + escapeLikePattern(query) + "%"
	predicate := "chat_messages.content ILIKE ? ESCAPE '\\'"
	if s.db.Dialector.Name() == "sqlite" {
		predicate = "lower(chat_messages.content) LIKE lower(?) ESCAPE '\\'"
	}

	var rows []struct {
		ID           uint
		SessionID    uint
		SessionTitle string
		Role         string
		Timestamp    time.Time
		Content      string
	}
	if err := s.db.Table("chat_messages").
		Select("chat_messages.id AS id, chat_messages.session_id AS session_id, sessions.title AS session_title, chat_messages.role AS role, chat_messages.timestamp AS timestamp, chat_messages.content AS content").
		Joins("JOIN sessions ON sessions.id = chat_messages.session_id").
		Where("sessions.project_id = ? AND sessions.parent_subagent_run_id IS NULL", projectID).
		Where(predicate, pattern).
		Order("chat_messages.timestamp DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("session: search messages: %w", err)
	}

	results := make([]MessageSearchResult, len(rows))
	for i, row := range rows {
		results[i] = MessageSearchResult{
			MessageID:    row.ID,
			SessionID:    row.SessionID,
			SessionTitle: row.SessionTitle,
			Role:         row.Role,
			Timestamp:    row.Timestamp,
			Snippet:      snippetAround(row.Content, query, snippetRuneBudget),
		}
	}
	return results, nil
}

// escapeLikePattern backslash-escapes LIKE/ILIKE metacharacters in a
// user-supplied search term so it is matched literally within a "%...%"
// wildcard pattern.
func escapeLikePattern(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// snippetAround flattens whitespace in content and returns up to limit runes
// centered on the first case-insensitive occurrence of query, marking
// elided text on either side. If query isn't found (e.g. a trigram-adjacent
// match under future ranking), it falls back to a leading truncation.
func snippetAround(content, query string, limit int) string {
	flat := strings.Join(strings.Fields(content), " ")
	if flat == "" {
		return ""
	}
	runes := []rune(flat)
	lower := strings.ToLower(flat)
	byteIndex := strings.Index(lower, strings.ToLower(query))
	if byteIndex < 0 {
		if len(runes) <= limit {
			return flat
		}
		return string(runes[:limit]) + " …"
	}

	matchStart := len([]rune(flat[:byteIndex]))
	matchEnd := matchStart + len([]rune(query))
	start := max(0, matchStart-(limit-(matchEnd-matchStart))/2)
	end := min(len(runes), start+limit)
	start = max(0, end-limit)

	result := string(runes[start:end])
	if start > 0 {
		result = "… " + result
	}
	if end < len(runes) {
		result += " …"
	}
	return result
}
