package job

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/workflow"
	"gorm.io/gorm"
)

// fakeRunner is a deterministic workflow runner for job orchestration tests.
type fakeRunner struct {
	mu      sync.Mutex
	replies []string
	errs    []error
	block   bool // block the next Run until ctx is canceled
	got     int
}

func (f *fakeRunner) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	f.mu.Lock()
	index := f.got
	f.got++
	if f.block {
		f.block = false
		f.mu.Unlock()
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}
	err := error(nil)
	content := ""
	if index < len(f.errs) {
		err = f.errs[index]
	}
	if index < len(f.replies) {
		content = f.replies[index]
	}
	f.mu.Unlock()
	if err != nil {
		return agent.Result{}, err
	}
	msg := store.ChatMessage{Role: "assistant", Content: content, Status: store.MessageStatusComplete, Timestamp: time.Now()}
	return agent.Result{Messages: []store.ChatMessage{msg}}, nil
}

func testToolReg() *toolkit.Registry {
	reg := toolkit.NewRegistry()
	reg.Register(echoTool{})
	return reg
}

type echoTool struct{}

func (echoTool) Name() string               { return "echo" }
func (echoTool) Description() string        { return "" }
func (echoTool) Parameters() map[string]any { return nil }
func (echoTool) Execute(context.Context, map[string]any) *toolkit.ToolResult {
	return toolkit.NewToolResult("echoed")
}

func baseRegistry() *registry.Registry {
	return &registry.Registry{
		Identities: map[string]registry.Identity{
			"default": {Name: "default", ContextWindowTokens: 128000, SystemPrompt: "helpful"},
		},
		ToolGroups: map[string]registry.ToolGroup{
			"basic": {Name: "basic", Tools: []string{"echo"}},
		},
		Concierges: map[string]registry.Concierge{
			"coding": {Name: "coding", Identity: "default", ToolGroups: []string{"basic"}},
		},
		Workflows: map[string]registry.Workflow{
			"summary-workflow": {
				Name:        "summary-workflow",
				Description: "d",
				Concierge:   "coding",
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"topic": map[string]any{"type": "string"}},
					"required":   []any{"topic"},
				},
				Steps: []string{"do step one"},
			},
		},
		Jobs: map[string]registry.Job{},
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

type services struct {
	db       *gorm.DB
	regStore *registry.Store
	workflow *workflow.Service
	job      *Service
	runner   *fakeRunner
}

func newServices(t *testing.T, reg *registry.Registry, runner *fakeRunner) *services {
	t.Helper()
	db := openTestDB(t)
	regStore := registry.NewStore(reg)
	proj, err := project.New(db, t.TempDir())
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	if _, err := proj.EnsureDefault(); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	wf := workflow.NewService(db, regStore, testToolReg(), runner, proj, notify.New(""))
	jobSvc := NewService(db, regStore, wf, notify.New(""))
	return &services{db: db, regStore: regStore, workflow: wf, job: jobSvc, runner: runner}
}

func (s *services) addJob(job registry.Job) {
	reg := s.regStore.Current()
	reg.Jobs[job.Name] = job
	s.regStore.Publish(reg)
}

func claimAndRun(t *testing.T, s *services, jobName string, now time.Time) *store.JobRun {
	t.Helper()
	run, claimed, err := s.job.claim(context.Background(), s.regStore.Current(), jobName, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatalf("expected claim to succeed")
	}
	s.job.executeJob(context.Background(), s.regStore.Current(), run)
	got, err := s.job.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return got
}

func morningJob(name string, attempts int) registry.Job {
	return registry.Job{
		Name:  name,
		Title: "Morning",
		Goal:  "g",
		Workflows: []registry.JobWorkflowBinding{
			{Workflow: "summary-workflow", Project: store.DefaultProjectName, Input: map[string]any{"topic": "${job.goal}"}, MaxAttempts: attempts},
		},
		Trigger:             "true",
		MaxExecutionsPerDay: 100,
	}
}

// uniqueJobName returns a per-test unique job name so lingering goroutines
// from one integration test never collide with another test's state.
func uniqueJobName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestIntegration_ClaimRejectsOverlap(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 1)
	s := newServices(t, reg, &fakeRunner{replies: []string{"ok"}})
	now := time.Now()

	run, claimed, err := s.job.claim(context.Background(), reg, jobName, now)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, _ := s.job.claim(context.Background(), reg, jobName, now); claimed {
		t.Fatal("expected overlapping claim to be rejected")
	}
	s.job.executeJob(context.Background(), reg, run)
	if _, claimed, _ := s.job.claim(context.Background(), reg, jobName, now); !claimed {
		t.Fatal("expected claim after completion to succeed")
	}
}

func TestIntegration_ClaimHonorsDailyCapAndRollsOver(t *testing.T) {
	jobName := uniqueJobName("morning")
	job := morningJob(jobName, 1)
	job.MaxExecutionsPerDay = 2
	reg := baseRegistry()
	reg.Jobs[jobName] = job
	s := newServices(t, reg, &fakeRunner{replies: []string{"ok", "ok", "ok"}})
	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local)

	if got := claimAndRun(t, s, jobName, now); got.Status != store.JobRunSucceeded {
		t.Fatalf("expected first run succeeded, got %q", got.Status)
	}
	if got := claimAndRun(t, s, jobName, now); got.Status != store.JobRunSucceeded {
		t.Fatalf("expected second run succeeded, got %q", got.Status)
	}
	if _, claimed, _ := s.job.claim(context.Background(), reg, jobName, now); claimed {
		t.Fatal("expected daily cap to deny a third run")
	}
	// A new host-local day resets the counter.
	if got := claimAndRun(t, s, jobName, now.Add(24*time.Hour)); got.Status != store.JobRunSucceeded {
		t.Fatalf("expected next-day run succeeded, got %q", got.Status)
	}
}

func TestIntegration_JobSucceedsAfterBindingRetries(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 3)
	s := newServices(t, reg, &fakeRunner{replies: []string{"", "", "ok"}, errs: []error{context.DeadlineExceeded, context.DeadlineExceeded, nil}})

	got := claimAndRun(t, s, jobName, time.Now())
	if got.Status != store.JobRunSucceeded {
		t.Fatalf("expected succeeded run after retries, got %q (%s)", got.Status, got.Error)
	}

	var workflowRuns []store.WorkflowRun
	if err := s.db.Where("job_run_id = ?", got.ID).Order("attempt").Find(&workflowRuns).Error; err != nil {
		t.Fatalf("load workflow runs: %v", err)
	}
	if len(workflowRuns) != 3 {
		t.Fatalf("expected three workflow attempts, got %d", len(workflowRuns))
	}
	if workflowRuns[0].Attempt != 1 || workflowRuns[1].Attempt != 2 || workflowRuns[2].Attempt != 3 {
		t.Fatalf("unexpected attempt numbering: %+v", workflowRuns)
	}
}

func TestIntegration_JobContinuesPastBindingFailure(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Workflows["second-workflow"] = registry.Workflow{
		Name: "second-workflow", Description: "d2", Concierge: "coding",
		Steps: []string{"do second"},
	}
	job := morningJob(jobName, 1)
	job.Workflows = append(job.Workflows, registry.JobWorkflowBinding{
		Workflow: "second-workflow", Project: store.DefaultProjectName, MaxAttempts: 1,
	})
	reg.Jobs[jobName] = job
	s := newServices(t, reg, &fakeRunner{replies: []string{"", "ok"}, errs: []error{context.DeadlineExceeded, nil}})

	got := claimAndRun(t, s, jobName, time.Now())
	if got.Status != store.JobRunCompletedWithErrors {
		t.Fatalf("expected completed_with_errors, got %q (%s)", got.Status, got.Error)
	}

	var workflowRuns []store.WorkflowRun
	if err := s.db.Where("job_run_id = ?", got.ID).Order("binding_index").Find(&workflowRuns).Error; err != nil {
		t.Fatalf("load workflow runs: %v", err)
	}
	if len(workflowRuns) != 2 {
		t.Fatalf("expected both bindings attempted, got %d", len(workflowRuns))
	}
	if workflowRuns[0].Status != store.WorkflowRunFailed || workflowRuns[1].Status != store.WorkflowRunSucceeded {
		t.Fatalf("unexpected binding statuses: %+v", workflowRuns)
	}
}

func TestIntegration_ResolvedInputFailsSchemaIsFatal(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Workflows["summary-workflow"] = registry.Workflow{
		Name:        "summary-workflow",
		Description: "d",
		Concierge:   "coding",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"topic": map[string]any{"type": "integer"}},
			"required":   []any{"topic"},
		},
		Steps: []string{"do step"},
	}
	// ${job.goal} resolves to the string "g", which fails the integer schema.
	reg.Jobs[jobName] = morningJob(jobName, 3)
	s := newServices(t, reg, &fakeRunner{replies: []string{"ok"}})

	got := claimAndRun(t, s, jobName, time.Now())
	if got.Status != store.JobRunFailed {
		t.Fatalf("expected failed run, got %q (%s)", got.Status, got.Error)
	}
	// A fatal input failure is detected before any workflow attempt is made,
	// so no WorkflowRun row is created and nothing is retried.
	var workflowRuns []store.WorkflowRun
	if err := s.db.Where("job_run_id = ?", got.ID).Find(&workflowRuns).Error; err != nil {
		t.Fatalf("load workflow runs: %v", err)
	}
	if len(workflowRuns) != 0 {
		t.Fatalf("expected no workflow attempt for fatal input, got %+v", workflowRuns)
	}
}

func TestIntegration_CancellationDuringJobRun(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 1)
	s := newServices(t, reg, &fakeRunner{block: true})

	run, claimed, err := s.job.claim(context.Background(), reg, jobName, time.Now())
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.job.executeJob(context.Background(), reg, run)
	}()
	// Let the run reach the blocked step.
	time.Sleep(50 * time.Millisecond)
	if err := s.job.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	<-done

	got, err := s.job.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.JobRunCancelled {
		t.Fatalf("expected cancelled run, got %q", got.Status)
	}
}

func TestIntegration_ReconcileInterruptsStaleRuns(t *testing.T) {
	jobName := uniqueJobName("morning")
	reg := baseRegistry()
	reg.Jobs[jobName] = morningJob(jobName, 1)
	s := newServices(t, reg, &fakeRunner{})

	now := time.Now()
	run, claimed, err := s.job.claim(context.Background(), reg, jobName, now)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if err := s.job.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := s.job.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.JobRunInterrupted {
		t.Fatalf("expected interrupted run, got %q", got.Status)
	}
	var state store.JobState
	if err := s.db.Where("job_name = ?", jobName).First(&state).Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.ActiveRunID != nil {
		t.Fatalf("expected active claim cleared, got %v", *state.ActiveRunID)
	}
}
