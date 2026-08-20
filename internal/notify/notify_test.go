package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifierRecentConsumesEntries(t *testing.T) {
	n := New("")
	n.Warn("first warning")
	n.Error("second error")

	entries := n.Recent()
	if len(entries) != 2 {
		t.Fatalf("len(Recent()) = %d, want 2", len(entries))
	}
	if entries[0].Message != "first warning" {
		t.Errorf("first entry message = %q, want %q", entries[0].Message, "first warning")
	}
	if entries[1].Message != "second error" {
		t.Errorf("second entry message = %q, want %q", entries[1].Message, "second error")
	}

	if entries := n.Recent(); len(entries) != 0 {
		t.Errorf("second Recent() returned %d entries, want 0", len(entries))
	}
}

func TestNotifierDeliversThroughWorker(t *testing.T) {
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := New(server.URL)
	n.Warn("hello %s", "world")

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("notification was not delivered")
	}
	if err := n.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestNotifierShutdownCancelsRetryWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	n := New(server.URL)
	n.Error("will retry")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestNotifierShutdownDrainsAcceptedEntries(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := New(server.URL)
	for index := 0; index < 5; index++ {
		n.Warn("queued %d", index)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := n.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := received.Load(); got != 5 {
		t.Fatalf("delivered %d entries, want 5", got)
	}
}
