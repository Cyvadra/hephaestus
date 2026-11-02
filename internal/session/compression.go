package session

import (
	"fmt"

	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ResolveCompression implements the session compression-cache validator
// table from the design doc:
//
//  1. CompressionID set, its LastMessageID on the active path -> cache hit,
//     return that Compression unchanged.
//  2. CompressionID set, its LastMessageID off the active path (branch
//     switch) -> look up the most recent Compression for this session whose
//     LastMessageID *is* on the active path; adopt it, or clear the
//     session's compression pointers if none exists.
//  3. CompressionID unset -> no pointer to validate, but still look up the
//     most recent Compression whose LastMessageID is on the active path and
//     adopt it. This is how switching back to a branch re-adopts compression
//     that an earlier switch away cleared.
//
// sess's compression pointers are updated in place and persisted whenever
// case 2 changes them.
func (s *Service) ResolveCompression(sess *store.Session, activePath []store.ChatMessage) (*store.Compression, error) {
	comp, changed, err := s.resolveCompression(*sess, activePath)
	if err != nil {
		return nil, err
	}
	if !changed {
		return comp, nil
	}

	if comp == nil {
		sess.CompressionID = nil
		sess.CompressionLastMessageID = nil
		if err := s.db.Model(sess).Select("CompressionID", "CompressionLastMessageID").Updates(sess).Error; err != nil {
			return nil, fmt.Errorf("session: clear compression pointers for session %d: %w", sess.ID, err)
		}
		return nil, nil
	}

	sess.CompressionID = &comp.ID
	sess.CompressionLastMessageID = &comp.LastMessageID
	if err := s.db.Model(sess).Select("CompressionID", "CompressionLastMessageID").Updates(sess).Error; err != nil {
		return nil, fmt.Errorf("session: adopt compression %d for session %d: %w", comp.ID, sess.ID, err)
	}
	return comp, nil
}

// ResolveCompressionReadOnly returns the compression that applies to
// activePath without mutating sess or the database. Ephemeral callers such
// as side questions use it so a branch switch cannot repair the session's
// cache pointers as a side effect.
func (s *Service) ResolveCompressionReadOnly(sess store.Session, activePath []store.ChatMessage) (*store.Compression, error) {
	comp, _, err := s.resolveCompression(sess, activePath)
	return comp, err
}

// resolveCompression validates the session's cached compression against
// activePath. changed reports whether ResolveCompression would need to
// update the session's pointers (adopt a candidate or clear them).
func (s *Service) resolveCompression(sess store.Session, activePath []store.ChatMessage) (*store.Compression, bool, error) {
	onPath := make(map[uint]bool, len(activePath))
	for _, m := range activePath {
		onPath[m.ID] = true
	}

	if sess.CompressionID != nil && sess.CompressionLastMessageID != nil && onPath[*sess.CompressionLastMessageID] {
		var comp store.Compression
		if err := s.db.First(&comp, *sess.CompressionID).Error; err != nil {
			return nil, false, fmt.Errorf("session: load cached compression %d: %w", *sess.CompressionID, err)
		}
		return &comp, false, nil
	}

	// Cache miss: the active path changed (branch switch), or an earlier
	// switch cleared the pointers. Look for any compression of this session
	// whose coverage still ends on the current active path, preferring the
	// most recent one. The query must run even when CompressionID is nil,
	// because switching back to a branch must re-adopt its compression
	// instead of replaying the full raw history.
	pathIDs := make([]uint, 0, len(activePath))
	for _, m := range activePath {
		pathIDs = append(pathIDs, m.ID)
	}

	var candidates []store.Compression
	if err := s.db.Where("session_id = ? AND last_message_id IN ?", sess.ID, pathIDs).
		Order("created_at DESC").Find(&candidates).Error; err != nil {
		return nil, false, fmt.Errorf("session: query compressions for session %d: %w", sess.ID, err)
	}

	if len(candidates) == 0 {
		// Only a stale pointer needs clearing; nil pointers are already clear.
		return nil, sess.CompressionID != nil, nil
	}
	return &candidates[0], true, nil
}

// StoreCompression creates a cache row and immediately makes it the
// session's active compression cache. Compression survives a later failed
// turn because it covers only already-persisted chat history.
func (s *Service) StoreCompression(sessionID, firstMessageID, lastMessageID uint, messages datatypes.JSON) (*store.Compression, error) {
	row := &store.Compression{
		SessionID:      sessionID,
		FirstMessageID: firstMessageID,
		LastMessageID:  lastMessageID,
		Messages:       messages,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return fmt.Errorf("session: create compression: %w", err)
		}
		if err := tx.Model(&store.Session{}).Where("id = ?", sessionID).Updates(map[string]any{
			"compression_id":              row.ID,
			"compression_last_message_id": lastMessageID,
		}).Error; err != nil {
			return fmt.Errorf("session: set compression pointers: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return row, nil
}
