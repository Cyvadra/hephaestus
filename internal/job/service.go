// Package job orchestrates Job scheduling definitions: it claims eligible
// runs transactionally, executes their bound Workflows sequentially with
// per-binding retries, and persists durable run/state records.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/runctrl"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/workflow"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrJobNotFound = errors.New("job: run not found")
	ErrRunFinished = errors.New("job: run already finished")
)

// Service orchestrates Job runs. Different Jobs may run concurrently, but a
// single Job never overlaps itself thanks to the transactional claim.
type Service struct {
	db         *gorm.DB
	registries *registry.Store
	workflows  *workflow.Service
	notify     *notify.Notifier

	// ctrl owns run cancellation; see runctrl.Controller.
	ctrl *runctrl.Controller
}

// NewService wires the job orchestrator to its dependencies.
func NewService(db *gorm.DB, registries *registry.Store, workflows *workflow.Service, notify *notify.Notifier) *Service {
	return &Service{
		db:         db,
		registries: registries,
		workflows:  workflows,
		notify:     notify,
		ctrl:       runctrl.New(),
	}
}

// claim attempts to start a scheduled run for jobName. It returns the
// created JobRun, or (nil, false) when the job is not eligible (already
// running or at its per-day cap). The eligibility check, daily-limit
// enforcement, and run creation happen in one transaction with row locking
// so overlapping ticks cannot double-run a job.
func (s *Service) claim(ctx context.Context, reg *registry.Registry, jobName string, now time.Time) (*store.JobRun, bool, error) {
	job, ok := reg.Jobs[jobName]
	if !ok {
		return nil, false, fmt.Errorf("job: %q not found", jobName)
	}
	localDate := now.Format("2006-01-02")
	for attempt := 0; attempt < 3; attempt++ {
		var run *store.JobRun
		err := s.db.Transaction(func(tx *gorm.DB) error {
			state, err := s.lockState(tx, jobName)
			if err != nil {
				return err
			}
			if state.LocalDate != localDate {
				state.LocalDate = localDate
				state.ExecutionsToday = 0
			}
			if state.ActiveRunID != nil {
				return nil // already running
			}
			if state.ExecutionsToday >= job.MaxExecutionsPerDay {
				return nil
			}
			startedAt := time.Now()
			created := &store.JobRun{
				JobName:   jobName,
				LocalDate: localDate,
				Job:       datatypes.NewJSONType(job),
				Status:    store.JobRunPending,
			}
			if err := tx.Create(created).Error; err != nil {
				return err
			}
			state.ActiveRunID = &created.ID
			state.ExecutionsToday++
			state.LastStartedAt = &startedAt
			if err := tx.Save(&state).Error; err != nil {
				return err
			}
			run = created
			return nil
		})
		if err == nil {
			if run == nil {
				return nil, false, nil
			}
			return run, true, nil
		}
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			continue // a concurrent tick created the state row; retry
		}
		return nil, false, err
	}
	return nil, false, fmt.Errorf("job: claim %q: too many concurrent attempts", jobName)
}

func (s *Service) lockState(tx *gorm.DB, jobName string) (*store.JobState, error) {
	var state store.JobState
	query := tx.Where("job_name = ?", jobName)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&state).Error
	if err == nil {
		return &state, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	state = store.JobState{JobName: jobName}
	if err := tx.Create(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

// executeJob runs the claimed Job's bindings sequentially and finalizes the
// run and its JobState. A failed binding does not block later bindings. The
// run context derives from ctx (the scheduler's lifetime) and from the
// cancel controller, so both process shutdown and Cancel stop it.
func (s *Service) executeJob(ctx context.Context, reg *registry.Registry, run *store.JobRun) {
	runID := run.ID
	runCtx, cancel := context.WithCancel(ctx)
	s.ctrl.Register(runID, cancel)
	defer s.ctrl.Release(runID)

	now := time.Now()
	run.Status = store.JobRunRunning
	run.StartedAt = &now
	if err := s.db.Model(run).Updates(map[string]any{"status": store.JobRunRunning, "started_at": &now}).Error; err != nil {
		s.notify.Error("job: mark run %d running: %v", runID, err)
		s.releaseClaim(run)
		return
	}

	job := run.Job.Data()
	lastSucceededAt := s.lastSucceededAt(run.JobName)
	completed := 0
	var firstErr error
	for bindingIndex, binding := range job.Workflows {
		if runCtx.Err() != nil {
			break
		}
		status := s.runBinding(runCtx, reg, run, job, binding, bindingIndex, lastSucceededAt)
		if status == store.WorkflowRunSucceeded {
			completed++
		} else if firstErr == nil {
			firstErr = fmt.Errorf("binding %d (workflow %q): %s", bindingIndex, binding.Workflow, status)
		}
	}

	finalStatus := s.finalizeStatus(runCtx, firstErr, completed, len(job.Workflows))
	finished := time.Now()
	updateState := map[string]any{"active_run_id": nil}
	if finalStatus == store.JobRunSucceeded {
		updateState["last_succeeded_at"] = &finished
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(run).Updates(map[string]any{
			"status": finalStatus, "finished_at": &finished,
			"error": errorString(firstErr),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&store.JobState{}).Where("job_name = ? AND active_run_id = ?", run.JobName, runID).Updates(updateState).Error
	}); err != nil {
		s.notify.Error("job: finalize run %d: %v", runID, err)
	}
}

// releaseClaim clears a claim when no executor was successfully started. The
// run remains pending for startup reconciliation if the database is still
// unavailable; the conditional update never clears a newer claim.
func (s *Service) releaseClaim(run *store.JobRun) {
	if err := s.db.Model(&store.JobState{}).
		Where("job_name = ? AND active_run_id = ?", run.JobName, run.ID).
		Update("active_run_id", nil).Error; err != nil {
		s.notify.Error("job: release failed start claim for run %d: %v", run.ID, err)
	}
}

func (s *Service) lastSucceededAt(jobName string) *time.Time {
	var state store.JobState
	if err := s.db.Where("job_name = ?", jobName).First(&state).Error; err != nil {
		return nil
	}
	return state.LastSucceededAt
}

// runBinding executes one binding's Workflow up to MaxAttempts times.
// Retries happen only for retryable (failed) runs; fatal and cancelled runs
// stop immediately.
func (s *Service) runBinding(ctx context.Context, reg *registry.Registry, run *store.JobRun, job registry.Job, binding registry.JobWorkflowBinding, bindingIndex int, lastSucceededAt *time.Time) store.WorkflowRunStatus {
	wf, ok := reg.Workflows[binding.Workflow]
	if !ok {
		s.notify.Error("job: run %d binding %d references missing workflow %q", run.ID, bindingIndex, binding.Workflow)
		return store.WorkflowRunFatal
	}
	compiledInput, err := registry.CompileSchema(wf.InputSchema)
	if err != nil {
		s.notify.Error("job: run %d binding %d workflow %q input schema: %v", run.ID, bindingIndex, binding.Workflow, err)
		return store.WorkflowRunFatal
	}

	var lastStatus store.WorkflowRunStatus
	for attempt := 1; attempt <= binding.MaxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return store.WorkflowRunCancelled
			case <-time.After(time.Duration(binding.RetryDelaySeconds) * time.Second):
			}
		}
		if ctx.Err() != nil {
			return store.WorkflowRunCancelled
		}

		input, err := s.resolveInput(run, job, binding, lastSucceededAt)
		if err != nil {
			s.notify.Error("job: run %d binding %d input: %v", run.ID, bindingIndex, err)
			return store.WorkflowRunFatal
		}
		// A resolved input that fails the workflow's schema is fatal and
		// non-retryable.
		if err := compiledInput.Validate(input); err != nil {
			s.notify.Error("job: run %d binding %d input does not satisfy workflow %q schema: %v", run.ID, bindingIndex, binding.Workflow, err)
			return store.WorkflowRunFatal
		}

		lastStatus = s.startScheduledWorkflow(ctx, reg, run, binding, bindingIndex, attempt, input, wf)
		switch lastStatus {
		case store.WorkflowRunSucceeded, store.WorkflowRunFatal, store.WorkflowRunCancelled:
			return lastStatus
		}
	}
	return lastStatus
}

func (s *Service) startScheduledWorkflow(ctx context.Context, reg *registry.Registry, run *store.JobRun, binding registry.JobWorkflowBinding, bindingIndex, attempt int, input map[string]any, wf registry.Workflow) store.WorkflowRunStatus {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		s.notify.Error("job: marshal binding %d input: %v", bindingIndex, err)
		return store.WorkflowRunFatal
	}
	workflowRun := store.NewWorkflowRun(wf, binding.Project, inputJSON, attempt)
	workflowRun.JobRunID = &run.ID
	workflowRun.JobName = run.JobName
	workflowRun.BindingIndex = bindingIndex
	if err := s.db.Create(workflowRun).Error; err != nil {
		s.notify.Error("job: create workflow run for binding %d: %v", bindingIndex, err)
		return store.WorkflowRunFatal
	}
	status, _ := s.workflows.ExecuteRun(ctx, workflowRun.ID, reg)
	return status
}

// resolveInput substitutes the binding's ${...} placeholders. A resolved
// input that fails the workflow schema is reported as an error here.
func (s *Service) resolveInput(run *store.JobRun, job registry.Job, binding registry.JobWorkflowBinding, lastSucceededAt *time.Time) (map[string]any, error) {
	var startedAt, lastSucceeded time.Time
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	if lastSucceededAt != nil {
		lastSucceeded = *lastSucceededAt
	}
	vars := map[string]any{
		"job.name":                  job.Name,
		"job.title":                 job.Title,
		"job.goal":                  job.Goal,
		"run.local_date":            run.LocalDate,
		"run.started_at":            startedAt,
		"trigger.last_succeeded_at": lastSucceeded,
		"now":                       time.Now(),
	}
	input, err := registry.ResolvePlaceholders(binding.Input, vars)
	if err != nil {
		return nil, fmt.Errorf("job: binding %q input resolution: %w", binding.Workflow, err)
	}
	return input, nil
}

func (s *Service) finalizeStatus(ctx context.Context, firstErr error, completed, total int) store.JobRunStatus {
	if ctx.Err() != nil {
		return store.JobRunCancelled
	}
	if firstErr == nil {
		return store.JobRunSucceeded
	}
	if completed > 0 {
		return store.JobRunCompletedWithErrors
	}
	return store.JobRunFailed
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Get loads a Job run.
func (s *Service) Get(runID uint) (*store.JobRun, error) {
	var run store.JobRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return nil, ErrJobNotFound
	}
	return &run, nil
}

// List returns Job runs, newest first, optionally filtered by job name with
// bounded pagination.
func (s *Service) List(jobName string, limit, offset int) ([]store.JobRun, error) {
	return store.ListRuns[store.JobRun](s.db, "job_name", jobName, limit, offset)
}

// Cancel requests cancellation of an active Job run. Finished runs cannot be
// cancelled.
func (s *Service) Cancel(runID uint) error {
	return s.ctrl.CancelRun(s.db, &store.JobRun{}, runID, ErrJobNotFound, ErrRunFinished)
}

// Reconcile marks stale pending/running Job runs as interrupted and clears
// their active claims. It is called once at process startup; interrupted
// runs are never resumed in place, and future attempts follow normal
// trigger/retry policy.
func (s *Service) Reconcile() error {
	if err := store.MarkInterrupted(s.db, &store.JobRun{},
		[]string{string(store.JobRunPending), string(store.JobRunRunning)}); err != nil {
		return err
	}
	return s.db.Model(&store.JobState{}).
		Where("active_run_id IS NOT NULL").
		Updates(map[string]any{"active_run_id": nil}).Error
}

// Shutdown cancels every active Job run. Workers observe cancellation and
// finalize their run statuses.
func (s *Service) Shutdown() {
	s.ctrl.Shutdown()
}
