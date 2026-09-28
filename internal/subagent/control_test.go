package subagent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// partialExecutor blocks until cancelled or released, then reports partial output.
type partialExecutor struct {
	started chan uint
	release chan struct{}
}

func (e partialExecutor) ExecuteSubagent(ctx context.Context, run *store.SubagentRun) (uint, string, error) {
	e.started <- run.ID
	select {
	case <-ctx.Done():
		return 0, "partial work", ctx.Err()
	case <-e.release:
		return 0, "done: " + run.Label, nil
	}
}

type fakeLiveChild struct {
	mu      sync.Mutex
	steered []string
}

func (f *fakeLiveChild) ChildProgress(uint) (string, bool, error) { return "working on it", true, nil }

func (f *fakeLiveChild) SteerChild(_ uint, text string, aggressive bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steered = append(f.steered, text)
	if aggressive {
		return "replaced", nil
	}
	return "queued", nil
}

type controlFixture struct {
	db       *gorm.DB
	service  *Service
	exec     partialExecutor
	project  store.Project
	session  store.Session
	other    store.Session
	liveTest *fakeLiveChild
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	db, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "subagent-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &controlFixture{db: db, project: store.Project{Name: "subagent-control"}}
	if err := db.Create(&f.project).Error; err != nil {
		t.Fatal(err)
	}
	for _, session := range []*store.Session{&f.session, &f.other} {
		*session = store.Session{ProjectID: f.project.ID, SourceConcierge: "test", Settings: datatypes.NewJSONType(store.SessionSettings{Identity: "test"})}
		if err := db.Create(session).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.service = New(db)
	f.exec = partialExecutor{started: make(chan uint, 8), release: make(chan struct{})}
	f.service.SetExecutor(f.exec)
	f.liveTest = &fakeLiveChild{}
	f.service.SetLiveChild(f.liveTest)
	t.Cleanup(func() {
		f.service.Shutdown()
	})
	return f
}

func (f *controlFixture) spawn(t *testing.T, sessionID uint, parentRunID *uint, label string) *store.SubagentRun {
	t.Helper()
	run, err := f.service.StartSpawn(context.Background(), Request{
		ParentSessionID: sessionID, ParentRunID: parentRunID, ProjectID: f.project.ID,
		Category: store.SubagentCategoryGeneral, Label: label, Prompt: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-f.exec.started
	return run
}

func (f *controlFixture) waitTerminal(t *testing.T, runID uint) *store.SubagentRun {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		run, err := f.service.Get(runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Terminal() {
			return run
		}
		select {
		case <-deadline:
			t.Fatalf("run %d did not finish", runID)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestOwnershipHidesOtherAgentsRuns(t *testing.T) {
	f := newControlFixture(t)
	run := f.spawn(t, f.session.ID, nil, "mine")
	childSession := uint(999)
	if err := f.db.Model(run).Update("child_session_id", childSession).Error; err != nil {
		t.Fatal(err)
	}
	sibling := run.ID

	cases := map[string]struct {
		sessionID   uint
		parentRunID *uint
	}{
		"other session":     {f.other.ID, nil},
		"subagent of owner": {f.session.ID, &sibling},
	}
	for name, c := range cases {
		if _, err := f.service.Status(run.ID, c.sessionID, c.parentRunID); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s: Status err = %v, want ErrRunNotFound", name, err)
		}
		if _, err := f.service.Steer(run.ID, c.sessionID, c.parentRunID, "x", false); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s: Steer err = %v, want ErrRunNotFound", name, err)
		}
		if err := f.service.StopOwned(run.ID, c.sessionID, c.parentRunID); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s: StopOwned err = %v, want ErrRunNotFound", name, err)
		}
		runs, err := f.service.ListOwned(c.sessionID, c.parentRunID, false)
		if err != nil || len(runs) != 0 {
			t.Errorf("%s: ListOwned = %d runs, %v; want none", name, len(runs), err)
		}
	}
	if _, err := f.service.AwaitOwned(context.Background(), f.other.ID, nil, []uint{run.ID}, 0); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("AwaitOwned from other session err = %v, want ErrRunNotFound", err)
	}
}

func TestListStatusSteerAndStop(t *testing.T) {
	f := newControlFixture(t)
	first := f.spawn(t, f.session.ID, nil, "first")
	second := f.spawn(t, f.session.ID, nil, "second")

	if _, err := f.service.Steer(first.ID, f.session.ID, nil, "focus", false); !errors.Is(err, ErrChildStarting) {
		t.Fatalf("Steer without child err = %v, want ErrChildStarting", err)
	}
	if err := f.db.Model(first).Update("child_session_id", uint(4242)).Error; err != nil {
		t.Fatal(err)
	}
	outcome, err := f.service.Steer(first.ID, f.session.ID, nil, "focus on tests", true)
	if err != nil || outcome != "replaced" {
		t.Fatalf("Steer = %q, %v", outcome, err)
	}
	status, err := f.service.Status(first.ID, f.session.ID, nil)
	if err != nil || status.Progress != "working on it" {
		t.Fatalf("Status progress = %q, %v", status.Progress, err)
	}

	if err := f.service.StopOwned(first.ID, f.session.ID, nil); err != nil {
		t.Fatal(err)
	}
	stopped := f.waitTerminal(t, first.ID)
	if stopped.Status != store.SubagentRunCancelled || stopped.Result != "partial work" {
		t.Fatalf("stopped run = %s %q, want cancelled with partial result", stopped.Status, stopped.Result)
	}
	if _, err := f.service.Steer(first.ID, f.session.ID, nil, "late", false); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("Steer finished run err = %v, want ErrRunFinished", err)
	}
	status, err = f.service.Status(first.ID, f.session.ID, nil)
	if err != nil || status.Progress != "" || status.Run.Result != "partial work" {
		t.Fatalf("finished Status = %+v, %v", status, err)
	}

	all, err := f.service.ListOwned(f.session.ID, nil, false)
	if err != nil || len(all) != 2 || all[0].ID != second.ID {
		t.Fatalf("ListOwned all = %v, %v; want newest first", all, err)
	}
	active, err := f.service.ListOwned(f.session.ID, nil, true)
	if err != nil || len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("ListOwned active = %v, %v", active, err)
	}
}

func TestAwaitOwnedTimeoutReturnsRunningWithoutConsuming(t *testing.T) {
	f := newControlFixture(t)
	run := f.spawn(t, f.session.ID, nil, "slow")

	runs, err := f.service.AwaitOwned(context.Background(), f.session.ID, nil, []uint{run.ID}, 20*time.Millisecond)
	if err != nil || len(runs) != 1 || runs[0].Terminal() {
		t.Fatalf("timed-out await = %v, %v; want one running run", runs, err)
	}

	close(f.exec.release)
	runs, err = f.service.AwaitOwned(context.Background(), f.session.ID, nil, []uint{run.ID}, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != store.SubagentRunSucceeded || runs[0].Result != "done: slow" {
		t.Fatalf("await = %v, %v; want succeeded result", runs, err)
	}
	var event store.SubagentEvent
	if err := f.db.Where("run_id = ?", run.ID).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.ConsumedAt == nil {
		t.Fatal("finished run's completion event should be consumed by await")
	}
}
