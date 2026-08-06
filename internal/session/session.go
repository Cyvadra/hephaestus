// Package session implements Session lifecycle, active-path reconstruction
// and the compression-cache validator described in the design doc. Active
// path resolution always walks the full ChatMessage set for a session in
// memory rather than issuing one query per hop.
package session

import (
	"fmt"
	"time"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Service provides session lifecycle operations backed by db.
type Service struct {
	db *gorm.DB
}

// New creates a Service backed by db.
func New(db *gorm.DB) *Service {
	return &Service{db: db}
}

// CreateFromConcierge creates a new Session whose initial Settings are a
// snapshot of concierge's identity/impressions/tool groups/plugins.
func (s *Service) CreateFromConcierge(concierge registry.Concierge) (*store.Session, error) {
	settings := store.SessionSettings{
		Identity:    concierge.Identity,
		Impressions: append([]string(nil), concierge.Impressions...),
		ToolGroups:  append([]string(nil), concierge.ToolGroups...),
		Plugins:     append([]string(nil), concierge.Plugins...),
	}

	sess := &store.Session{
		SourceConcierge: concierge.Name,
		Settings:        datatypes.NewJSONType(settings),
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("session: create from concierge %q: %w", concierge.Name, err)
	}
	return sess, nil
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
	if len(msgs) == 0 {
		return nil, nil
	}

	out := make([]store.ChatMessage, len(msgs))
	copy(out, msgs)

	err := s.db.Transaction(func(tx *gorm.DB) error {
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
		return tx.Model(&store.Session{}).Where("id = ?", sessionID).
			Update("active_leaf_message_id", out[len(out)-1].ID).Error
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
