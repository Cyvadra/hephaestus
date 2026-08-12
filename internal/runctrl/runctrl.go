// Package runctrl owns the cancellation lifecycle shared by the long-running
// executors (workflows and jobs): a registry of active runs and their cancel
// functions, plus the terminal-state guard used by Cancel. It removes the
// duplicated mu/ctrl/cancel/reconcile scaffolding that each executor used to
// carry on its own.
package runctrl

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gorm.io/gorm"
)

// ErrNotActive is returned by Cancel when the run has no live executor handle
// registered (it is finishing, or its executor was never started).
var ErrNotActive = errors.New("runctrl: run has no active cancel handle")

// TerminalRow is a persisted run row that knows whether it has finished.
type TerminalRow interface {
	Terminal() bool
}

// Controller tracks the active runs of one executor type. It is safe for
// concurrent use.
type Controller struct {
	mu   sync.Mutex
	ctrl map[uint]context.CancelFunc
}

// New creates an empty Controller.
func New() *Controller {
	return &Controller{ctrl: map[uint]context.CancelFunc{}}
}

// Register records the cancel function for a run. Registering an existing id
// replaces its handle.
func (c *Controller) Register(id uint, cancel context.CancelFunc) {
	c.mu.Lock()
	c.ctrl[id] = cancel
	c.mu.Unlock()
}

// Cancel cancels an active run. It does not remove the handle; the executor's
// deferred Release does. It returns ErrNotActive when no live handle exists.
func (c *Controller) Cancel(id uint) error {
	c.mu.Lock()
	cancel, ok := c.ctrl[id]
	c.mu.Unlock()
	if !ok {
		return ErrNotActive
	}
	cancel()
	return nil
}

// Release cancels and forgets a run. It is idempotent and safe to defer.
func (c *Controller) Release(id uint) {
	c.mu.Lock()
	cancel, ok := c.ctrl[id]
	if ok {
		delete(c.ctrl, id)
	}
	c.mu.Unlock()
	if ok {
		cancel()
	}
}

// Shutdown cancels every active run. Used during process shutdown; workers
// observe cancellation and finalize their run statuses.
func (c *Controller) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cancel := range c.ctrl {
		cancel()
	}
}

// CancelRun is the persisted-run cancellation shared by the job and workflow
// services: load the row into run, reject finished runs, then cancel the
// live handle. errNotFound/errFinished are the caller's API sentinels.
func (c *Controller) CancelRun(db *gorm.DB, run TerminalRow, id uint, errNotFound, errFinished error) error {
	if err := db.First(run, id).Error; err != nil {
		return errNotFound
	}
	if run.Terminal() {
		return fmt.Errorf("%w: run %d already finished", errFinished, id)
	}
	if err := c.Cancel(id); err != nil {
		// The run exists but has no live executor handle: it is finishing or
		// has not been picked up yet, so it is no longer cancellable.
		return fmt.Errorf("%w: run %d is not actively executing", errFinished, id)
	}
	return nil
}
