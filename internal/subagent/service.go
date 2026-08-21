// Package subagent owns delegated agent execution and its durable lifecycle.
package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/runctrl"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRunNotFound     = errors.New("subagent: run not found")
	ErrRunFinished     = errors.New("subagent: run already finished")
	ErrMaxDepth        = errors.New("subagent: maximum delegation depth reached")
	ErrInvalidCategory = errors.New("subagent: invalid category")
)

const (
	maxSeedBytes         = 256 * 1024
	maxSubagentResult    = 192 * 1024
	maxNotificationBytes = 2 * 1024
	notificationLease    = 5 * time.Minute
)

// Request is the normalized input shared by spawn and fork tools.
type Request struct {
	ParentSessionID uint
	ParentRunID     *uint
	ProjectID       uint
	Depth           int
	Category        store.SubagentCategory
	Label           string
	Prompt          string
	Seed            []store.ChatMessage
}

// Executor creates the child session, runs one child turn, and returns its
// final text. Mode controls whether parent history is seeded into the child.
type Executor interface {
	ExecuteSubagent(context.Context, *store.SubagentRun) (childSessionID uint, result string, err error)
}

// InterruptedResultSource exposes recovered output from child turns that were
// interrupted by a previous process.
type InterruptedResultSource interface {
	InterruptedSubagentResult(subagentRunID uint) (childSessionID uint, result string, recovered bool, err error)
}

type completionPayload struct {
	RunID   uint                    `json:"run_id"`
	Label   string                  `json:"label"`
	Status  store.SubagentRunStatus `json:"status"`
	Summary string                  `json:"summary,omitempty"`
	Error   string                  `json:"error,omitempty"`
}

// Service permits multiple concurrent background children per parent session.
type Service struct {
	db       *gorm.DB
	maxDepth int
	executor Executor
	ctrl     *runctrl.Controller

	mu      sync.Mutex
	done    map[uint]chan struct{}
	wg      sync.WaitGroup
	closing bool

	// onCompletion notifies the delivery layer that a background run finished
	// and a completion event is now durable for the given root parent session.
	onCompletion func(sessionID uint)
}

// SetOnCompletion registers a callback invoked after a background run's
// completion event becomes durable, so an idle-session delivery layer can
// pick it up. It is nil during Reconcile (dependencies not yet wired).
func (s *Service) SetOnCompletion(fn func(sessionID uint)) { s.onCompletion = fn }

func New(db *gorm.DB, maxDepth int) *Service {
	return &Service{db: db, maxDepth: maxDepth, ctrl: runctrl.New(), done: map[uint]chan struct{}{}}
}

func (s *Service) SetExecutor(executor Executor) { s.executor = executor }

func (s *Service) StartSpawn(ctx context.Context, req Request) (*store.SubagentRun, error) {
	run, err := s.create(req, store.SubagentModeSpawn, store.SubagentScheduleBackground)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.ctrl.Register(run.ID, cancel)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.execute(runCtx, run.ID)
	}()
	return run, nil
}

func (s *Service) RunFork(ctx context.Context, req Request) (*store.SubagentRun, error) {
	run, err := s.create(req, store.SubagentModeFork, store.SubagentScheduleForeground)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.ctrl.Register(run.ID, cancel)
	s.wg.Add(1)
	defer s.wg.Done()
	s.execute(runCtx, run.ID)
	return s.Get(run.ID)
}

func (s *Service) create(req Request, mode store.SubagentMode, schedule store.SubagentSchedule) (*store.SubagentRun, error) {
	if req.Depth >= s.maxDepth {
		return nil, fmt.Errorf("%w: depth %d, limit %d", ErrMaxDepth, req.Depth, s.maxDepth)
	}
	if !req.Category.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCategory, req.Category)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, errors.New("subagent: service is shutting down")
	}
	run := &store.SubagentRun{
		ParentSessionID: req.ParentSessionID, ParentRunID: req.ParentRunID, ProjectID: req.ProjectID,
		Mode: mode, Schedule: schedule, Status: store.SubagentRunPending, Depth: req.Depth + 1,
		Category: req.Category, Label: req.Label, Prompt: req.Prompt,
	}
	if len(req.Seed) > 0 {
		seed, err := marshalSeed(req.Seed)
		if err != nil {
			return nil, fmt.Errorf("subagent: marshal seed: %w", err)
		}
		run.Seed = datatypes.JSON(seed)
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		parent, err := store.LockSession(tx, req.ParentSessionID)
		if err != nil {
			return err
		}
		if parent.ProjectID != req.ProjectID {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(run).Error
	}); err != nil {
		return nil, fmt.Errorf("subagent: create run: %w", err)
	}
	s.done[run.ID] = make(chan struct{})
	return run, nil
}

func (s *Service) execute(ctx context.Context, runID uint) {
	defer s.ctrl.Release(runID)
	defer s.signalDone(runID)
	run, err := s.Get(runID)
	if err != nil {
		log.Printf("subagents: load run %d for execution: %v", runID, err)
		s.failUnloadedRun(runID, err)
		return
	}
	now := time.Now()
	if err := s.db.Model(run).Updates(map[string]any{"status": store.SubagentRunRunning, "started_at": &now}).Error; err != nil {
		s.finish(run, store.SubagentRunFailed, 0, "", err)
		return
	}
	if s.executor == nil {
		s.finish(run, store.SubagentRunFailed, 0, "", errors.New("subagent executor is not configured"))
		return
	}
	execCtx := toolkit.WithSubagentContext(ctx, toolkit.SubagentContext{RunID: run.ID, ParentSessionID: run.ParentSessionID, Depth: run.Depth})
	childID, result, runErr := s.executor.ExecuteSubagent(execCtx, run)
	status := store.SubagentRunSucceeded
	if errors.Is(ctx.Err(), context.Canceled) {
		status = store.SubagentRunCancelled
		if runErr == nil {
			runErr = ctx.Err()
		}
	} else if runErr != nil {
		status = store.SubagentRunFailed
	}
	if err := s.finish(run, status, childID, result, runErr); err != nil {
		log.Printf("subagents: finalize run %d: %v", run.ID, err)
	}
}

func (s *Service) failUnloadedRun(runID uint, runErr error) {
	finished := time.Now()
	if err := s.db.Model(&store.SubagentRun{}).Where("id = ? AND status = ?", runID, store.SubagentRunPending).Updates(map[string]any{
		"status": store.SubagentRunFailed, "error": runErr.Error(), "finished_at": &finished,
	}).Error; err != nil {
		log.Printf("subagents: mark unloaded run %d failed: %v", runID, err)
	}
}

func (s *Service) finish(run *store.SubagentRun, status store.SubagentRunStatus, childID uint, result string, runErr error) error {
	finished := time.Now()
	result = transform.LimitTextBytes(result, maxSubagentResult)
	updates := map[string]any{"status": status, "result": result, "finished_at": &finished}
	runError := ""
	if childID != 0 {
		updates["child_session_id"] = childID
	}
	if runErr != nil {
		runError = runErr.Error()
		updates["error"] = runError
	}
	var deliverySessionID uint
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(run).Updates(updates).Error; err != nil {
			return fmt.Errorf("update terminal status: %w", err)
		}
		if run.Schedule != store.SubagentScheduleBackground {
			return nil
		}
		var err error
		deliverySessionID, err = createCompletionEvent(tx, run, status, result, runError)
		if err != nil {
			return fmt.Errorf("create completion event: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if run.Schedule == store.SubagentScheduleBackground && s.onCompletion != nil {
		s.onCompletion(deliverySessionID)
	}
	return nil
}

// FormatNotifications joins claimed completion notifications into a single
// user-facing message for durable delivery into the parent session transcript.
func FormatNotifications(notifications []agent.Notification) string {
	parts := make([]string, len(notifications))
	for i := range notifications {
		parts[i] = notifications[i].Text
	}
	return strings.Join(parts, "\n\n")
}

func createCompletionEvent(db *gorm.DB, run *store.SubagentRun, status store.SubagentRunStatus, result, runError string) (uint, error) {
	payload, err := json.Marshal(completionPayload{
		RunID: run.ID, Label: transform.LimitTextBytes(run.Label, 160), Status: status,
		Summary: transform.LimitTextBytes(result, maxNotificationBytes/2), Error: transform.LimitTextBytes(runError, maxNotificationBytes/4),
	})
	if err != nil {
		return 0, err
	}
	if len(payload) > maxNotificationBytes {
		payload, err = json.Marshal(completionPayload{RunID: run.ID, Label: transform.LimitTextBytes(run.Label, 80), Status: status})
		if err != nil {
			return 0, err
		}
	}
	deliverySessionID, err := rootParentSessionID(db, run)
	if err != nil {
		return 0, err
	}
	if err := db.Where(store.SubagentEvent{RunID: run.ID}).FirstOrCreate(&store.SubagentEvent{RunID: run.ID, ParentSessionID: deliverySessionID, Payload: datatypes.JSON(payload)}).Error; err != nil {
		return 0, err
	}
	return deliverySessionID, nil
}

func rootParentSessionID(db *gorm.DB, run *store.SubagentRun) (uint, error) {
	current := run
	for current.ParentRunID != nil {
		var parent store.SubagentRun
		if err := db.First(&parent, *current.ParentRunID).Error; err != nil {
			return 0, fmt.Errorf("load parent run %d: %w", *current.ParentRunID, err)
		}
		current = &parent
	}
	return current.ParentSessionID, nil
}

func (s *Service) signalDone(runID uint) {
	s.mu.Lock()
	if done := s.done[runID]; done != nil {
		close(done)
		delete(s.done, runID)
	}
	s.mu.Unlock()
}

func (s *Service) Get(runID uint) (*store.SubagentRun, error) {
	var run store.SubagentRun
	if err := s.db.First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("subagent: get run: %w", err)
	}
	return &run, nil
}

func (s *Service) ListByParentSession(sessionID uint) ([]store.SubagentRun, error) {
	var count int64
	if err := s.db.Model(&store.Session{}).Where("id = ?", sessionID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var runs []store.SubagentRun
	err := s.db.Where("parent_session_id = ?", sessionID).Order("id desc").Find(&runs).Error
	return runs, err
}

func (s *Service) ListBackgroundByParentSessions(sessionIDs []uint) ([]store.SubagentRun, error) {
	if len(sessionIDs) == 0 {
		return []store.SubagentRun{}, nil
	}
	var runs []store.SubagentRun
	err := s.db.Where("parent_session_id IN ? AND schedule = ?", sessionIDs, store.SubagentScheduleBackground).
		Order("parent_session_id, id desc").Find(&runs).Error
	return runs, err
}

func (s *Service) Cancel(runID uint) error {
	return s.ctrl.CancelRun(s.db, &store.SubagentRun{}, runID, ErrRunNotFound, ErrRunFinished)
}

// AwaitActiveDirect freezes and waits for the currently active direct spawn set.
func (s *Service) AwaitActiveDirect(ctx context.Context, parentSessionID uint, parentRunID *uint) ([]store.SubagentRun, error) {
	query := s.db.Where("parent_session_id = ? AND schedule = ? AND status IN ?", parentSessionID, store.SubagentScheduleBackground, []store.SubagentRunStatus{store.SubagentRunPending, store.SubagentRunRunning})
	if parentRunID == nil {
		query = query.Where("parent_run_id IS NULL")
	} else {
		query = query.Where("parent_run_id = ?", *parentRunID)
	}
	var targets []store.SubagentRun
	if err := query.Order("id").Find(&targets).Error; err != nil {
		return nil, err
	}
	for _, target := range targets {
		s.mu.Lock()
		done := s.done[target.ID]
		s.mu.Unlock()
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ids := make([]uint, len(targets))
	for i := range targets {
		ids[i] = targets[i].ID
	}
	if len(ids) == 0 {
		return []store.SubagentRun{}, nil
	}
	var runs []store.SubagentRun
	if err := s.db.Where("id IN ?", ids).Order("id").Find(&runs).Error; err != nil {
		return nil, err
	}
	if err := s.consumeRunEvents(ids); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Service) Reconcile(recovery ...InterruptedResultSource) error {
	if err := s.backfillChildSessionIDs(); err != nil {
		return err
	}
	var runs []store.SubagentRun
	if err := s.db.Where("status IN ?", []store.SubagentRunStatus{store.SubagentRunPending, store.SubagentRunRunning}).Find(&runs).Error; err != nil {
		return err
	}
	var source InterruptedResultSource
	if len(recovery) > 0 {
		source = recovery[0]
	}
	for index := range runs {
		run := &runs[index]
		childID, result := uint(0), ""
		if source != nil {
			var recovered bool
			var err error
			childID, result, recovered, err = source.InterruptedSubagentResult(run.ID)
			if err != nil {
				return fmt.Errorf("reconcile child run %d: %w", run.ID, err)
			}
			if !recovered {
				childID, result = 0, ""
			}
		}
		if err := s.finish(run, store.SubagentRunInterrupted, childID, result, errors.New("server restarted before subagent finished")); err != nil {
			return fmt.Errorf("reconcile run %d: %w", run.ID, err)
		}
	}
	var missingEvents []store.SubagentRun
	if err := s.db.Where("schedule = ? AND status IN ? AND NOT EXISTS (?)",
		store.SubagentScheduleBackground,
		[]store.SubagentRunStatus{store.SubagentRunSucceeded, store.SubagentRunFailed, store.SubagentRunCancelled, store.SubagentRunInterrupted},
		s.db.Model(&store.SubagentEvent{}).Select("1").Where("subagent_events.run_id = subagent_runs.id"),
	).Find(&missingEvents).Error; err != nil {
		return err
	}
	for index := range missingEvents {
		run := &missingEvents[index]
		if _, err := createCompletionEvent(s.db, run, run.Status, run.Result, run.Error); err != nil {
			return fmt.Errorf("reconcile completion event for run %d: %w", run.ID, err)
		}
	}
	return nil
}

func (s *Service) backfillChildSessionIDs() error {
	var children []store.Session
	if err := s.db.Where("parent_subagent_run_id IS NOT NULL").Find(&children).Error; err != nil {
		return fmt.Errorf("subagent: load child sessions: %w", err)
	}
	for _, child := range children {
		if err := s.db.Model(&store.SubagentRun{}).
			Where("id = ? AND child_session_id IS NULL", *child.ParentSubagentRunID).
			Update("child_session_id", child.ID).Error; err != nil {
			return fmt.Errorf("subagent: link run %d to child session %d: %w", *child.ParentSubagentRunID, child.ID, err)
		}
	}
	return nil
}

func (s *Service) Shutdown() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.ctrl.Shutdown()
	s.wg.Wait()
}

// ClaimNotifications leases unconsumed completion events in durable order.
// Failed turns release their leases; abandoned leases expire automatically.
func (s *Service) ClaimNotifications(sessionID uint) ([]agent.Notification, error) {
	var events []store.SubagentEvent
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		expired := time.Now().Add(-notificationLease)
		query := tx.Where("parent_session_id = ? AND consumed_at IS NULL AND (claimed_at IS NULL OR claimed_at < ?)", sessionID, expired).Order("id")
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := query.Find(&events).Error; err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		ids := make([]uint, len(events))
		for index := range events {
			ids[index] = events[index].ID
		}
		now := time.Now()
		return tx.Model(&store.SubagentEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).Update("claimed_at", &now).Error
	}); err != nil {
		return nil, err
	}
	out := make([]agent.Notification, len(events))
	for i := range events {
		out[i] = agent.Notification{ID: events[i].ID, Text: "Subagent completion (details available from subagent-runs API): " + string(events[i].Payload)}
	}
	return out, nil
}

func (s *Service) AcknowledgeNotifications(ids []uint) error {
	return s.AcknowledgeNotificationsTx(s.db, ids)
}

func (s *Service) AcknowledgeNotificationsTx(tx *gorm.DB, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return tx.Model(&store.SubagentEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).Updates(map[string]any{"consumed_at": &now, "claimed_at": nil}).Error
}

func (s *Service) ReleaseNotifications(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Model(&store.SubagentEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).Update("claimed_at", nil).Error
}

func (s *Service) consumeRunEvents(runIDs []uint) error {
	if len(runIDs) == 0 {
		return nil
	}
	now := time.Now()
	return s.db.Model(&store.SubagentEvent{}).Where("run_id IN ? AND consumed_at IS NULL", runIDs).Updates(map[string]any{"consumed_at": &now, "claimed_at": nil}).Error
}

func marshalSeed(seed []store.ChatMessage) ([]byte, error) {
	trimmed := append([]store.ChatMessage(nil), seed...)
	for index := range trimmed {
		trimmed[index].Attachments = nil
		trimmed[index].Content = transform.LimitTextBytes(trimmed[index].Content, maxSeedBytes/8)
		trimmed[index].ReasoningContent = transform.LimitTextBytes(trimmed[index].ReasoningContent, maxSeedBytes/8)
		if len(trimmed[index].ToolCalls) > maxSeedBytes/8 {
			trimmed[index].ToolCalls = nil
		}
	}
	for len(trimmed) > 0 {
		encoded, err := json.Marshal(trimmed)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= maxSeedBytes {
			return encoded, nil
		}
		trimmed = append(trimmed[:1], trimmed[2:]...)
	}
	return nil, errors.New("subagent: seed cannot fit size limit")
}
