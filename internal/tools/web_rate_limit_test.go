package tools

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWebProviderQueuesRateLimitEachProviderIndependently(t *testing.T) {
	interval := 40 * time.Millisecond
	queues := newWebProviderQueuesWithInterval(interval)
	starts := make(chan struct {
		provider string
		at       time.Time
	}, 4)
	for _, provider := range []string{"brave", "brave", "brave", "sogou"} {
		go func() {
			if err := queues.wait(context.Background(), provider); err != nil {
				t.Errorf("wait(%s): %v", provider, err)
				return
			}
			starts <- struct {
				provider string
				at       time.Time
			}{provider: provider, at: time.Now()}
		}()
	}

	var braveStarts, sogouStarts []time.Time
	for range 4 {
		select {
		case start := <-starts:
			if start.provider == "brave" {
				braveStarts = append(braveStarts, start.at)
			} else {
				sogouStarts = append(sogouStarts, start.at)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for queued requests")
		}
	}
	if len(braveStarts) != 3 || len(sogouStarts) != 1 {
		t.Fatalf("unexpected starts: brave=%d sogou=%d", len(braveStarts), len(sogouStarts))
	}
	for index := 1; index < len(braveStarts); index++ {
		if gap := braveStarts[index].Sub(braveStarts[index-1]); gap < interval-5*time.Millisecond {
			t.Fatalf("brave requests started %v apart, want at least %v", gap, interval)
		}
	}
	if gap := braveStarts[0].Sub(sogouStarts[0]).Abs(); gap >= interval {
		t.Fatalf("different providers waited %v, want independent queues", gap)
	}
}

func TestWebProviderQueueHonorsCancellationWhileWaiting(t *testing.T) {
	queues := newWebProviderQueuesWithInterval(time.Second)
	if err := queues.wait(context.Background(), "brave"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := queues.wait(ctx, "brave"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled wait took %v", elapsed)
	}
}
