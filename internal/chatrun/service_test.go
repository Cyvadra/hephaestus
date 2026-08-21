package chatrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var nextTestProjectID atomic.Uint64

func newTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("HEPHAESTUS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("HEPHAESTUS_TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open Postgres: %v", err)
	}
	return New(db), db
}

func testProjectID() uint {
	return uint(900000000 + nextTestProjectID.Add(1))
}

func cleanupProjectRuns(t *testing.T, db *gorm.DB, projectID uint) {
	t.Helper()
	t.Cleanup(func() {
		var runIDs []uint
		db.Model(&store.ChatRun{}).Where("project_id = ?", projectID).Pluck("id", &runIDs)
		if len(runIDs) > 0 {
			db.Where("run_id IN ?", runIDs).Delete(&store.ChatRunEvent{})
		}
		db.Where("project_id = ?", projectID).Delete(&store.ChatRun{})
	})
}

func TestStartPersistsFinalResultAndSnapshot(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)
	messageID := uint(42)
	run, err := svc.Start(1, projectID, store.ChatRunMessage, map[string]any{"text": "hello"}, func(_ context.Context, onDelta func(chat.StreamEvent)) (*Result, error) {
		onDelta(chat.StreamEvent{Type: "delta", Text: "hi"})
		return &Result{FinalMessageID: &messageID, Response: map[string]any{"ok": true}}, nil
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	final := waitForTerminalRun(t, svc, run.ID)
	if final.Status != store.ChatRunSucceeded {
		t.Fatalf("expected succeeded run, got %s", final.Status)
	}
	if final.FinalMessageID == nil || *final.FinalMessageID != messageID {
		t.Fatalf("expected final message id %d, got %v", messageID, final.FinalMessageID)
	}
	if final.Snapshot.Data().Content != "hi" {
		t.Fatalf("expected snapshot content hi, got %q", final.Snapshot.Data().Content)
	}
	var response map[string]bool
	if err := json.Unmarshal(final.Result, &response); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !response["ok"] {
		t.Fatalf("expected persisted result, got %s", string(final.Result))
	}
}

func TestIsActiveRunConflictRecognizesPostgresConstraint(t *testing.T) {
	err := &pgconn.PgError{Code: "23505", ConstraintName: "idx_chat_runs_active_session"}
	if !isActiveRunConflict(err) {
		t.Fatal("expected active-run constraint to be recognized")
	}
	other := &pgconn.PgError{Code: "23505", ConstraintName: "other_constraint"}
	if isActiveRunConflict(other) {
		t.Fatal("did not expect unrelated unique constraint to be recognized")
	}
	if isActiveRunConflict(errors.New("duplicate")) {
		t.Fatal("did not expect generic error to be recognized")
	}
}

func TestPublishDisconnectsLaggingSubscriber(t *testing.T) {
	svc := &Service{subs: map[uint]map[chan ProgressEvent]struct{}{}}
	ch := make(chan ProgressEvent, 1)
	ch <- ProgressEvent{Sequence: 1}
	svc.subs[7] = map[chan ProgressEvent]struct{}{ch: {}}

	svc.publish(7, ProgressEvent{Sequence: 2})

	if _, ok := svc.subs[7]; ok {
		t.Fatal("lagging subscriber remained registered")
	}
	<-ch
	if _, ok := <-ch; ok {
		t.Fatal("lagging subscriber channel remained open")
	}
}

func TestActiveForSessionReturnsNotFoundWhenNoRunIsActive(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)

	_, err := svc.ActiveForSession(projectID)
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected no active run to return ErrRunNotFound, got %v", err)
	}
}

func TestStartRejectsConcurrentRunForSession(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)
	block := make(chan struct{})
	first, err := svc.Start(projectID, projectID, store.ChatRunMessage, nil, func(context.Context, func(chat.StreamEvent)) (*Result, error) {
		<-block
		return nil, nil
	})
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}
	_, err = svc.Start(projectID, projectID, store.ChatRunMessage, nil, func(context.Context, func(chat.StreamEvent)) (*Result, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrRunActive) {
		t.Fatalf("expected active run conflict, got %v", err)
	}
	close(block)
	_ = waitForTerminalRun(t, svc, first.ID)
}

func TestCancelledRunInvokesRunEnded(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)
	var callbackCount atomic.Int32
	svc.SetOnRunEnded(func(_ uint, _ store.ChatRunStatus) {
		callbackCount.Add(1)
	})
	run, err := svc.Start(projectID, projectID, store.ChatRunMessage, nil, func(ctx context.Context, _ func(chat.StreamEvent)) (*Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := svc.Cancel(run.ID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	final := waitForTerminalRun(t, svc, run.ID)
	if final.Status != store.ChatRunCancelled {
		t.Fatalf("expected cancelled run, got %s", final.Status)
	}
	if callbackCount.Load() != 1 {
		t.Fatalf("cancelled run invoked onRunEnded %d times, want 1", callbackCount.Load())
	}
}

func TestExecutePersistsCancellationWhenExecutorReturnsSuccess(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)
	started := make(chan struct{})
	proceed := make(chan struct{})
	run, err := svc.Start(projectID, projectID, store.ChatRunMessage, nil, func(context.Context, func(chat.StreamEvent)) (*Result, error) {
		close(started)
		<-proceed
		return &Result{}, nil
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	<-started
	if err := svc.Cancel(run.ID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	close(proceed)
	if final := waitForTerminalRun(t, svc, run.ID); final.Status != store.ChatRunCancelled {
		t.Fatalf("run status = %s, want cancelled", final.Status)
	}
}

func TestReconcileRestoresInterruptedSnapshotToHistory(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)
	project := store.Project{ID: projectID, Name: fmt.Sprintf("chatrun-reconcile-%d", projectID)}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	session := store.Session{ProjectID: projectID, SourceConcierge: "test"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	prompt := store.ChatMessage{SessionID: session.ID, Role: "user", Content: "question", Status: store.MessageStatusComplete, Timestamp: time.Now()}
	if err := db.Create(&prompt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&session).Update("active_leaf_message_id", prompt.ID).Error; err != nil {
		t.Fatal(err)
	}
	subagentRunID := uint(77)
	run := store.ChatRun{SessionID: session.ID, ProjectID: projectID, SubagentRunID: &subagentRunID, Kind: store.ChatRunMessage, Status: store.ChatRunRunning}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(chat.StreamEvent{Type: "delta", Text: "partial answer"})
	if err := db.Create(&store.ChatRunEvent{RunID: run.ID, Sequence: 1, Type: "delta", Payload: payload}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Where("session_id = ?", session.ID).Delete(&store.ChatMessage{})
		db.Delete(&store.Session{}, session.ID)
		db.Delete(&store.Project{}, projectID)
	})

	if err := svc.Reconcile(); err != nil {
		t.Fatal(err)
	}
	var restored store.ChatMessage
	if err := db.Where("session_id = ? AND role = ?", session.ID, "assistant").First(&restored).Error; err != nil {
		t.Fatal(err)
	}
	if restored.Content != "partial answer" || restored.Status != store.MessageStatusIncomplete || restored.ParentMessageID == nil || *restored.ParentMessageID != prompt.ID {
		t.Fatalf("restored message = %+v", restored)
	}
	terminal, err := svc.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != store.ChatRunInterrupted {
		t.Fatalf("run status = %s, want interrupted", terminal.Status)
	}
	childID, result, recovered, err := svc.InterruptedSubagentResult(subagentRunID)
	if err != nil || !recovered || childID != session.ID || result != "partial answer" {
		t.Fatalf("recovered child=%d result=%q recovered=%v err=%v", childID, result, recovered, err)
	}
}

func TestReconcileDoesNotRestoreOrdinaryRunSnapshotToHistory(t *testing.T) {
	svc, db := newTestService(t)
	projectID := testProjectID()
	cleanupProjectRuns(t, db, projectID)
	project := store.Project{ID: projectID, Name: fmt.Sprintf("chatrun-ordinary-reconcile-%d", projectID)}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	session := store.Session{ProjectID: projectID, SourceConcierge: "test"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	run := store.ChatRun{SessionID: session.ID, ProjectID: projectID, Kind: store.ChatRunMessage, Status: store.ChatRunRunning}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(chat.StreamEvent{Type: "delta", Text: "partial ordinary answer"})
	if err := db.Create(&store.ChatRunEvent{RunID: run.ID, Sequence: 1, Type: "delta", Payload: payload}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Where("session_id = ?", session.ID).Delete(&store.ChatMessage{})
		db.Delete(&store.Session{}, session.ID)
		db.Delete(&store.Project{}, projectID)
	})

	if err := svc.Reconcile(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&store.ChatMessage{}).Where("session_id = ?", session.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ordinary run restored %d messages, want none", count)
	}
	terminal, err := svc.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != store.ChatRunInterrupted {
		t.Fatalf("run status = %s, want interrupted", terminal.Status)
	}
}

func waitForTerminalRun(t *testing.T, svc *Service, runID uint) *store.ChatRun {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for terminal run")
		case <-ticker.C:
			run, err := svc.Get(runID)
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			if run.Status.IsTerminal() {
				return run
			}
		}
	}
}
