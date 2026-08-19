package interaction

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequestPermissionWaitsForApproval(t *testing.T) {
	manager := NewManager()
	events := make(chan Event, 1)
	ctx := WithReporter(context.Background(), func(event Event) { events <- event })
	done := make(chan error, 1)
	go func() { done <- manager.RequestPermission(ctx, 7, "Allow command?", "rm -rf build") }()

	select {
	case event := <-events:
		if event.Type != EventAskPermission || event.Request.SessionID != 7 {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("permission request was not reported")
	}
	if err := manager.Respond(7, true); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("request should be approved, got %v", err)
	}
}

func TestRequestPermissionReturnsDenied(t *testing.T) {
	manager := NewManager()
	events := make(chan Event, 1)
	ctx := WithReporter(context.Background(), func(event Event) { events <- event })
	done := make(chan error, 1)
	go func() { done <- manager.RequestPermission(ctx, 9, "Allow command?", "sudo reboot") }()
	<-events
	if err := manager.Respond(9, false); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if err := <-done; !errors.Is(err, ErrDenied) {
		t.Fatalf("expected denial, got %v", err)
	}
	if err := manager.Respond(9, true); !errors.Is(err, ErrNoPending) {
		t.Fatalf("expected no pending request, got %v", err)
	}
}

func TestAutoApproveCanBeEnabledAndDisabledPerSession(t *testing.T) {
	manager := NewManager()
	manager.SetAutoApprove(7, true)

	if err := manager.RequestPermission(context.Background(), 7, "Allow command?", "rm -rf build"); err != nil {
		t.Fatalf("auto-approved request: %v", err)
	}
	if !manager.AutoApprove(7) {
		t.Fatal("session should have automatic approval enabled")
	}
	if manager.AutoApprove(8) {
		t.Fatal("automatic approval must not affect another session")
	}

	manager.SetAutoApprove(7, false)
	if manager.AutoApprove(7) {
		t.Fatal("session should have automatic approval disabled")
	}
	events := make(chan Event, 1)
	ctx := WithReporter(context.Background(), func(event Event) { events <- event })
	done := make(chan error, 1)
	go func() { done <- manager.RequestPermission(ctx, 7, "Allow command?", "rm -rf build") }()
	<-events
	if err := manager.Respond(7, true); err != nil {
		t.Fatalf("approve after disabling automatic approval: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("request should be approved: %v", err)
	}
}

func TestEnableAutoApproveApprovesPendingRequest(t *testing.T) {
	manager := NewManager()
	events := make(chan Event, 1)
	ctx := WithReporter(context.Background(), func(event Event) { events <- event })
	done := make(chan error, 1)
	go func() { done <- manager.RequestPermission(ctx, 7, "Allow command?", "rm -rf build") }()
	<-events

	if err := manager.EnableAutoApprove(7); err != nil {
		t.Fatalf("enable automatic approval: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("pending request should be approved: %v", err)
	}
}

// TestRequestPermissionHonorsApprovalRacingCancellation guards against a
// Respond that races the caller's context cancellation: the buffered
// decision channel can hold an approval that select's pseudo-random choice
// would otherwise discard in favor of ctx.Done().
func TestRequestPermissionHonorsApprovalRacingCancellation(t *testing.T) {
	manager := NewManager()
	events := make(chan Event, 1)
	ctx, cancel := context.WithCancel(WithReporter(context.Background(), func(event Event) { events <- event }))
	done := make(chan error, 1)
	go func() { done <- manager.RequestPermission(ctx, 11, "Allow command?", "sudo reboot") }()
	<-events

	if err := manager.Respond(11, true); err != nil {
		t.Fatalf("approve: %v", err)
	}
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("expected the delivered approval to win over cancellation, got %v", err)
	}
}
