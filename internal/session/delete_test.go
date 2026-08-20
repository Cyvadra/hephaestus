package session

import (
	"errors"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestDeleteRejectsActiveWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*gorm.DB, store.Session) error
	}{
		{
			name: "chat run",
			create: func(db *gorm.DB, session store.Session) error {
				return db.Create(&store.ChatRun{SessionID: session.ID, ProjectID: session.ProjectID, Kind: store.ChatRunMessage, Status: store.ChatRunRunning}).Error
			},
		},
		{
			name: "subagent run",
			create: func(db *gorm.DB, session store.Session) error {
				return db.Create(&store.SubagentRun{ParentSessionID: session.ID, ProjectID: session.ProjectID, Mode: store.SubagentModeSpawn, Schedule: store.SubagentScheduleBackground, Status: store.SubagentRunRunning, Depth: 1, Label: "active", Prompt: "work"}).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&store.Project{}, &store.Session{}, &store.ChatRun{}, &store.ChatRunEvent{}, &store.SubagentRun{}, &store.SubagentEvent{}, &store.ChannelBinding{}, &store.ChatMessage{}, &store.MessageAttachment{}, &store.Compression{}, &store.PluginState{}, &store.ToolAudit{}); err != nil {
				t.Fatal(err)
			}
			project := store.Project{Name: "delete-test"}
			if err := db.Create(&project).Error; err != nil {
				t.Fatal(err)
			}
			session := store.Session{ProjectID: project.ID, Settings: datatypes.NewJSONType(store.SessionSettings{}), LastMessageTime: time.Now()}
			if err := db.Create(&session).Error; err != nil {
				t.Fatal(err)
			}
			if err := test.create(db, session); err != nil {
				t.Fatal(err)
			}

			if err := New(db).Delete(session.ID); !errors.Is(err, ErrSessionBusy) {
				t.Fatalf("Delete() error = %v, want ErrSessionBusy", err)
			}
			if err := db.First(&store.Session{}, session.ID).Error; err != nil {
				t.Fatalf("busy session was deleted: %v", err)
			}
		})
	}
}
