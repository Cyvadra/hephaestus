// Package resume delivers background-subagent completion events to their
// root parent session as durable user messages. It is the delivery half of
// the completion outbox: when a session is idle it auto-triggers a fresh
// turn that feeds the completions in; when a turn is already running it
// releases the lease so the in-flight turn's steer path consumes them.
package resume

import (
	"context"
	"fmt"
	"log"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"gorm.io/gorm"
)

// maxAutoResume bounds how many completion-triggered turns may run back to
// back without an intervening human turn, preventing a spawn → completion →
// auto-spawn cascade from consuming unbounded tokens unattended.
const maxAutoResume = 3

// Dispatcher claims pending completion events for a session and, when the
// session is idle, starts a background turn that persists them as a user
// message and answers them.
type Dispatcher struct {
	db        *gorm.DB
	sessions  *session.Service
	subagents *subagent.Service
	chatRuns  *chatrun.Service
	pipeline  *chat.Pipeline
}

func New(db *gorm.DB, sessions *session.Service, subagents *subagent.Service, chatRuns *chatrun.Service, pipeline *chat.Pipeline) *Dispatcher {
	return &Dispatcher{db: db, sessions: sessions, subagents: subagents, chatRuns: chatRuns, pipeline: pipeline}
}

// Deliver claims and delivers the pending completion notifications for
// sessionID. It is safe to call concurrently: chatrun's partial unique index
// enforces one active run per session, so a loser of a race releases its
// lease for the winner's steer path to consume.
func (d *Dispatcher) Deliver(sessionID uint) {
	notifications, err := d.subagents.ClaimNotifications(sessionID)
	if err != nil || len(notifications) == 0 {
		return
	}
	ids := make([]uint, len(notifications))
	for i := range notifications {
		ids[i] = notifications[i].ID
	}
	release := func() {
		if err := d.subagents.ReleaseNotifications(ids); err != nil {
			log.Printf("resume: release notifications for session %d: %v", sessionID, err)
		}
	}

	sess, err := d.sessions.Get(sessionID)
	if err != nil {
		release()
		return
	}
	if count, err := d.consecutiveResumes(sessionID); err == nil && count >= maxAutoResume {
		// Leave the events for the next human turn's steer path.
		release()
		return
	}

	text := subagent.FormatNotifications(notifications)
	request := map[string]any{"text": text}
	_, err = d.chatRuns.Start(sessionID, sess.ProjectID, store.ChatRunSubagentResume, request, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
		result, runErr := d.pipeline.Run(ctx, sessionID, text, chat.TurnOptions{OnDelta: onDelta, NotificationIDs: ids})
		if result == nil {
			return &chatrun.Result{}, runErr
		}
		var finalMessageID *uint
		if result.Message != nil {
			finalMessageID = &result.Message.ID
		}
		return &chatrun.Result{FinalMessageID: finalMessageID, Response: map[string]any{"message": result.Message}}, runErr
	})
	if err != nil {
		release()
	}
}

// consecutiveResumes counts the immediately preceding back-to-back
// completion-triggered turns for a session.
func (d *Dispatcher) consecutiveResumes(sessionID uint) (int, error) {
	var runs []store.ChatRun
	if err := d.db.Where("session_id = ?", sessionID).Order("id desc").Limit(maxAutoResume + 1).Find(&runs).Error; err != nil {
		return 0, fmt.Errorf("resume: count runs: %w", err)
	}
	count := 0
	for _, r := range runs {
		if r.Kind != store.ChatRunSubagentResume {
			break
		}
		count++
	}
	return count, nil
}

// Sweep delivers pending completion events for every session. It is intended
// for startup, after Reconcile has rebuilt events for runs interrupted by a
// restart.
func (d *Dispatcher) Sweep() error {
	var sessionIDs []uint
	if err := d.db.Model(&store.SubagentEvent{}).Where("consumed_at IS NULL").Distinct().Pluck("parent_session_id", &sessionIDs).Error; err != nil {
		return fmt.Errorf("resume: list pending sessions: %w", err)
	}
	for _, id := range sessionIDs {
		d.Deliver(id)
	}
	return nil
}
