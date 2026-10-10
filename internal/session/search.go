package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/gorm"
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
// case-insensitively match query, ordered like ListByProject. Queries are
// parsed by ParseSearchQuery: every keyword must match (title or summary),
// and Simplified/Traditional Chinese variants match each other. This is the
// fast tier of chat history search: it hits the small sessions table and is
// meant to run on every keystroke.
func (s *Service) SearchByTitle(projectID uint, query string, limit int) ([]store.Session, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	parsed := ParseSearchQuery(query)
	db := s.db.Where("project_id = ? AND parent_subagent_run_id IS NULL", projectID)
	for _, term := range parsed.Terms {
		predicate, args := s.anyLike([]string{"title", "summary"}, term)
		db = db.Where(predicate, args...)
	}
	var sessions []store.Session
	if err := db.Order("last_message_time desc, id desc").
		Limit(limit).
		Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("session: search by title: %w", err)
	}
	return sessions, nil
}

// SearchMessages full-text searches chat_messages.content across every
// session in projectID, newest match first. In keyword mode (see
// ParseSearchQuery) it instead finds sessions whose title, summary and
// messages together contain every keyword, returning one result per session
// anchored on its newest message that matches any keyword. This is the slow
// tier of chat history search: it scans the (potentially large) messages
// table and is meant to be debounced or explicitly triggered rather than run
// on every keystroke.
func (s *Service) SearchMessages(projectID uint, query string, limit, offset int) ([]MessageSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	limit, offset = store.NormalizePagination(limit, offset)
	parsed := ParseSearchQuery(query)
	if parsed.Mode == SearchModeKeywords {
		return s.searchSessionsByKeywords(projectID, parsed, limit, offset)
	}
	predicate, args := s.anyLike([]string{"chat_messages.content"}, parsed.Terms[0])

	var rows []messageRow
	if err := s.messageRows().
		Where("sessions.project_id = ? AND sessions.parent_subagent_run_id IS NULL", projectID).
		Where(predicate, args...).
		Order("chat_messages.timestamp DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("session: search messages: %w", err)
	}

	results := make([]MessageSearchResult, len(rows))
	for i, row := range rows {
		results[i] = row.result(parsed.allVariants())
	}
	return results, nil
}

type messageRow struct {
	ID           uint
	SessionID    uint
	SessionTitle string
	Role         string
	Timestamp    time.Time
	Content      string
}

func (row messageRow) result(variants []string) MessageSearchResult {
	return MessageSearchResult{
		MessageID:    row.ID,
		SessionID:    row.SessionID,
		SessionTitle: row.SessionTitle,
		Role:         row.Role,
		Timestamp:    row.Timestamp,
		Snippet:      snippetAroundAny(row.Content, variants, snippetRuneBudget),
	}
}

// messageRowColumns is the projection scanned into messageRow.
const messageRowColumns = "chat_messages.id AS id, chat_messages.session_id AS session_id, sessions.title AS session_title, chat_messages.role AS role, chat_messages.timestamp AS timestamp, chat_messages.content AS content"

func (s *Service) messageRows() *gorm.DB {
	return s.db.Table("chat_messages").
		Select(messageRowColumns).
		Joins("JOIN sessions ON sessions.id = chat_messages.session_id")
}

// searchSessionsByKeywords matches sessions where each keyword appears in the
// title, the summary or any message, then anchors each session's result on
// its newest message matching any keyword (or its summary when only the
// title/summary matched).
func (s *Service) searchSessionsByKeywords(projectID uint, parsed SearchQuery, limit, offset int) ([]MessageSearchResult, error) {
	// One pass over the project's messages records, per session, which
	// keywords any message contains, instead of a correlated EXISTS scan per
	// keyword. MAX(CASE ...) works on both Postgres and SQLite.
	var hitColumns []string
	var hitArgs []any
	for i, term := range parsed.Terms {
		predicate, args := s.anyLike([]string{"m.content"}, term)
		hitColumns = append(hitColumns, fmt.Sprintf("MAX(CASE WHEN %s THEN 1 ELSE 0 END) AS t%d", predicate, i))
		hitArgs = append(hitArgs, args...)
	}
	anyMessagePredicate, anyMessageArgs := s.anyLike([]string{"m.content"}, parsed.allVariants())
	hits := s.db.Table("chat_messages m").
		Select("m.session_id AS session_id, "+strings.Join(hitColumns, ", "), hitArgs...).
		Joins("JOIN sessions hs ON hs.id = m.session_id").
		Where("hs.project_id = ? AND hs.parent_subagent_run_id IS NULL", projectID).
		Where(anyMessagePredicate, anyMessageArgs...).
		Group("m.session_id")

	db := s.db.Model(&store.Session{}).
		Select("sessions.*").
		Joins("LEFT JOIN (?) AS hits ON hits.session_id = sessions.id", hits).
		Where("sessions.project_id = ? AND sessions.parent_subagent_run_id IS NULL", projectID)
	for i, term := range parsed.Terms {
		sessionPredicate, sessionArgs := s.anyLike([]string{"sessions.title", "sessions.summary"}, term)
		db = db.Where(fmt.Sprintf("(%s) OR hits.t%d = 1", sessionPredicate, i), sessionArgs...)
	}
	var sessions []store.Session
	if err := db.Order("sessions.last_message_time desc, sessions.id desc").Limit(limit).Offset(offset).Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("session: keyword search: %w", err)
	}

	variants := parsed.allVariants()
	anyPredicate, anyArgs := s.anyLike([]string{"chat_messages.content"}, variants)
	newest := map[uint]messageRow{}
	if len(sessions) > 0 {
		sessionIDs := make([]uint, len(sessions))
		for i, session := range sessions {
			sessionIDs[i] = session.ID
		}
		matches := s.messageRows().
			Select(messageRowColumns+", ROW_NUMBER() OVER (PARTITION BY chat_messages.session_id ORDER BY chat_messages.timestamp DESC, chat_messages.id DESC) AS row_rank").
			Where("chat_messages.session_id IN ?", sessionIDs).
			Where(anyPredicate, anyArgs...)
		var rows []messageRow
		if err := s.db.Table("(?) AS ranked", matches).Where("row_rank = 1").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("session: keyword search snippet: %w", err)
		}
		for _, row := range rows {
			newest[row.SessionID] = row
		}
	}
	results := make([]MessageSearchResult, 0, len(sessions))
	for _, session := range sessions {
		if row, ok := newest[session.ID]; ok {
			results = append(results, row.result(variants))
			continue
		}
		results = append(results, MessageSearchResult{
			SessionID:    session.ID,
			SessionTitle: session.Title,
			Timestamp:    session.LastMessageTime,
			Snippet:      snippetAroundAny(session.Summary, variants, snippetRuneBudget),
		})
	}
	return results, nil
}

// anyLike builds "col1 LIKE p1 OR col1 LIKE p2 OR col2 ..." matching any
// pattern in any column, case-insensitively.
func (s *Service) anyLike(columns, patterns []string) (string, []any) {
	sqlite := s.db.Dialector.Name() == "sqlite"
	var clauses []string
	var args []any
	for _, column := range columns {
		for _, pattern := range patterns {
			if sqlite {
				clauses = append(clauses, "lower("+column+") LIKE lower(?) ESCAPE '\\'")
			} else {
				clauses = append(clauses, column+" ILIKE ? ESCAPE '\\'")
			}
			args = append(args, "%"+escapeLikePattern(pattern)+"%")
		}
	}
	return strings.Join(clauses, " OR "), args
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
	return snippetAroundAny(content, []string{query}, limit)
}

// snippetAroundAny is snippetAround centered on the earliest occurrence of
// any of queries.
func snippetAroundAny(content string, queries []string, limit int) string {
	flat := strings.Join(strings.Fields(content), " ")
	if flat == "" {
		return ""
	}
	runes := []rune(flat)
	lower := strings.ToLower(flat)
	query, byteIndex := "", -1
	for _, candidate := range queries {
		if index := strings.Index(lower, strings.ToLower(candidate)); index >= 0 && (byteIndex < 0 || index < byteIndex) {
			query, byteIndex = candidate, index
		}
	}
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
