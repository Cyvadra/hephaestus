package channel

import (
	"context"
	"testing"

	"github.com/Cyvadra/hephaestus/pkg/channels"
)

func TestIsApproval(t *testing.T) {
	for _, input := range []string{"确认", "好的，确认执行", "YES", "yes please", "y", "reply: y", "1"} {
		if !isApproval(input) {
			t.Errorf("isApproval(%q) = false", input)
		}
	}
	for _, input := range []string{"no", "deny", "maybe", "10", ""} {
		if isApproval(input) {
			t.Errorf("isApproval(%q) = true", input)
		}
	}
}

func TestChannelTurnOptionsPreservesExpectedLeaf(t *testing.T) {
	leaf := uint(42)
	options := channelTurnOptions(&leaf, nil)
	if options.ExpectedLeaf == nil || *options.ExpectedLeaf != leaf {
		t.Fatalf("ExpectedLeaf = %v, want %d", options.ExpectedLeaf, leaf)
	}
}

func TestStopClosesQueuesAndPreventsNewWorkers(t *testing.T) {
	svc := New(nil, nil, nil, nil, nil, nil, nil)
	queue := make(chan channels.InboundMessage)
	svc.queues["qq\x00chat"] = queue
	svc.workers.Add(1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		svc.runStack(context.Background(), queue)
	}()

	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	select {
	case <-workerDone:
	default:
		t.Fatal("worker did not stop")
	}
	svc.handleInbound(context.Background(), channels.InboundMessage{Channel: "qq", ChatID: "after-stop"})
	if len(svc.queues) != 0 {
		t.Fatalf("queues after stopped handleInbound = %d, want 0", len(svc.queues))
	}
}
