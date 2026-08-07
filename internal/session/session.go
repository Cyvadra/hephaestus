// Package session implements Session lifecycle, active-path reconstruction
// and the compression-cache validator described in the design doc. Active
// path resolution always walks the full ChatMessage set for a session in
// memory rather than issuing one query per hop.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Service provides session lifecycle operations backed by db.
type Service struct {
	db             *gorm.DB
	defaultProject string
}

var (
	// ErrStaleActiveLeaf means another request changed the active branch
	// after this caller assembled its turn.
	ErrStaleActiveLeaf  = errors.New("session: active leaf changed")
	ErrInvalidParent    = errors.New("session: parent message does not belong to session")
	ErrMessageNotFound  = errors.New("session: message not found")
	ErrNotAssistant     = errors.New("session: message is not an assistant message")
	ErrToolCallMessage  = errors.New("session: assistant messages with tool calls cannot be edited")
	ErrMessageNotOnPath = errors.New("session: message is not on the selected active path")
	ErrEmptyContent     = errors.New("session: message content cannot be empty")
)

// New creates a Service backed by db. When defaultProject is provided, new
// and replacement sessions with no explicit Project bind to it.
func New(db *gorm.DB, defaultProject ...string) *Service {
	svc := &Service{db: db}
	if len(defaultProject) > 0 {
		svc.defaultProject = defaultProject[0]
	}
	return svc
}

// CreateFromConcierge creates a new Session whose initial Settings are a
// snapshot of concierge's identity/impressions/tool groups/plugins.
func (s *Service) CreateFromConcierge(concierge registry.Concierge) (*store.Session, error) {
	return s.Create(concierge.Name, settingsFromConcierge(concierge))
}

// Create makes a new session from an explicit settings snapshot.
func (s *Service) Create(sourceConcierge string, settings store.SessionSettings) (*store.Session, error) {
	settings = s.withDefaultProject(settings)
	sess := &store.Session{
		SourceConcierge: sourceConcierge,
		Settings:        datatypes.NewJSONType(settings),
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("session: create: %w", err)
	}
	return sess, nil
}

func settingsFromConcierge(concierge registry.Concierge) store.SessionSettings {
	return store.SessionSettings{
		Identity:    concierge.Identity,
		Impressions: append([]string(nil), concierge.Impressions...),
		ToolGroups:  append([]string(nil), concierge.ToolGroups...),
		Plugins:     append([]string(nil), concierge.Plugins...),
	}
}

// AppendMessage inserts msg as a child of parentID (nil for the first
// message of a session), advances the session's active leaf to it, and
// returns the persisted row.
func (s *Service) AppendMessage(sessionID uint, parentID *uint, msg store.ChatMessage) (*store.ChatMessage, error) {
	msg.SessionID = sessionID
	msg.ParentMessageID = parentID
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	return &msg, s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&msg).Error; err != nil {
			return fmt.Errorf("session: append message: %w", err)
		}
		if err := tx.Model(&store.Session{}).Where("id = ?", sessionID).
			Update("active_leaf_message_id", msg.ID).Error; err != nil {
			return fmt.Errorf("session: advance active leaf: %w", err)
		}
		return nil
	})
}

// AppendMessages inserts msgs in order as a single chain, the first msg
// becoming a child of parentID, each subsequent one a child of the
// previous, then advances the session's active leaf to the last one. All
// inserts and the leaf update happen in a single transaction: either the
// whole turn is recorded or none of it is, matching the design doc's rule
// that incomplete turns (from /stop or errors) must not be persisted.
func (s *Service) AppendMessages(sessionID uint, parentID *uint, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	return s.appendMessages(sessionID, parentID, nil, false, msgs)
}

// AppendMessagesAtLeaf atomically verifies that expectedLeaf is still the
// active branch, appends msgs below parentID, and advances the active leaf.
// It prevents concurrent continuations from silently overwriting each other.
func (s *Service) AppendMessagesAtLeaf(sessionID uint, parentID, expectedLeaf *uint, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	return s.appendMessages(sessionID, parentID, expectedLeaf, true, msgs)
}

func (s *Service) appendMessages(sessionID uint, parentID, expectedLeaf *uint, checkActiveLeaf bool, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	out := make([]store.ChatMessage, len(msgs))
	copy(out, msgs)

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var sess store.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return fmt.Errorf("session: load for append: %w", err)
		}
		if checkActiveLeaf && !sameID(sess.ActiveLeafMessageID, expectedLeaf) {
			return ErrStaleActiveLeaf
		}
		if parentID != nil {
			var count int64
			if err := tx.Model(&store.ChatMessage{}).Where("id = ? AND session_id = ?", *parentID, sessionID).Count(&count).Error; err != nil {
				return fmt.Errorf("session: validate parent: %w", err)
			}
			if count == 0 {
				return ErrInvalidParent
			}
		}
		parent := parentID
		for i := range out {
			out[i].SessionID = sessionID
			out[i].ParentMessageID = parent
			if out[i].Timestamp.IsZero() {
				out[i].Timestamp = time.Now()
			}
			if err := tx.Create(&out[i]).Error; err != nil {
				return fmt.Errorf("session: append message %d/%d: %w", i+1, len(out), err)
			}
			parent = &out[i].ID
		}
		result := tx.Model(&store.Session{}).Where("id = ?", sessionID)
		if checkActiveLeaf {
			if expectedLeaf == nil {
				result = result.Where("active_leaf_message_id IS NULL")
			} else {
				result = result.Where("active_leaf_message_id = ?", *expectedLeaf)
			}
		}
		updated := result.Update("active_leaf_message_id", out[len(out)-1].ID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrStaleActiveLeaf
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SelectActiveLeaf validates that leafID belongs to sessionID before making
// it the active branch.
func (s *Service) SelectActiveLeaf(sessionID, leafID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&store.ChatMessage{}).Where("id = ? AND session_id = ?", leafID, sessionID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrInvalidParent
		}
		return tx.Model(&store.Session{}).Where("id = ?", sessionID).Update("active_leaf_message_id", leafID).Error
	})
}

// EditAssistantAtLeaf creates an edited sibling of messageID and makes it
// the active leaf. The original message and all of its descendants remain
// unchanged and reachable through the session's message tree.
func (s *Service) EditAssistantAtLeaf(sessionID, messageID, expectedLeaf uint, content, reasoningContent string) (*store.ChatMessage, error) {
	if strings.TrimSpace(content) == "" {
		return nil, ErrEmptyContent
	}

	edited := store.ChatMessage{
		SessionID:        sessionID,
		Timestamp:        time.Now(),
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoningContent,
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var sess store.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return fmt.Errorf("session: load for assistant edit: %w", err)
		}
		previousLeaf := sess.ActiveLeafMessageID

		var target store.ChatMessage
		if err := tx.Where("id = ? AND session_id = ?", messageID, sessionID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMessageNotFound
			}
			return fmt.Errorf("session: load assistant message for edit: %w", err)
		}
		if target.Role != "assistant" {
			return ErrNotAssistant
		}
		if hasToolCalls(target.ToolCalls) {
			return ErrToolCallMessage
		}

		var all []store.ChatMessage
		if err := tx.Where("session_id = ?", sessionID).Find(&all).Error; err != nil {
			return fmt.Errorf("session: load active path for assistant edit: %w", err)
		}
		path, err := walkActivePath(all, &expectedLeaf)
		if err != nil {
			return ErrMessageNotOnPath
		}
		onPath := false
		for _, message := range path {
			if message.ID == messageID {
				onPath = true
				break
			}
		}
		if !onPath {
			return ErrMessageNotOnPath
		}

		edited.ParentMessageID = target.ParentMessageID
		if err := tx.Create(&edited).Error; err != nil {
			return fmt.Errorf("session: create edited assistant message: %w", err)
		}
		updated := tx.Model(&store.Session{}).Where("id = ?", sessionID)
		if previousLeaf == nil {
			updated = updated.Where("active_leaf_message_id IS NULL")
		} else {
			updated = updated.Where("active_leaf_message_id = ?", *previousLeaf)
		}
		updated = updated.Update("active_leaf_message_id", edited.ID)
		if updated.Error != nil {
			return fmt.Errorf("session: activate edited assistant message: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrStaleActiveLeaf
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &edited, nil
}

func hasToolCalls(raw datatypes.JSON) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var calls []json.RawMessage
	if err := json.Unmarshal(raw, &calls); err != nil {
		return true
	}
	return len(calls) > 0
}

// Replace archives sessionID and creates its replacement in one transaction.
func (s *Service) Replace(sessionID uint, sourceConcierge string, settings store.SessionSettings) (*store.Session, error) {
	settings = s.withDefaultProject(settings)
	next := &store.Session{SourceConcierge: sourceConcierge, Settings: datatypes.NewJSONType(settings)}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&store.Session{}).Where("id = ?", sessionID).Update("flag_archived", true).Error; err != nil {
			return err
		}
		return tx.Create(next).Error
	}); err != nil {
		return nil, fmt.Errorf("session: replace %d: %w", sessionID, err)
	}
	return next, nil
}

// BindUnscopedSessions attaches the default Project to sessions created
// before one was configured. It is intended to run during startup.
func (s *Service) BindUnscopedSessions() error {
	if s.defaultProject == "" {
		return nil
	}
	var sessions []store.Session
	if err := s.db.Find(&sessions).Error; err != nil {
		return fmt.Errorf("session: list sessions for default project: %w", err)
	}
	for index := range sessions {
		settings := sessions[index].Settings.Data()
		if settings.Project != "" {
			continue
		}
		settings.Project = s.defaultProject
		if err := s.db.Model(&sessions[index]).Update("settings", datatypes.NewJSONType(settings)).Error; err != nil {
			return fmt.Errorf("session: bind default project to %d: %w", sessions[index].ID, err)
		}
	}
	return nil
}

func (s *Service) withDefaultProject(settings store.SessionSettings) store.SessionSettings {
	if settings.Project == "" {
		settings.Project = s.defaultProject
	}
	return settings
}

func sameID(left, right *uint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// ActivePath loads every ChatMessage belonging to sessionID and walks
// ParentMessageID from session.ActiveLeafMessageID back to the root,
// returning messages in root-to-leaf order. It returns an empty slice for a
// session with no messages yet.
func (s *Service) ActivePath(sess store.Session) ([]store.ChatMessage, error) {
	if sess.ActiveLeafMessageID == nil {
		return nil, nil
	}

	var all []store.ChatMessage
	if err := s.db.Where("session_id = ?", sess.ID).Find(&all).Error; err != nil {
		return nil, fmt.Errorf("session: load messages for session %d: %w", sess.ID, err)
	}

	return walkActivePath(all, sess.ActiveLeafMessageID)
}

// walkActivePath is the pure part of ActivePath: given every message of a
// session and its active leaf id, it walks ParentMessageID back to the root
// in memory and returns messages in root-to-leaf order.
func walkActivePath(all []store.ChatMessage, leafID *uint) ([]store.ChatMessage, error) {
	if leafID == nil {
		return nil, nil
	}

	byID := make(map[uint]store.ChatMessage, len(all))
	for _, m := range all {
		byID[m.ID] = m
	}

	var reversed []store.ChatMessage
	currentID := leafID
	for currentID != nil {
		m, ok := byID[*currentID]
		if !ok {
			return nil, fmt.Errorf("session: active_leaf_message_id %d not found among provided messages", *currentID)
		}
		reversed = append(reversed, m)
		currentID = m.ParentMessageID
	}

	path := make([]store.ChatMessage, len(reversed))
	for i, m := range reversed {
		path[len(reversed)-1-i] = m
	}
	return path, nil
}
