package subagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

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

func newTestRun(t *testing.T, db *gorm.DB, schedule store.SubagentSchedule, status store.SubagentRunStatus) *store.SubagentRun {
	t.Helper()
	var project store.Project
	if err := db.Where("name = ?", store.DefaultProjectName).First(&project).Error; err != nil {
		t.Fatalf("load default project: %v", err)
	}
	marker := fmt.Sprintf("subagent-test-%d", time.Now().UnixNano())
	session := store.Session{
		ProjectID: project.ID, SourceConcierge: marker,
		Settings: datatypes.NewJSONType(store.SessionSettings{Identity: "test"}),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	run := &store.SubagentRun{
		ParentSessionID: session.ID, ProjectID: project.ID, Mode: store.SubagentModeSpawn,
		Schedule: schedule, Status: status, Depth: 1, Label: marker, Prompt: "test",
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() {
		db.Where("run_id = ?", run.ID).Delete(&store.SubagentEvent{})
		db.Delete(&store.SubagentRun{}, run.ID)
		db.Delete(&store.Session{}, session.ID)
	})
	return run
}

func TestFinishCreatesBackgroundEvent(t *testing.T) {
	db := openTestDB(t)
	service := New(db, 2)
	run := newTestRun(t, db, store.SubagentScheduleBackground, store.SubagentRunRunning)

	if err := service.finish(run, store.SubagentRunSucceeded, 0, "completed", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	persisted, err := service.Get(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if persisted.Status != store.SubagentRunSucceeded || persisted.Result != "completed" {
		t.Fatalf("persisted run = %+v", persisted)
	}
	var count int64
	if err := db.Model(&store.SubagentEvent{}).Where("run_id = ?", run.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("completion event count = %d, want 1", count)
	}
}

func TestNotificationLeaseCanReleaseAndAcknowledge(t *testing.T) {
	db := openTestDB(t)
	service := New(db, 2)
	run := newTestRun(t, db, store.SubagentScheduleBackground, store.SubagentRunRunning)
	if err := service.finish(run, store.SubagentRunSucceeded, 0, "completed", nil); err != nil {
		t.Fatal(err)
	}

	claimed, err := service.ClaimNotifications(run.ParentSessionID)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim = %+v, err = %v", claimed, err)
	}
	if err := service.ReleaseNotifications([]uint{claimed[0].ID}); err != nil {
		t.Fatal(err)
	}
	claimedAgain, err := service.ClaimNotifications(run.ParentSessionID)
	if err != nil || len(claimedAgain) != 1 || claimedAgain[0].ID != claimed[0].ID {
		t.Fatalf("second claim = %+v, err = %v", claimedAgain, err)
	}
	if err := service.AcknowledgeNotifications([]uint{claimedAgain[0].ID}); err != nil {
		t.Fatal(err)
	}
	claimedAfterAck, err := service.ClaimNotifications(run.ParentSessionID)
	if err != nil || len(claimedAfterAck) != 0 {
		t.Fatalf("claim after acknowledgement = %+v, err = %v", claimedAfterAck, err)
	}
}

func TestReconcileCreatesMissingTerminalEvent(t *testing.T) {
	db := openTestDB(t)
	service := New(db, 2)
	run := newTestRun(t, db, store.SubagentScheduleBackground, store.SubagentRunSucceeded)
	run.Result = "legacy result"
	if err := db.Model(run).Update("result", run.Result).Error; err != nil {
		t.Fatal(err)
	}

	if err := service.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var event store.SubagentEvent
	if err := db.Where("run_id = ?", run.ID).First(&event).Error; err != nil {
		t.Fatalf("load repaired event: %v", err)
	}
	if len(event.Payload) == 0 {
		t.Fatal("repaired event has empty payload")
	}
}

func TestCreateRejectsMaximumDepth(t *testing.T) {
	service := New(nil, 2)
	_, err := service.create(Request{Depth: 2}, store.SubagentModeSpawn, store.SubagentScheduleBackground)
	if !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("create error = %v, want ErrMaxDepth", err)
	}
}

func TestMarshalSeedEnforcesByteLimit(t *testing.T) {
	seed, err := marshalSeed([]store.ChatMessage{
		{Role: "user", Content: strings.Repeat("a", maxSeedBytes)},
		{Role: "assistant", Content: strings.Repeat("b", maxSeedBytes)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) > maxSeedBytes {
		t.Fatalf("seed length = %d, limit = %d", len(seed), maxSeedBytes)
	}
	if !json.Valid(seed) {
		t.Fatalf("seed is not valid JSON: %q", seed)
	}
}
