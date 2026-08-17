package resume

import (
	"os"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestFormatNotifications(t *testing.T) {
	got := subagent.FormatNotifications([]agent.Notification{
		{ID: 1, Text: "a"},
		{ID: 2, Text: "b"},
	})
	want := "a\n\nb"
	if got != want {
		t.Fatalf("FormatNotifications = %q, want %q", got, want)
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("HEPHAESTUS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("HEPHAESTUS_TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db
}

func TestConsecutiveResumesGuard(t *testing.T) {
	db := openTestDB(t)
	var project store.Project
	if err := db.Where("name = ?", store.DefaultProjectName).First(&project).Error; err != nil {
		t.Fatalf("load default project: %v", err)
	}
	marker := "resume-test"
	session := store.Session{
		ProjectID: project.ID, SourceConcierge: marker,
		Settings: datatypes.NewJSONType(store.SessionSettings{Identity: "test"}),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		db.Where("session_id = ?", session.ID).Delete(&store.ChatRun{})
		db.Delete(&store.Session{}, session.ID)
	})

	// Two back-to-back auto-resumes, then a human turn.
	for i := 0; i < 2; i++ {
		run := &store.ChatRun{SessionID: session.ID, ProjectID: project.ID, Kind: store.ChatRunSubagentResume, Status: store.ChatRunSucceeded}
		if err := db.Create(run).Error; err != nil {
			t.Fatalf("create resume run: %v", err)
		}
	}
	human := &store.ChatRun{SessionID: session.ID, ProjectID: project.ID, Kind: store.ChatRunMessage, Status: store.ChatRunSucceeded}
	if err := db.Create(human).Error; err != nil {
		t.Fatalf("create human run: %v", err)
	}
	for i := 0; i < 3; i++ {
		run := &store.ChatRun{SessionID: session.ID, ProjectID: project.ID, Kind: store.ChatRunSubagentResume, Status: store.ChatRunSucceeded}
		if err := db.Create(run).Error; err != nil {
			t.Fatalf("create trailing resume run: %v", err)
		}
	}

	d := &Dispatcher{db: db}
	count, err := d.consecutiveResumes(session.ID)
	if err != nil {
		t.Fatalf("consecutiveResumes: %v", err)
	}
	if count != 3 {
		t.Fatalf("consecutiveResumes = %d, want 3", count)
	}
}
