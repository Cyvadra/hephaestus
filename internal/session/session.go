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
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Service provides session lifecycle operations backed by db.
type Service struct {
	db *gorm.DB
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

// Patch describes user-editable session metadata. Nil fields are unchanged.
type Patch struct {
	Title           *string
	Archived        *bool
	Pinned          *bool
	ReasoningEffort *string
	EnableWebSearch *bool
}

// ValidationError reports a rejected session metadata change.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

// New creates a Service backed by db.
func New(db *gorm.DB) *Service { return &Service{db: db} }

// Get loads one Session by id.
func (s *Service) Get(sessionID uint) (*store.Session, error) {
	var sess store.Session
	if err := s.db.First(&sess, sessionID).Error; err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListByProject returns sessions in one Project ordered for chat lists.
func (s *Service) ListByProject(projectID uint) ([]store.Session, error) {
	var sessions []store.Session
	if err := s.db.Where("project_id = ?", projectID).Order("updated_at desc").Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// Attachment loads one delivered attachment after validating that it belongs
// to sessionID and that the attachment's Project matches the Session Project.
func (s *Service) Attachment(sessionID, attachmentID uint) (*store.Session, *store.MessageAttachment, error) {
	sess, err := s.Get(sessionID)
	if err != nil {
		return nil, nil, err
	}
	var attachment store.MessageAttachment
	if err := s.db.Where("id = ? AND session_id = ?", attachmentID, sessionID).First(&attachment).Error; err != nil {
		return sess, nil, err
	}
	if attachment.ProjectID != sess.ProjectID {
		return sess, nil, gorm.ErrRecordNotFound
	}
	return sess, &attachment, nil
}

// MessageAttachments returns delivered attachments for one message.
func (s *Service) MessageAttachments(messageID uint) ([]store.MessageAttachment, error) {
	var attachments []store.MessageAttachment
	if err := s.db.Where("message_id = ?", messageID).Find(&attachments).Error; err != nil {
		return nil, err
	}
	return attachments, nil
}

// CreateFromConcierge creates a new Session whose initial Settings are a
// snapshot of concierge's identity/impressions/tool groups/plugins, with
// the initial runtime state (reasoning effort and web-search availability)
// derived from the identity and persisted atomically with the session row.
func (s *Service) CreateFromConcierge(concierge registry.Concierge, projectID uint, reasoningEffort string) (*store.Session, error) {
	enableWebSearch := true
	sess := &store.Session{
		SourceConcierge: concierge.Name,
		ProjectID:       projectID,
		Settings:        datatypes.NewJSONType(SettingsFromConcierge(concierge)),
		ReasoningEffort: reasoningEffort,
		EnableWebSearch: &enableWebSearch,
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("session: create: %w", err)
	}
	return sess, nil
}

// Update applies a validated metadata patch atomically and returns the
// current session row.
func (s *Service) Update(sessionID uint, patch Patch) (*store.Session, error) {
	var updated store.Session
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&updated, sessionID).Error; err != nil {
			return err
		}
		if patch.Title != nil {
			title := strings.TrimSpace(*patch.Title)
			if title == "" {
				return ValidationError("session title cannot be empty")
			}
			if len([]rune(title)) > 64 {
				return ValidationError("session title must be 64 characters or fewer")
			}
			updated.Title = title
		}
		if patch.ReasoningEffort != nil {
			switch *patch.ReasoningEffort {
			case registry.ReasoningNone, registry.ReasoningLow, registry.ReasoningHigh, registry.ReasoningMax:
				updated.ReasoningEffort = *patch.ReasoningEffort
			default:
				return ValidationError("reasoning_effort must be none, low, high, or max")
			}
		}
		if patch.Archived != nil {
			updated.FlagArchived = *patch.Archived
			if updated.FlagArchived {
				updated.FlagPinned = 0
			}
		}
		if patch.Pinned != nil {
			if *patch.Pinned && updated.FlagArchived {
				return ValidationError("an archived session cannot be pinned")
			}
			if *patch.Pinned {
				updated.FlagPinned = 1
			} else {
				updated.FlagPinned = 0
			}
		}
		if patch.EnableWebSearch != nil {
			updated.EnableWebSearch = patch.EnableWebSearch
		}
		return tx.Save(&updated).Error
	})
	if err != nil {
		return nil, fmt.Errorf("session: update %d: %w", sessionID, err)
	}
	return &updated, nil
}

// Delete removes a session and its dependent conversation records atomically.
func (s *Service) Delete(sessionID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var sess store.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", sessionID).Delete(&store.ChannelBinding{}).Error; err != nil {
			return fmt.Errorf("session: delete channel bindings: %w", err)
		}
		for _, model := range []any{&store.ChatMessage{}, &store.Compression{}, &store.PluginState{}, &store.ToolAudit{}} {
			if err := tx.Where("session_id = ?", sessionID).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&sess).Error
	})
}

// SettingsFromConcierge copies a concierge's mutable session settings.
func SettingsFromConcierge(concierge registry.Concierge) store.SessionSettings {
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
	if msg.Status == "" {
		msg.Status = store.MessageStatusComplete
	}
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
// whole turn is recorded or none of it is, so completed and incomplete turn
// snapshots remain internally consistent.
func (s *Service) AppendMessages(sessionID uint, parentID *uint, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	return s.appendMessages(sessionID, parentID, nil, false, msgs)
}

// AppendMessagesAtLeaf atomically verifies that expectedLeaf is still the
// active branch, appends msgs below parentID, and advances the active leaf.
// It prevents concurrent continuations from silently overwriting each other.
func (s *Service) AppendMessagesAtLeaf(sessionID uint, parentID, expectedLeaf *uint, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	return s.appendMessages(sessionID, parentID, expectedLeaf, true, msgs)
}

// AppendMessagesAtLeafWithDeliveries is AppendMessagesAtLeaf with explicit
// assistant file deliveries atomically attached to the final message.
func (s *Service) AppendMessagesAtLeafWithDeliveries(sessionID, projectID uint, parentID, expectedLeaf *uint, msgs []store.ChatMessage, deliveries []toolkit.FileDelivery) ([]store.ChatMessage, error) {
	return s.appendMessagesWithDeliveries(sessionID, projectID, parentID, expectedLeaf, true, msgs, deliveries)
}

// AppendMessagesDetachedWithDeliveries persists an inactive branch and its
// final-message attachments without changing the active leaf.
func (s *Service) AppendMessagesDetachedWithDeliveries(sessionID, projectID uint, parentID *uint, msgs []store.ChatMessage, deliveries []toolkit.FileDelivery) ([]store.ChatMessage, error) {
	return s.appendMessagesWithDeliveries(sessionID, projectID, parentID, nil, false, msgs, deliveries)
}

func (s *Service) appendMessages(sessionID uint, parentID, expectedLeaf *uint, checkActiveLeaf bool, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	return s.appendMessagesWithDeliveries(sessionID, 0, parentID, expectedLeaf, checkActiveLeaf, msgs, nil)
}

func (s *Service) appendMessagesWithDeliveries(sessionID, projectID uint, parentID, expectedLeaf *uint, checkActiveLeaf bool, msgs []store.ChatMessage, deliveries []toolkit.FileDelivery) ([]store.ChatMessage, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	out := make([]store.ChatMessage, len(msgs))
	copy(out, msgs)

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := insertChain(tx, sessionID, parentID, out); err != nil {
			return err
		}
		if len(deliveries) > 0 {
			final := &out[len(out)-1]
			if final.Role != "assistant" {
				return fmt.Errorf("session: final message must be an assistant to attach files")
			}
			attachments := make([]store.MessageAttachment, 0, len(deliveries))
			for _, delivery := range deliveries {
				attachments = append(attachments, store.MessageAttachment{
					SessionID: sessionID, MessageID: final.ID, ProjectID: projectID,
					Path: delivery.Path, Name: delivery.Name, Size: delivery.Size, MIME: delivery.MIME,
				})
			}
			if err := tx.Create(&attachments).Error; err != nil {
				return fmt.Errorf("session: attach files: %w", err)
			}
			final.Attachments = attachments
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

// AppendMessagesDetached inserts msgs as a single chain below parentID
// without touching the session's active leaf. It lets a turn whose branch
// lost a leaf race persist its output as a reachable-but-inactive branch
// instead of discarding generated tokens.
func (s *Service) AppendMessagesDetached(sessionID uint, parentID *uint, msgs []store.ChatMessage) ([]store.ChatMessage, error) {
	if len(msgs) == 0 {
		return nil, nil
	}
	out := make([]store.ChatMessage, len(msgs))
	copy(out, msgs)
	if err := insertChain(s.db, sessionID, parentID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// insertChain persists msgs as a chain whose first element is a child of
// parentID (nil for the first message of a session), assigning each row's
// id in place. It does not advance the session's active leaf, so callers
// that need atomicity must wrap it in their own transaction.
func insertChain(tx *gorm.DB, sessionID uint, parentID *uint, msgs []store.ChatMessage) error {
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
	for i := range msgs {
		msgs[i].SessionID = sessionID
		msgs[i].ParentMessageID = parent
		if msgs[i].Status == "" {
			msgs[i].Status = store.MessageStatusComplete
		}
		if msgs[i].Timestamp.IsZero() {
			msgs[i].Timestamp = time.Now()
		}
		if err := tx.Create(&msgs[i]).Error; err != nil {
			return fmt.Errorf("session: append message %d/%d: %w", i+1, len(msgs), err)
		}
		parent = &msgs[i].ID
	}
	return nil
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

		all, err := loadMessages(tx, sessionID)
		if err != nil {
			return err
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
		var attachments []store.MessageAttachment
		if err := tx.Where("message_id = ?", target.ID).Find(&attachments).Error; err != nil {
			return fmt.Errorf("session: load assistant attachments for edit: %w", err)
		}
		for index := range attachments {
			attachments[index].ID = 0
			attachments[index].MessageID = edited.ID
			attachments[index].CreatedAt = time.Now()
		}
		if len(attachments) > 0 {
			if err := tx.Create(&attachments).Error; err != nil {
				return fmt.Errorf("session: copy assistant attachments for edit: %w", err)
			}
			edited.Attachments = attachments
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
	next := &store.Session{SourceConcierge: sourceConcierge, Settings: datatypes.NewJSONType(settings)}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var previous store.Session
		if err := tx.First(&previous, sessionID).Error; err != nil {
			return err
		}
		next.ProjectID = previous.ProjectID
		if err := tx.Model(&previous).Update("flag_archived", true).Error; err != nil {
			return err
		}
		return tx.Create(next).Error
	}); err != nil {
		return nil, fmt.Errorf("session: replace %d: %w", sessionID, err)
	}
	return next, nil
}

// ForkAt creates an independent session from the path ending at assistant
// messageID. The message must belong to sessionID and can be on any retained
// branch, not only the session's currently active one.
func (s *Service) ForkAt(sessionID, messageID uint) (*store.Session, error) {
	return s.fork(sessionID, &messageID)
}

func (s *Service) fork(sessionID uint, leafID *uint) (*store.Session, error) {
	var fork store.Session
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var source store.Session
		if err := tx.First(&source, sessionID).Error; err != nil {
			return err
		}

		path, err := forkPath(tx, source, leafID)
		if err != nil {
			return fmt.Errorf("session: load fork path: %w", err)
		}

		fork = store.Session{
			ProjectID:       source.ProjectID,
			SourceConcierge: source.SourceConcierge,
			Settings:        datatypes.NewJSONType(copySettings(source.Settings.Data())),
			ReasoningEffort: source.ReasoningEffort,
			EnableWebSearch: copyBool(source.EnableWebSearch),
			Title:           forkTitle(source.Title),
		}
		if err := tx.Create(&fork).Error; err != nil {
			return fmt.Errorf("session: create fork: %w", err)
		}

		var parentID *uint
		for _, sourceMessage := range path {
			message := store.ChatMessage{
				SessionID:        fork.ID,
				ParentMessageID:  parentID,
				Timestamp:        sourceMessage.Timestamp,
				Role:             sourceMessage.Role,
				Content:          sourceMessage.Content,
				Status:           sourceMessage.Status,
				ReasoningContent: sourceMessage.ReasoningContent,
				ToolCalls:        append(datatypes.JSON(nil), sourceMessage.ToolCalls...),
				ToolCallID:       sourceMessage.ToolCallID,
			}
			if err := tx.Create(&message).Error; err != nil {
				return fmt.Errorf("session: copy fork message %d: %w", sourceMessage.ID, err)
			}

			if len(sourceMessage.Attachments) > 0 {
				attachments := make([]store.MessageAttachment, 0, len(sourceMessage.Attachments))
				for _, sourceAttachment := range sourceMessage.Attachments {
					attachments = append(attachments, store.MessageAttachment{
						SessionID: fork.ID,
						MessageID: message.ID,
						ProjectID: fork.ProjectID,
						Path:      sourceAttachment.Path,
						Name:      sourceAttachment.Name,
						Size:      sourceAttachment.Size,
						MIME:      sourceAttachment.MIME,
					})
				}
				if err := tx.Create(&attachments).Error; err != nil {
					return fmt.Errorf("session: copy fork attachments for message %d: %w", sourceMessage.ID, err)
				}
			}

			parentID = &message.ID
		}
		if parentID != nil {
			if err := tx.Model(&fork).Update("active_leaf_message_id", *parentID).Error; err != nil {
				return fmt.Errorf("session: set fork active leaf: %w", err)
			}
			fork.ActiveLeafMessageID = parentID
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("session: fork %d: %w", sessionID, err)
	}
	return &fork, nil
}

func forkPath(db *gorm.DB, sess store.Session, leafID *uint) ([]store.ChatMessage, error) {
	if leafID == nil {
		return nil, nil
	}
	var leaf store.ChatMessage
	if err := db.Where("id = ? AND session_id = ?", *leafID, sess.ID).First(&leaf).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}
	if leaf.Role != "assistant" {
		return nil, ErrNotAssistant
	}
	all, err := loadMessages(db, sess.ID)
	if err != nil {
		return nil, err
	}
	return walkActivePath(all, leafID)
}

func activePath(db *gorm.DB, sess store.Session) ([]store.ChatMessage, error) {
	if sess.ActiveLeafMessageID == nil {
		return nil, nil
	}
	all, err := loadMessages(db, sess.ID)
	if err != nil {
		return nil, err
	}
	return walkActivePath(all, sess.ActiveLeafMessageID)
}

func copySettings(settings store.SessionSettings) store.SessionSettings {
	return store.SessionSettings{
		Identity:    settings.Identity,
		Impressions: append([]string(nil), settings.Impressions...),
		ToolGroups:  append([]string(nil), settings.ToolGroups...),
		Plugins:     append([]string(nil), settings.Plugins...),
		Project:     settings.Project,
	}
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func forkTitle(sourceTitle string) string {
	if sourceTitle == "" {
		return ""
	}
	const suffix = " (fork)"
	const maxRunes = 64
	runes := []rune(sourceTitle)
	if len(runes) > maxRunes-len([]rune(suffix)) {
		runes = runes[:maxRunes-len([]rune(suffix))]
	}
	return string(runes) + suffix
}

// Messages returns every ChatMessage of sessionID in ascending id order.
func (s *Service) Messages(sessionID uint) ([]store.ChatMessage, error) {
	return loadMessages(s.db, sessionID)
}

// ActivePath loads every ChatMessage belonging to sessionID and walks
// ParentMessageID from session.ActiveLeafMessageID back to the root,
// returning messages in root-to-leaf order. It returns an empty slice for a
// session with no messages yet.
func (s *Service) ActivePath(sess store.Session) ([]store.ChatMessage, error) {
	if sess.ActiveLeafMessageID == nil {
		return nil, nil
	}

	all, err := loadMessages(s.db, sess.ID)
	if err != nil {
		return nil, err
	}
	return walkActivePath(all, sess.ActiveLeafMessageID)
}

// PathAtLeaf returns the root-to-leaf path for leafID after confirming that
// the message belongs to sess. It does not change the session's active leaf.
func (s *Service) PathAtLeaf(sess store.Session, leafID *uint) ([]store.ChatMessage, error) {
	if leafID == nil {
		return nil, nil
	}
	all, err := loadMessages(s.db, sess.ID)
	if err != nil {
		return nil, err
	}
	path, err := walkActivePath(all, leafID)
	if err != nil {
		return nil, ErrInvalidParent
	}
	return path, nil
}

// loadMessages loads every ChatMessage of sessionID in ascending id order.
// Used by ActivePath, the edit path, and HTTP history: message trees are
// walked in memory rather than queried hop-by-hop.
func loadMessages(db *gorm.DB, sessionID uint) ([]store.ChatMessage, error) {
	var all []store.ChatMessage
	if err := db.Preload("Attachments").Where("session_id = ?", sessionID).Order("id").Find(&all).Error; err != nil {
		return nil, fmt.Errorf("session: load messages for session %d: %w", sessionID, err)
	}
	return all, nil
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
	visited := make(map[uint]bool, len(all))
	currentID := leafID
	for currentID != nil {
		if visited[*currentID] {
			return nil, fmt.Errorf("session: cycle detected at message %d", *currentID)
		}
		visited[*currentID] = true
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
