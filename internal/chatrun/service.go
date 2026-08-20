// Package chatrun owns durable, reconnectable chat turn execution.
package chatrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/runctrl"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	ErrRunNotFound = errors.New("chatrun: run not found")
	ErrRunActive   = errors.New("chatrun: session already has an active run")
	ErrRunFinished = errors.New("chatrun: run already finished")
)

// Result is the final client-visible payload and optional persisted message id
// produced by one durable chat turn.
type Result struct {
	FinalMessageID *uint
	Response       any
}

// Execute performs a chat pipeline entry point and reports its typed progress.
type Execute func(context.Context, func(chat.StreamEvent)) (*Result, error)

// ProgressEvent is sent to live subscribers after each durable snapshot update.
type ProgressEvent struct {
	Sequence uint64         `json:"sequence"`
	Type     string         `json:"type"`
	Payload  datatypes.JSON `json:"payload"`
}

// Subscription is a live progress subscription. Close releases the listener.
type Subscription struct {
	Events <-chan ProgressEvent
	Close  func()
}

// Service runs chat turns outside individual HTTP request lifetimes.
type Service struct {
	db   *gorm.DB
	ctrl *runctrl.Controller

	mu           sync.Mutex
	subs         map[uint]map[chan ProgressEvent]struct{}
	progressMu   sync.Mutex
	sequences    map[uint]uint64
	wg           sync.WaitGroup
	shuttingDown bool

	// onRunEnded notifies the delivery layer that a session's run finished,
	// so completions that arrived too late to be steered can be delivered.
	onRunEnded func(sessionID uint, status store.ChatRunStatus)
}

// SetOnRunEnded registers a callback invoked after a run reaches a terminal
// state for the given session.
func (s *Service) SetOnRunEnded(fn func(sessionID uint, status store.ChatRunStatus)) {
	s.onRunEnded = fn
}

func New(db *gorm.DB) *Service {
	return &Service{db: db, ctrl: runctrl.New(), subs: map[uint]map[chan ProgressEvent]struct{}{}, sequences: map[uint]uint64{}}
}

// Start creates one pending run and executes it in a background goroutine.
// PostgreSQL's partial unique index enforces one active run per session.
func (s *Service) Start(sessionID, projectID uint, kind store.ChatRunKind, request map[string]any, execute Execute) (*store.ChatRun, error) {
	run := &store.ChatRun{
		SessionID: sessionID,
		ProjectID: projectID,
		Kind:      kind,
		Status:    store.ChatRunPending,
		Request:   datatypes.NewJSONType(request),
		Snapshot:  datatypes.NewJSONType(store.ChatRunSnapshot{}),
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := store.LockSession(tx, sessionID)
		if err != nil {
			return err
		}
		if session.ProjectID != projectID {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(run).Error
	}); err != nil {
		if isActiveRunConflict(err) {
			return nil, ErrRunActive
		}
		return nil, fmt.Errorf("chatrun: create: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ctrl.Register(run.ID, cancel)
	s.progressMu.Lock()
	s.sequences[run.ID] = 0
	s.progressMu.Unlock()
	s.wg.Add(1)
	go s.execute(ctx, run.ID, sessionID, execute)
	return run, nil
}

func isActiveRunConflict(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_chat_runs_active_session"
}

func (s *Service) execute(ctx context.Context, runID, sessionID uint, execute Execute) {
	defer s.wg.Done()
	defer s.ctrl.Release(runID)
	defer s.closeRun(runID)

	now := time.Now()
	if err := s.db.Model(&store.ChatRun{}).Where("id = ?", runID).Updates(map[string]any{
		"status": store.ChatRunRunning, "started_at": &now,
	}).Error; err != nil {
		if finishErr := s.finish(runID, store.ChatRunInterrupted, nil, err); finishErr != nil {
			log.Printf("chatrun: finalize run %d after start failure: %v", runID, finishErr)
		}
		return
	}
	var progressErr error
	result, runErr := execute(ctx, func(delta chat.StreamEvent) {
		if progressErr == nil {
			progressErr = s.recordDelta(runID, delta)
		}
	})
	if runErr == nil {
		runErr = progressErr
	}
	status := store.ChatRunSucceeded
	if runErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			s.progressMu.Lock()
			interrupted := s.shuttingDown
			s.progressMu.Unlock()
			if interrupted {
				status = store.ChatRunInterrupted
			} else {
				status = store.ChatRunCancelled
			}
		} else {
			status = store.ChatRunFailed
		}
	}
	if err := s.finish(runID, status, result, runErr); err != nil {
		log.Printf("chatrun: finalize run %d: %v", runID, err)
		return
	}
	if s.onRunEnded != nil {
		s.onRunEnded(sessionID, status)
	}
}

func (s *Service) recordDelta(runID uint, delta chat.StreamEvent) error {
	payload, err := json.Marshal(delta)
	if err != nil {
		return fmt.Errorf("chatrun: encode progress: %w", err)
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	sequence := s.sequences[runID] + 1
	event := store.ChatRunEvent{RunID: runID, Sequence: sequence, Type: delta.Type, Payload: payload}
	if err := s.db.Create(&event).Error; err != nil {
		return fmt.Errorf("chatrun: persist progress: %w", err)
	}
	s.sequences[runID] = sequence
	s.publish(runID, ProgressEvent{Sequence: sequence, Type: delta.Type, Payload: payload})
	return nil
}

func (s *Service) finish(runID uint, status store.ChatRunStatus, result *Result, runErr error) error {
	finished := time.Now()
	update := map[string]any{"status": status, "finished_at": &finished}
	snapshot, snapshotErr := s.snapshot(runID)
	if snapshotErr == nil {
		update["snapshot"] = datatypes.NewJSONType(snapshot)
	}
	if runErr != nil {
		update["error"] = runErr.Error()
	}
	if result != nil {
		if result.FinalMessageID != nil {
			update["final_message_id"] = result.FinalMessageID
		}
		if result.Response != nil {
			encoded, err := json.Marshal(result.Response)
			if err == nil {
				update["result"] = datatypes.JSON(encoded)
			}
		}
	}
	if err := s.db.Model(&store.ChatRun{}).Where("id = ?", runID).Updates(update).Error; err != nil {
		return fmt.Errorf("chatrun: persist terminal state: %w", err)
	}
	s.publish(runID, ProgressEvent{Type: "done"})
	return nil
}

func (s *Service) snapshot(runID uint) (store.ChatRunSnapshot, error) {
	var events []store.ChatRunEvent
	if err := s.db.Where("run_id = ?", runID).Order("sequence").Find(&events).Error; err != nil {
		return store.ChatRunSnapshot{}, err
	}
	var snapshot store.ChatRunSnapshot
	for _, event := range events {
		var delta chat.StreamEvent
		if err := json.Unmarshal(event.Payload, &delta); err != nil {
			continue
		}
		snapshot.Sequence = event.Sequence
		switch delta.Type {
		case "delta":
			snapshot.Content += delta.Text
		case "reasoning":
			snapshot.ReasoningContent += delta.Text
		case "tool_call", "tool_output", "tool_result":
			snapshot.ToolCalls = appendJSON(snapshot.ToolCalls, delta.ToolCall)
		case "ask_permission":
			snapshot.Interaction = marshalJSON(delta.Interaction)
		case "session_updated":
			snapshot.SessionUpdate = marshalJSON(delta.Session)
		}
	}
	return snapshot, nil
}

func appendJSON(current datatypes.JSON, value any) datatypes.JSON {
	var values []json.RawMessage
	_ = json.Unmarshal(current, &values)
	encoded, err := json.Marshal(value)
	if err == nil {
		values = append(values, encoded)
	}
	encoded, _ = json.Marshal(values)
	return encoded
}

func marshalJSON(value any) datatypes.JSON {
	encoded, _ := json.Marshal(value)
	return encoded
}

// Get loads a run and maps missing rows to a domain error.
func (s *Service) Get(runID uint) (*store.ChatRun, error) {
	var run store.ChatRun
	if err := s.db.First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("chatrun: get: %w", err)
	}
	return &run, nil
}

// ActiveForSession returns the pending or running run for a session.
func (s *Service) ActiveForSession(sessionID uint) (*store.ChatRun, error) {
	var run store.ChatRun
	result := s.db.Where("session_id = ? AND status IN ?", sessionID, []store.ChatRunStatus{store.ChatRunPending, store.ChatRunRunning}).Limit(1).Find(&run)
	if result.Error != nil {
		return nil, fmt.Errorf("chatrun: active session run: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, ErrRunNotFound
	}
	return &run, nil
}

// Cancel cancels a live run after rejecting nonexistent and terminal rows.
func (s *Service) Cancel(runID uint) error {
	return s.ctrl.CancelRun(s.db, &store.ChatRun{}, runID, ErrRunNotFound, ErrRunFinished)
}

// CancelSession cancels the session's one active run, if it has one.
func (s *Service) CancelSession(sessionID uint) error {
	run, err := s.ActiveForSession(sessionID)
	if err != nil {
		return err
	}
	return s.Cancel(run.ID)
}

// Shutdown interrupts all active worker contexts. Startup reconciliation owns
// marking any unfinished persisted runs as interrupted.
func (s *Service) Shutdown() {
	s.progressMu.Lock()
	s.shuttingDown = true
	s.progressMu.Unlock()
	s.ctrl.Shutdown()
	s.wg.Wait()
}

// Reconcile marks runs left active by a previous process as interrupted.
func (s *Service) Reconcile() error {
	finished := time.Now()
	return s.db.Model(&store.ChatRun{}).
		Where("status IN ?", []store.ChatRunStatus{store.ChatRunPending, store.ChatRunRunning}).
		Updates(map[string]any{"status": store.ChatRunInterrupted, "finished_at": &finished, "error": "server restarted before chat generation finished"}).Error
}

// Subscribe registers before returning a snapshot. Consumers must use the
// returned run as their first state and then consume Events to avoid gaps.
func (s *Service) Subscribe(runID uint) (*store.ChatRun, []store.ChatRunEvent, *Subscription, error) {
	ch := make(chan ProgressEvent, 64)
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.mu.Lock()
	if s.subs[runID] == nil {
		s.subs[runID] = map[chan ProgressEvent]struct{}{}
	}
	s.subs[runID][ch] = struct{}{}
	s.mu.Unlock()
	sub := &Subscription{Events: ch, Close: func() { s.removeSub(runID, ch) }}
	run, err := s.Get(runID)
	if err != nil {
		s.removeSub(runID, ch)
		return nil, nil, nil, err
	}
	var events []store.ChatRunEvent
	if err := s.db.Where("run_id = ?", runID).Order("sequence").Find(&events).Error; err != nil {
		s.removeSub(runID, ch)
		return nil, nil, nil, fmt.Errorf("chatrun: list events: %w", err)
	}
	if run.Status.IsTerminal() {
		s.removeSub(runID, ch)
	}
	return run, events, sub, nil
}

func (s *Service) removeSub(runID uint, ch chan ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if subscribers := s.subs[runID]; subscribers != nil {
		if _, ok := subscribers[ch]; ok {
			delete(subscribers, ch)
			close(ch)
		}
		if len(subscribers) == 0 {
			delete(s.subs, runID)
		}
	}
}

func (s *Service) closeRun(runID uint) {
	s.progressMu.Lock()
	delete(s.sequences, runID)
	s.progressMu.Unlock()
	s.mu.Lock()
	for ch := range s.subs[runID] {
		close(ch)
	}
	delete(s.subs, runID)
	s.mu.Unlock()
}

func (s *Service) publish(runID uint, event ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs[runID] {
		select {
		case ch <- event:
		default:
			delete(s.subs[runID], ch)
			close(ch)
		}
	}
	if len(s.subs[runID]) == 0 {
		delete(s.subs, runID)
	}
}
