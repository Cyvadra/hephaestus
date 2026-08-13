package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/runctrl"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/gorm"
)

var (
	ErrRunNotFound      = errors.New("workflow: run not found")
	ErrRunFinished      = errors.New("workflow: run already finished")
	ErrWorkflowNotFound = errors.New("workflow: workflow not found")
	ErrProjectNotFound  = errors.New("workflow: project not found")
	ErrInvalidInput     = errors.New("workflow: invalid input")
)

// Runner is the agent-loop contract the workflow executor needs, satisfied
// by *agent.Runner and fakeable in tests.
type Runner interface {
	Run(ctx context.Context, req agent.Request) (agent.Result, error)
}

// ProgressEventType names the kinds of live progress updates a run emits.
type ProgressEventType string

const (
	// ProgressRun carries a full run snapshot (status/fields changed).
	ProgressRun ProgressEventType = "run"
	// ProgressStep carries a step lifecycle update (started/finished).
	ProgressStep ProgressEventType = "step"
	// ProgressDelta carries an agent stream delta for the current step.
	ProgressDelta ProgressEventType = "delta"
	// ProgressDone marks the end of a run's progress stream.
	ProgressDone ProgressEventType = "done"
)

// ProgressEvent is one live progress update for a workflow run, delivered to
// subscribers and streamed to clients via SSE.
type ProgressEvent struct {
	Type  ProgressEventType      `json:"type"`
	Run   *store.WorkflowRun     `json:"run,omitempty"`
	Step  *store.WorkflowStepRun `json:"step,omitempty"`
	Delta *agent.StreamEvent     `json:"delta,omitempty"`
}

// Subscription is a live progress subscription for one workflow run. Close
// must be called to release the subscription.
type Subscription struct {
	Events <-chan ProgressEvent
	Close  func()
}

// Service executes Workflows durably. Each run captures one immutable
// registry snapshot at creation; configuration published later never alters
// an in-flight run.
type Service struct {
	db         *gorm.DB
	registries *registry.Store
	toolReg    *toolkit.Registry
	runner     Runner
	projects   *project.Service
	notify     *notify.Notifier

	// mu guards subs (live progress subscribers). Run cancellation lives in
	// ctrl (runctrl.Controller).
	mu   sync.Mutex
	ctrl *runctrl.Controller
	subs map[uint]map[chan ProgressEvent]struct{}
}

// NewService wires the workflow executor to its dependencies.
func NewService(db *gorm.DB, registries *registry.Store, toolReg *toolkit.Registry, runner Runner, projects *project.Service, notify *notify.Notifier) *Service {
	return &Service{
		db:         db,
		registries: registries,
		toolReg:    toolReg,
		runner:     runner,
		projects:   projects,
		notify:     notify,
		ctrl:       runctrl.New(),
		subs:       map[uint]map[chan ProgressEvent]struct{}{},
	}
}

// Start begins an asynchronous workflow run. It captures one immutable
// registry snapshot, validates the input against the workflow's schema, and
// returns the created pending run; execution continues in the background.
func (s *Service) Start(workflowName, projectName string, input map[string]any) (*store.WorkflowRun, error) {
	reg := s.registries.Current()
	wf, ok := reg.Workflows[workflowName]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrWorkflowNotFound, workflowName)
	}
	compiledInput, err := registry.CompileSchema(wf.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("workflow: %q input schema: %w", workflowName, err)
	}
	if err := compiledInput.Validate(input); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if _, err := s.projects.GetByName(projectName); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w %q", ErrProjectNotFound, projectName)
		}
		return nil, err
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("workflow: marshal input: %w", err)
	}

	run := store.NewWorkflowRun(wf, projectName, inputJSON, 1)
	if err := s.db.Create(run).Error; err != nil {
		return nil, fmt.Errorf("workflow: create run: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.ctrl.Register(run.ID, cancel)
	go func() {
		defer s.ctrl.Release(run.ID)
		_, _ = s.execute(runCtx, run.ID, reg)
	}()
	return run, nil
}

// ExecuteRun runs an existing pending run synchronously to completion using
// the given immutable registry snapshot and returns its final status. It is
// used by the job orchestrator for scheduled runs; the run row must already
// exist. The run is registered with the cancel controller so it can be
// cancelled through Cancel like any manually started run.
func (s *Service) ExecuteRun(ctx context.Context, runID uint, reg *registry.Registry) (store.WorkflowRunStatus, error) {
	runCtx, cancel := context.WithCancel(ctx)
	s.ctrl.Register(runID, cancel)
	defer s.ctrl.Release(runID)
	return s.execute(runCtx, runID, reg)
}

func (s *Service) execute(ctx context.Context, runID uint, reg *registry.Registry) (store.WorkflowRunStatus, error) {
	var run store.WorkflowRun
	if err := s.db.First(&run, runID).Error; err != nil {
		s.notify.Error("workflow: load run %d: %v", runID, err)
		// Best-effort: never leave a pending row without an executor.
		now := time.Now()
		s.db.Model(&store.WorkflowRun{}).Where("id = ?", runID).
			Updates(map[string]any{"status": store.WorkflowRunInterrupted, "finished_at": &now, "error": "executor failed to load run"})
		return store.WorkflowRunInterrupted, err
	}
	defer s.closeRun(runID)

	now := time.Now()
	run.Status = store.WorkflowRunRunning
	run.StartedAt = &now
	if err := s.db.Model(&run).Updates(map[string]any{"status": store.WorkflowRunRunning, "started_at": &now}).Error; err != nil {
		s.notify.Error("workflow: mark run %d running: %v", runID, err)
		return store.WorkflowRunInterrupted, err
	}
	runSnapshot := run
	s.publish(runID, ProgressEvent{Type: ProgressRun, Run: &runSnapshot})

	status, outputJSON, stepErr := s.runSteps(ctx, &run, reg)
	finished := time.Now()
	update := map[string]any{"status": status, "finished_at": &finished}
	run.Status = status
	run.FinishedAt = &finished
	if stepErr != nil {
		update["error"] = stepErr.Error()
		run.Error = stepErr.Error()
	}
	if outputJSON != nil {
		update["output"] = outputJSON
		run.Output = outputJSON
	}
	if err := s.db.Model(&run).Updates(update).Error; err != nil {
		s.notify.Error("workflow: persist run %d final status: %v", runID, err)
		return store.WorkflowRunInterrupted, fmt.Errorf("workflow: persist run %d final status: %w", runID, err)
	}
	finalSnapshot := run
	s.publish(runID, ProgressEvent{Type: ProgressRun, Run: &finalSnapshot})
	return status, stepErr
}

// Subscribe registers a live progress subscription for a run. If the run is
// already finished, the returned subscription's channel is closed so the
// consumer immediately sees the terminal state.
func (s *Service) Subscribe(runID uint) (*Subscription, error) {
	var run store.WorkflowRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return nil, ErrRunNotFound
	}
	ch := make(chan ProgressEvent, 64)
	s.mu.Lock()
	if s.subs[runID] == nil {
		s.subs[runID] = map[chan ProgressEvent]struct{}{}
	}
	s.subs[runID][ch] = struct{}{}
	s.mu.Unlock()

	sub := &Subscription{Events: ch, Close: func() { s.removeSub(runID, ch) }}
	// Re-check terminal state after registration: a run finishing between the
	// initial load and registration has already had closeRun close every
	// registered channel, so ours must be closed here or it never will be.
	// removeSub arbitrates so exactly one side closes the channel.
	if err := s.db.First(&run, runID).Error; err != nil || run.Status.IsTerminal() {
		s.mu.Lock()
		if s.dropSubLocked(runID, ch) {
			close(ch)
		}
		s.mu.Unlock()
	}
	return sub, nil
}

func (s *Service) removeSub(runID uint, ch chan ProgressEvent) {
	s.mu.Lock()
	s.dropSubLocked(runID, ch)
	s.mu.Unlock()
}

// dropSubLocked deregisters ch and reports whether it was still registered
// (and therefore not yet closed by closeRun). Callers hold s.mu.
func (s *Service) dropSubLocked(runID uint, ch chan ProgressEvent) bool {
	channels, ok := s.subs[runID]
	if !ok {
		return false
	}
	if _, ok := channels[ch]; !ok {
		return false
	}
	delete(channels, ch)
	if len(channels) == 0 {
		delete(s.subs, runID)
	}
	return true
}

// publish delivers a progress event to every subscriber of the run without
// blocking the executor; a slow subscriber has events dropped rather than
// stalling execution, since each event is an idempotent state update. Sends
// are non-blocking, so holding the lock here (and in closeRun) makes
// send-on-closed-channel impossible.
func (s *Service) publish(runID uint, ev ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs[runID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// closeRun closes every subscriber channel for a finished run, signalling
// consumers to fetch the final state.
func (s *Service) closeRun(runID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs[runID] {
		close(ch)
	}
	delete(s.subs, runID)
}

// runSteps executes every step of the workflow in order, persisting each
// step's transcript and output before the next step starts. A step failure
// stops the run. The run's output is finalized against the output schema (or
// as a JSON string) after the final step.
func (s *Service) runSteps(ctx context.Context, run *store.WorkflowRun, reg *registry.Registry) (store.WorkflowRunStatus, []byte, error) {
	wf := run.Workflow.Data()
	cfg, err := agent.ResolveConcierge(reg, run.Concierge, s.toolReg)
	if err != nil {
		return store.WorkflowRunFatal, nil, err
	}
	proj, err := s.projects.GetByName(run.ProjectName)
	if err != nil {
		return store.WorkflowRunFatal, nil, fmt.Errorf("workflow: project %q not found", run.ProjectName)
	}
	ctx = toolkit.WithWorkspace(ctx, s.projects.Path(*proj))

	var input map[string]any
	if len(run.Input) > 0 {
		if err := json.Unmarshal(run.Input, &input); err != nil {
			return store.WorkflowRunFatal, nil, fmt.Errorf("workflow: decode run input: %w", err)
		}
	}

	priorOutputs := make([]string, 0, len(wf.Steps))
	for index, stepText := range wf.Steps {
		step := &store.WorkflowStepRun{
			WorkflowRunID: run.ID,
			Index:         index,
			Text:          stepText,
			Status:        store.WorkflowRunPending,
		}
		if err := s.db.Create(step).Error; err != nil {
			return store.WorkflowRunFatal, nil, fmt.Errorf("workflow: create step run: %w", err)
		}

		prompt := buildStepPrompt(wf, input, priorOutputs, index)
		messages := append(append([]store.ChatMessage(nil), cfg.Static...),
			store.ChatMessage{Role: "user", Content: prompt, Timestamp: time.Now()})

		now := time.Now()
		step.Status = store.WorkflowRunRunning
		step.StartedAt = &now
		if err := s.db.Model(step).Updates(map[string]any{"status": store.WorkflowRunRunning, "started_at": &now}).Error; err != nil {
			return store.WorkflowRunFatal, nil, err
		}
		stepSnapshot := *step
		s.publish(run.ID, ProgressEvent{Type: ProgressStep, Step: &stepSnapshot})

		result, runErr := s.runner.Run(ctx, agent.Request{
			Identity: cfg.Identity,
			Toolset:  cfg.Toolset,
			Plugins:  cfg.Plugins,
			Turn:     plugin.TurnContext{Scope: toolkit.ScopeWorkflow, Messages: messages, Metadata: map[string]any{}},
			Scope:    toolkit.ScopeWorkflow,
			Audit:    agent.AuditOwner{WorkflowRunID: &run.ID, WorkflowStepRunID: &step.ID},
			OnDelta: func(ev agent.StreamEvent) {
				// The step row does not change while the agent runs; reuse the
				// running snapshot instead of copying it per delta.
				s.publish(run.ID, ProgressEvent{Type: ProgressDelta, Step: &stepSnapshot, Delta: &ev})
			},
		})
		finished := time.Now()
		output := stepOutput(result)
		transcript, _ := json.Marshal(result.Messages)
		stepUpdates := map[string]any{
			"transcript":  transcript,
			"output":      output,
			"finished_at": &finished,
		}
		if runErr != nil {
			status, message := classifyStatus(ctx, runErr)
			stepUpdates["status"] = status
			stepUpdates["error"] = message
			step.Status = status
			step.Error = message
			if err := s.db.Model(step).Updates(stepUpdates).Error; err != nil {
				s.notify.Error("workflow: persist step %d failure: %v", step.ID, err)
			}
			failedStep := *step
			s.publish(run.ID, ProgressEvent{Type: ProgressStep, Step: &failedStep})
			return status, nil, fmt.Errorf("workflow: step %d failed: %w", index+1, runErr)
		}
		stepUpdates["status"] = store.WorkflowRunSucceeded
		step.Status = store.WorkflowRunSucceeded
		step.Output = output
		if err := s.db.Model(step).Updates(stepUpdates).Error; err != nil {
			s.notify.Error("workflow: persist step %d success: %v", step.ID, err)
		}
		doneStep := *step
		s.publish(run.ID, ProgressEvent{Type: ProgressStep, Step: &doneStep})
		priorOutputs = append(priorOutputs, output)
	}

	finalOutput := ""
	if n := len(priorOutputs); n > 0 {
		finalOutput = priorOutputs[n-1]
	}
	outputJSON, err := normalizeFinalOutput(finalOutput, wf)
	if err != nil {
		return store.WorkflowRunFailed, nil, err
	}
	return store.WorkflowRunSucceeded, outputJSON, nil
}

// Get loads a run with its ordered steps.
func (s *Service) Get(runID uint) (*store.WorkflowRun, []store.WorkflowStepRun, error) {
	var run store.WorkflowRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return nil, nil, ErrRunNotFound
	}
	var steps []store.WorkflowStepRun
	if err := s.db.Where("workflow_run_id = ?", runID).Order("index").Find(&steps).Error; err != nil {
		return nil, nil, err
	}
	return &run, steps, nil
}

// List returns workflow runs, newest first, optionally filtered by workflow
// name with bounded pagination.
func (s *Service) List(workflowName string, limit, offset int) ([]store.WorkflowRun, error) {
	return store.ListRuns[store.WorkflowRun](s.db, "workflow_name", workflowName, limit, offset)
}

// ListByJobRun returns the workflow runs created for one Job run, in attempt
// order per binding index.
func (s *Service) ListByJobRun(jobRunID uint) ([]store.WorkflowRun, error) {
	var runs []store.WorkflowRun
	if err := s.db.Where("job_run_id = ?", jobRunID).Order("binding_index, attempt").Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// Cancel requests cancellation of an active run. Finished runs cannot be
// cancelled.
func (s *Service) Cancel(runID uint) error {
	return s.ctrl.CancelRun(s.db, &store.WorkflowRun{}, runID, ErrRunNotFound, ErrRunFinished)
}

// Reconcile marks stale pending/running runs and their steps as interrupted.
// It is called once at process startup; interrupted runs are never resumed
// in place, and future attempts follow normal trigger/retry policy.
func (s *Service) Reconcile() error {
	pending := []string{string(store.WorkflowRunPending), string(store.WorkflowRunRunning)}
	if err := store.MarkInterrupted(s.db, &store.WorkflowRun{}, pending); err != nil {
		return err
	}
	return store.MarkInterrupted(s.db, &store.WorkflowStepRun{}, pending)
}

// Shutdown cancels every active run. It is called during process shutdown;
// workers observe cancellation and finalize their run statuses.
func (s *Service) Shutdown() {
	s.ctrl.Shutdown()
}
