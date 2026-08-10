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
