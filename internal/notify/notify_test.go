package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
