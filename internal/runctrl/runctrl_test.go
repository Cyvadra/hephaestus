package runctrl

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestCancelActiveRun(t *testing.T) {
	c := New()
	var cancelled atomic.Bool
	c.Register(1, func() { cancelled.Store(true) })

	if err := c.Cancel(1); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !cancelled.Load() {
		t.Fatal("expected cancel to be invoked")
	}
}

func TestCancelUnknownRunReturnsErrNotActive(t *testing.T) {
	c := New()
	if err := c.Cancel(42); !errors.Is(err, ErrNotActive) {
		t.Fatalf("expected ErrNotActive, got %v", err)
	}
}

func TestReleaseCancelsAndForgets(t *testing.T) {
	c := New()
	var cancelled atomic.Int32
	c.Register(1, func() { cancelled.Add(1) })

	c.Release(1)
	c.Release(1) // idempotent
	if cancelled.Load() != 1 {
		t.Fatalf("cancel invocations = %d, want 1", cancelled.Load())
	}
	if err := c.Cancel(1); !errors.Is(err, ErrNotActive) {
		t.Fatalf("expected ErrNotActive after release, got %v", err)
	}
}

func TestShutdownCancelsEveryRun(t *testing.T) {
	c := New()
	var cancelled atomic.Int32
	c.Register(1, func() { cancelled.Add(1) })
	c.Register(2, func() { cancelled.Add(1) })

	c.Shutdown()
	if cancelled.Load() != 2 {
		t.Fatalf("cancel invocations = %d, want 2", cancelled.Load())
	}
}

func TestRegisterReplacesExistingHandle(t *testing.T) {
	c := New()
	var first atomic.Int32
	var second atomic.Int32
	c.Register(1, func() { first.Add(1) })
	c.Register(1, func() { second.Add(1) })

	if err := c.Cancel(1); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if first.Load() != 0 || second.Load() != 1 {
		t.Fatalf("first = %d, second = %d", first.Load(), second.Load())
	}
}

func TestCancelContextIsDerivedCancellable(t *testing.T) {
	c := New()
	cancel := context.CancelFunc(func() {})
	// Sanity: Register accepts a context.CancelFunc (the type executors pass).
	c.Register(1, cancel)
	_ = cancel
}
