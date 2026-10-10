package toolevent

import (
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type stubSource struct{ acked, released []uint }

func (s *stubSource) PendingSessionIDs() ([]uint, error) { return nil, nil }

func (s *stubSource) ClaimNotifications(uint) ([]agent.Notification, error) {
	return []agent.Notification{{ID: 1, Text: "subagent"}}, nil
}
func (s *stubSource) AcknowledgeNotificationsTx(_ *gorm.DB, ids []uint) error {
	s.acked = append(s.acked, ids...)
	return nil
}
func (s *stubSource) ReleaseNotifications(ids []uint) error {
	s.released = append(s.released, ids...)
	return nil
}

func TestCombinedSourceDeliversLateToolResults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&store.Session{}, &store.ToolEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO sessions (id, project_id) VALUES (7, 1)").Error; err != nil {
		t.Fatal(err)
	}
	tools := New(db)
	recorded := make(chan uint, 1)
	tools.SetOnRecorded(func(sessionID uint) { recorded <- sessionID })
	if err := tools.RecordLateToolResult(agent.LateToolResult{SessionID: 7, Tool: "shell", Command: "sleep 600", PID: 42, Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-recorded:
		if id != 7 {
			t.Fatalf("recorded session %d", id)
		}
	case <-time.After(time.Second):
		t.Fatal("onRecorded not called")
	}

	subagents := &stubSource{}
	combined := Combined{Subagents: subagents, Tools: tools}
	notifications, err := combined.ClaimNotifications(7)
	if err != nil || len(notifications) != 2 {
		t.Fatalf("notifications = %+v, err = %v", notifications, err)
	}
	toolNotice := notifications[1]
	if toolNotice.ID == 1 || !strings.Contains(toolNotice.Text, `"command":"sleep 600"`) {
		t.Fatalf("tool notification = %+v", toolNotice)
	}
	if again, _ := tools.ClaimNotifications(7); len(again) != 0 {
		t.Fatalf("claimed event was claimable again: %+v", again)
	}

	if err := combined.AcknowledgeNotificationsTx(db, []uint{1, toolNotice.ID}); err != nil {
		t.Fatal(err)
	}
	if len(subagents.acked) != 1 || subagents.acked[0] != 1 {
		t.Fatalf("subagent acks = %v", subagents.acked)
	}
	var event store.ToolEvent
	if err := db.First(&event).Error; err != nil || event.ConsumedAt == nil {
		t.Fatalf("event = %+v, err = %v", event, err)
	}
}
