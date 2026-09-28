package subagentexec

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/subagent"
	"gorm.io/datatypes"
)

func TestLiveChildProgressAndSteering(t *testing.T) {
	db, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "live-child.db"))
	if err != nil {
		t.Fatal(err)
	}
	project := store.Project{Name: "live-child"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	child := store.Session{ProjectID: project.ID, SourceConcierge: "test", Settings: datatypes.NewJSONType(store.SessionSettings{Identity: "test"})}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	chatRuns := chatrun.New(db)
	t.Cleanup(chatRuns.Shutdown)
	executor := NewPipelineExecutor(db, nil, nil, chatRuns, nil)

	if _, active, err := executor.ChildProgress(child.ID); err != nil || active {
		t.Fatalf("idle child progress active=%v err=%v", active, err)
	}
	if _, err := executor.SteerChild(child.ID, "x", false); !errors.Is(err, subagent.ErrChildStarting) {
		t.Fatalf("steer child without chat run err = %v, want ErrChildStarting", err)
	}

	started := make(chan struct{})
	run, err := chatRuns.StartSubagent(child.ID, project.ID, 1, nil, func(ctx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
		onDelta(chat.StreamEvent{Type: "delta", Text: "partial "})
		onDelta(chat.StreamEvent{Type: "delta", Text: "answer"})
		close(started)
		<-ctx.Done()
		return &chatrun.Result{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	deadline := time.After(3 * time.Second)
	for {
		if content, active, err := executor.ChildProgress(child.ID); err != nil {
			t.Fatal(err)
		} else if active && content == "partial answer" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("child run never reported its streamed content")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if outcome, err := executor.SteerChild(child.ID, "first", false); err != nil || outcome != "queued" {
		t.Fatalf("first steer = %q, %v", outcome, err)
	}
	if outcome, err := executor.SteerChild(child.ID, "second", true); err != nil || outcome != "replaced" {
		t.Fatalf("second steer = %q, %v", outcome, err)
	}
	pending, ok, err := chatRuns.GetSteering(child.ID)
	if err != nil || !ok || pending.Text != "second" {
		t.Fatalf("pending steering = %+v ok=%v err=%v", pending, ok, err)
	}

	if err := db.Model(&store.ChatRun{}).Where("id = ?", run.ID).Update("status", store.ChatRunCancelling).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := executor.SteerChild(child.ID, "late", false); !errors.Is(err, subagent.ErrRunFinished) {
		t.Fatalf("steer cancelling child err = %v, want ErrRunFinished", err)
	}
	if err := db.Model(&store.ChatRun{}).Where("id = ?", run.ID).Update("status", store.ChatRunRunning).Error; err != nil {
		t.Fatal(err)
	}

	// Stopping a steered child must not replay the unclaimed steering as an
	// untracked successor turn.
	if err := chatRuns.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := chatRuns.Wait(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	successor, started2, err := chatRuns.StartSteeringFallback(run.ID, child.ID, project.ID, nil, func(string) chatrun.Execute {
		t.Fatal("fallback executor built for a subagent child run")
		return nil
	})
	if err != nil || started2 || successor != nil {
		t.Fatalf("fallback = %+v started=%v err=%v, want none", successor, started2, err)
	}
	// A child whose chat run already ended is finished, not still starting.
	if _, err := executor.SteerChild(child.ID, "late", false); !errors.Is(err, subagent.ErrRunFinished) {
		t.Fatalf("steer ended child err = %v, want ErrRunFinished", err)
	}
	// The unclaimed instruction is left for the executor to report.
	if text, ok := chatRuns.TakeSubagentSteering(run.ID); !ok || text != "second" {
		t.Fatalf("take steering = %q ok=%v, want second", text, ok)
	}
	if _, ok := chatRuns.TakeSubagentSteering(run.ID); ok {
		t.Fatal("steering taken twice")
	}
}
