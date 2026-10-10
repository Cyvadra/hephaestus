// Package toolevent records tool calls that finished after a force steer
// abandoned them, and delivers them to their session as completion
// notifications, mirroring subagent completion events.
package toolevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/outbox"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Service stores and claims tool completion events.
type Service struct {
	db *gorm.DB
	// onRecorded is called after an event is stored, typically to deliver it.
	onRecorded func(sessionID uint)
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

// SetOnRecorded registers a callback run after each recorded event.
func (s *Service) SetOnRecorded(fn func(sessionID uint)) { s.onRecorded = fn }

// RecordLateToolResult implements agent.LateToolRecorder.
func (s *Service) RecordLateToolResult(result agent.LateToolResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("toolevent: encode: %w", err)
	}
	// Events for a deleted session would never be consumed and would be
	// retried by every startup sweep.
	var sessions int64
	if err := s.db.Model(&store.Session{}).Where("id = ?", result.SessionID).Count(&sessions).Error; err != nil {
		return fmt.Errorf("toolevent: check session: %w", err)
	}
	if sessions == 0 {
		return nil
	}
	if err := s.db.Create(&store.ToolEvent{SessionID: result.SessionID, Payload: datatypes.JSON(payload)}).Error; err != nil {
		return fmt.Errorf("toolevent: record: %w", err)
	}
	if s.onRecorded != nil {
		go s.onRecorded(result.SessionID)
	}
	return nil
}

func (s *Service) notifications() outbox.Table {
	return outbox.Table{DB: s.db, Model: &store.ToolEvent{}, SessionColumn: "session_id", Prefix: "Late tool completion (a tool call abandoned after a force steer has now finished): "}
}

// ClaimNotifications leases unconsumed events for sessionID in order.
func (s *Service) ClaimNotifications(sessionID uint) ([]agent.Notification, error) {
	notifications, err := s.notifications().ClaimNotifications(sessionID)
	if err != nil {
		return nil, fmt.Errorf("toolevent: claim: %w", err)
	}
	return notifications, nil
}

func (s *Service) AcknowledgeNotificationsTx(tx *gorm.DB, ids []uint) error {
	return s.notifications().AcknowledgeNotificationsTx(tx, ids)
}

func (s *Service) ReleaseNotifications(ids []uint) error {
	return s.notifications().ReleaseNotifications(ids)
}

// PendingSessionIDs lists sessions with unconsumed events.
func (s *Service) PendingSessionIDs() ([]uint, error) {
	return s.notifications().PendingSessionIDs()
}

// Source is a durable notification source, as consumed by chat.Pipeline.
type Source interface {
	ClaimNotifications(uint) ([]agent.Notification, error)
	AcknowledgeNotificationsTx(*gorm.DB, []uint) error
	ReleaseNotifications([]uint) error
	PendingSessionIDs() ([]uint, error)
}

// toolTag marks notification IDs owned by the tool-event source so they never
// collide with subagent event IDs in one turn's acknowledgement list. Tagged
// IDs live only in memory between claim and ack/release (agent.Result,
// chat.TurnOptions); they are never persisted or sent to clients, and table
// IDs stay far below the tag bit.
const toolTag = uint(1) << (bits.UintSize - 1)

// Combined merges subagent and tool notifications into one Source.
type Combined struct {
	Subagents Source
	Tools     *Service
}

func (c Combined) ClaimNotifications(sessionID uint) ([]agent.Notification, error) {
	notifications, err := c.Subagents.ClaimNotifications(sessionID)
	if err != nil {
		return nil, err
	}
	tools, err := c.Tools.ClaimNotifications(sessionID)
	if err != nil {
		subagentIDs := make([]uint, len(notifications))
		for i := range notifications {
			subagentIDs[i] = notifications[i].ID
		}
		_ = c.Subagents.ReleaseNotifications(subagentIDs)
		return nil, err
	}
	for _, notification := range tools {
		notification.ID |= toolTag
		notifications = append(notifications, notification)
	}
	return notifications, nil
}

func (c Combined) AcknowledgeNotificationsTx(tx *gorm.DB, all []uint) error {
	subagentIDs, toolIDs := split(all)
	if err := c.Subagents.AcknowledgeNotificationsTx(tx, subagentIDs); err != nil {
		return err
	}
	return c.Tools.AcknowledgeNotificationsTx(tx, toolIDs)
}

func (c Combined) ReleaseNotifications(all []uint) error {
	subagentIDs, toolIDs := split(all)
	// Release both sources even if one fails, so leases are not held.
	return errors.Join(c.Subagents.ReleaseNotifications(subagentIDs), c.Tools.ReleaseNotifications(toolIDs))
}

// PendingSessionIDs unions the sessions pending in either source.
func (c Combined) PendingSessionIDs() ([]uint, error) {
	sessionIDs, err := c.Subagents.PendingSessionIDs()
	if err != nil {
		return nil, err
	}
	toolSessionIDs, err := c.Tools.PendingSessionIDs()
	if err != nil {
		return nil, err
	}
	seen := make(map[uint]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		seen[id] = true
	}
	for _, id := range toolSessionIDs {
		if !seen[id] {
			sessionIDs = append(sessionIDs, id)
		}
	}
	return sessionIDs, nil
}

func split(all []uint) (subagentIDs, toolIDs []uint) {
	for _, id := range all {
		if id&toolTag != 0 {
			toolIDs = append(toolIDs, id&^toolTag)
		} else {
			subagentIDs = append(subagentIDs, id)
		}
	}
	return subagentIDs, toolIDs
}
