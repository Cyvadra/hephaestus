package workflow

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// fakeRunner is a deterministic agent runner for workflow tests.
type fakeRunner struct {
	mu      sync.Mutex
	replies []string
	errs    []error
	got     []string // captured step prompts, in order
	// emitDelta, when true, emits a live delta through req.OnDelta before
	// returning.
	emitDelta bool
	// hold, when non-nil, blocks Run until the channel is closed (letting a
	// test subscribe before execution proceeds).
	hold chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
	}
	prompt := ""
	if n := len(req.Turn.Messages); n > 0 {
		prompt = req.Turn.Messages[n-1].Content
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	index := len(f.got)
	f.got = append(f.got, prompt)
	if index < len(f.errs) && f.errs[index] != nil {
		return agent.Result{}, f.errs[index]
	}
	content := ""
	if index < len(f.replies) {
		content = f.replies[index]
	}
	if f.emitDelta && req.OnDelta != nil {
		req.OnDelta(agent.StreamEvent{Type: "delta", Text: "live " + content})
	}
	msg := store.ChatMessage{Role: "assistant", Content: content, Status: store.MessageStatusComplete, Timestamp: time.Now()}
	return agent.Result{Messages: []store.ChatMessage{msg}, Turn: req.Turn}, nil
}

func (f *fakeRunner) prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func testRegistry() *registry.Registry {
	return &registry.Registry{
		Identities: map[string]registry.Identity{
			"default": {Name: "default", ContextWindowTokens: 128000, SystemPrompt: "You're a helpful assistant."},
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
				Description: "summarize a topic",
				Concierge:   "coding",
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"topic": map[string]any{"type": "string"}},
					"required":   []any{"topic"},
				},
				OutputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"summary": map[string]any{"type": "string"}},
					"required":   []any{"summary"},
				},
				Steps: []string{"Summarize the topic", "Produce the JSON output"},
			},
		},
		Jobs: map[string]registry.Job{},
	}
}

func testToolReg() *toolkit.Registry {
	reg := toolkit.NewRegistry()
	reg.Register(echoTool{name: "echo"})
	return reg
}

type echoTool struct{ name string }

func (echoTool) Name() string               { return "echo" }
func (echoTool) Description() string        { return "" }
func (echoTool) Parameters() map[string]any { return nil }
func (echoTool) Execute(context.Context, map[string]any) *toolkit.ToolResult {
	return toolkit.NewToolResult("echoed")
}

// --- Pure helper tests (no database) ---

func TestBuildStepPrompt_IncludesContextAndPriorOutputs(t *testing.T) {
	wf := registry.Workflow{Name: "summary-workflow", Description: "summarize a topic", Steps: []string{"s1", "s2"}}
	prompt := buildStepPrompt(wf, map[string]any{"topic": "go"}, []string{"prior output"}, 1)
	for _, want := range []string{"summary-workflow", "summarize a topic", `"topic":"go"`, "prior output", "Current step 2 of 2: s2"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
}

func TestNormalizeFinalOutput_WithoutSchemaWrapsTextAsJSON(t *testing.T) {
	raw, err := normalizeFinalOutput("plain text result", registry.Workflow{})
	if err != nil {
		t.Fatalf("normalizeFinalOutput: %v", err)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("expected JSON string wrapper, got %s: %v", raw, err)
	}
	if got != "plain text result" {
		t.Fatalf("expected wrapped text, got %q", got)
	}
}

func TestNormalizeFinalOutput_ValidatesAgainstSchema(t *testing.T) {
	wf := registry.Workflow{OutputSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"summary": map[string]any{"type": "string"}},
		"required":   []any{"summary"},
	}}
	raw, err := normalizeFinalOutput(`{"summary":"done"}`, wf)
	if err != nil {
		t.Fatalf("normalizeFinalOutput: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["summary"] != "done" {
		t.Fatalf("unexpected output: %v", got)
	}
}

func TestNormalizeFinalOutput_RejectsInvalidJSONWhenSchemaSet(t *testing.T) {
	wf := registry.Workflow{OutputSchema: map[string]any{"type": "object"}}
	if _, err := normalizeFinalOutput("not json", wf); err == nil {
		t.Fatal("expected non-JSON final output to fail")
	}
	if _, err := normalizeFinalOutput(`{"summary":"done"}`, registry.Workflow{OutputSchema: map[string]any{
		"type":       "object",
		"required":   []any{"summary"},
		"properties": map[string]any{"summary": map[string]any{"type": "number"}},
	}}); err == nil {
		t.Fatal("expected schema mismatch to fail")
	}
}

func TestClassifyStatus_CancelledWhenCtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, _ := classifyStatus(ctx, context.Canceled)
	if status != store.WorkflowRunCancelled {
		t.Fatalf("expected cancelled status, got %q", status)
	}
}

func TestClassifyStatus_RetryableFailure(t *testing.T) {
	status, _ := classifyStatus(context.Background(), context.DeadlineExceeded)
	if status != store.WorkflowRunFailed {
		t.Fatalf("expected failed status, got %q", status)
	}
}

// --- Database-gated integration tests ---

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

func newTestService(t *testing.T, db *gorm.DB, runner *fakeRunner) *Service {
	t.Helper()
	regStore := registry.NewStore(testRegistry())
	proj, err := project.New(db, t.TempDir())
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	if _, err := proj.EnsureDefault(); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	return NewService(db, regStore, testToolReg(), runner, proj, notify.New(""))
}

func waitForTerminalRun(t *testing.T, db *gorm.DB, runID uint) store.WorkflowRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var run store.WorkflowRun
		if err := db.First(&run, runID).Error; err != nil {
			t.Fatalf("load run %d: %v", runID, err)
		}
		switch run.Status {
		case store.WorkflowRunSucceeded, store.WorkflowRunFailed, store.WorkflowRunFatal, store.WorkflowRunCancelled, store.WorkflowRunInterrupted:
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %d did not reach a terminal status", runID)
	return store.WorkflowRun{}
}

func TestIntegration_WorkflowRunEndToEnd(t *testing.T) {
	db := openTestDB(t)
	runner := &fakeRunner{
		replies: []string{"the topic is about go", `{"summary":"go summary"}`},
	}
	svc := newTestService(t, db, runner)

	run, err := svc.Start("summary-workflow", store.DefaultProjectName, map[string]any{"topic": "go"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitForTerminalRun(t, db, run.ID)
	if finished.Status != store.WorkflowRunSucceeded {
		t.Fatalf("expected succeeded run, got %q (%s)", finished.Status, finished.Error)
	}

	got, steps, err := svc.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected two step runs, got %d", len(steps))
	}
	for _, step := range steps {
		if step.Status != store.WorkflowRunSucceeded {
			t.Fatalf("expected step %d succeeded, got %q", step.Index, step.Status)
		}
	}
	var output map[string]any
	if err := json.Unmarshal(got.Output, &output); err != nil {
		t.Fatalf("expected normalized JSON output, got %s", got.Output)
	}
	if output["summary"] != "go summary" {
		t.Fatalf("unexpected output: %v", output)
	}
	// The second step's prompt must include the first step's output.
	prompts := runner.prompts()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "the topic is about go") {
		t.Fatalf("expected prior step output propagated, got %v", prompts)
	}
}

func TestIntegration_WorkflowStepFailureStopsRun(t *testing.T) {
	db := openTestDB(t)
	runner := &fakeRunner{
		replies: []string{"ok", "never reached"},
		errs:    []error{nil, context.DeadlineExceeded},
	}
	svc := newTestService(t, db, runner)

	run, err := svc.Start("summary-workflow", store.DefaultProjectName, map[string]any{"topic": "go"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitForTerminalRun(t, db, run.ID)
	if finished.Status != store.WorkflowRunFailed {
		t.Fatalf("expected failed run, got %q", finished.Status)
	}

	_, steps, err := svc.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected the second step to be recorded, got %d", len(steps))
	}
	if steps[1].Status != store.WorkflowRunFailed {
		t.Fatalf("expected step 2 failed, got %q", steps[1].Status)
	}
}

func TestIntegration_WorkflowRunCancellation(t *testing.T) {
	db := openTestDB(t)
	runner := &fakeRunner{
		replies: []string{"blocked"},
		errs:    []error{context.Canceled},
	}
	svc := newTestService(t, db, runner)

	run, err := svc.Start("summary-workflow", store.DefaultProjectName, map[string]any{"topic": "go"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := svc.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	finished := waitForTerminalRun(t, db, run.ID)
	if finished.Status != store.WorkflowRunCancelled {
		t.Fatalf("expected cancelled run, got %q (%s)", finished.Status, finished.Error)
	}
}

// TestIntegration_ExecuteRunCancellableLikeManualRun guards the scheduled-run
// cancel path: a run executed via ExecuteRun (as the job orchestrator does)
// must be cancellable through Cancel, not reported as not-found.
func TestIntegration_ExecuteRunCancellableLikeManualRun(t *testing.T) {
	db := openTestDB(t)
	runner := &fakeRunner{hold: make(chan struct{})}
	svc := newTestService(t, db, runner)

	reg := testRegistry()
	wf := reg.Workflows["summary-workflow"]
	inputJSON, err := json.Marshal(map[string]any{"topic": "go"})
	if err != nil {
		t.Fatal(err)
	}
	run := &store.WorkflowRun{
		WorkflowName: wf.Name,
		Concierge:    wf.Concierge,
		ProjectName:  store.DefaultProjectName,
		Workflow:     datatypes.NewJSONType(wf),
		Input:        datatypes.JSON(inputJSON),
		Attempt:      1,
		Status:       store.WorkflowRunPending,
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.ExecuteRun(context.Background(), run.ID, reg)
	}()

	// Wait until the executor has marked the run running (the cancel handle is
	// registered before that) so Cancel is deterministic.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var r store.WorkflowRun
		if err := db.First(&r, run.ID).Error; err == nil && r.Status == store.WorkflowRunRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := svc.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel scheduled run: %v", err)
	}
	<-done

	got, _, err := svc.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.WorkflowRunCancelled {
		t.Fatalf("expected cancelled run, got %q (%s)", got.Status, got.Error)
	}
}

func TestIntegration_WorkflowInputInvalidRejected(t *testing.T) {
	db := openTestDB(t)
	svc := newTestService(t, db, &fakeRunner{})

	if _, err := svc.Start("summary-workflow", store.DefaultProjectName, map[string]any{}); err == nil {
		t.Fatal("expected invalid input (missing required topic) to be rejected")
	}
	if _, err := svc.Start("missing-workflow", store.DefaultProjectName, nil); err == nil {
		t.Fatal("expected unknown workflow to be rejected")
	}
}

func TestIntegration_WorkflowRunStreamsProgressEvents(t *testing.T) {
	db := openTestDB(t)
	hold := make(chan struct{})
	runner := &fakeRunner{replies: []string{"first", `{"summary":"done"}`}, emitDelta: true, hold: hold}
	svc := newTestService(t, db, runner)

	run, err := svc.Start("summary-workflow", store.DefaultProjectName, map[string]any{"topic": "go"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	sub, err := svc.Subscribe(run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	close(hold) // let execution proceed now that we are subscribed

	var sawRunning, sawStep, sawDelta, sawDone bool
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				break loop
			}
			switch ev.Type {
			case ProgressRun:
				if ev.Run != nil && ev.Run.Status == store.WorkflowRunRunning {
					sawRunning = true
				}
				if ev.Run != nil && ev.Run.Status == store.WorkflowRunSucceeded {
					sawDone = true
				}
			case ProgressStep:
				sawStep = true
			case ProgressDelta:
				if ev.Delta != nil && ev.Delta.Text == "live first" {
					sawDelta = true
				}
			}
		case <-deadline:
			t.Fatal("timeout waiting for workflow progress events")
		}
	}
	if !sawRunning || !sawStep || !sawDelta || !sawDone {
		t.Fatalf("expected running/step/delta/done events, got running=%v step=%v delta=%v done=%v", sawRunning, sawStep, sawDelta, sawDone)
	}
}
