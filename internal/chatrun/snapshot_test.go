package chatrun

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/chat"
)

// The stream emits tool arguments and output token by token. The snapshot
// must collapse those fragments into one entry per invocation rather than
// retaining every fragment, which previously made snapshot rebuild
// quadratic and left the stored snapshot unreadable.
func TestToolCallAccumulatorCoalescesStreamedFragments(t *testing.T) {
	calls := newToolCallAccumulator()
	calls.merge("tool_call", &chat.StreamToolCall{CallIndex: 0, Index: 0, ID: "call_a", Name: "shell", Status: "calling"})
	for _, fragment := range []string{`{"`, `command`, `":"`, `ls`, `"}`} {
		calls.merge("tool_call", &chat.StreamToolCall{CallIndex: 0, Index: 0, Arguments: fragment, Status: "calling"})
	}
	calls.merge("tool_output", &chat.StreamToolCall{CallIndex: 0, Index: 0, Result: "par", Status: "calling"})
	calls.merge("tool_output", &chat.StreamToolCall{CallIndex: 0, Index: 0, Result: "tial", Status: "calling"})
	calls.merge("tool_result", &chat.StreamToolCall{CallIndex: 0, Index: 0, Result: "complete output", Status: "complete"})

	var decoded []chat.StreamToolCall
	if err := json.Unmarshal(calls.encode(), &decoded); err != nil {
		t.Fatalf("decode snapshot tool calls: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 coalesced tool call, got %d", len(decoded))
	}
	got := decoded[0]
	if got.ID != "call_a" || got.Name != "shell" {
		t.Fatalf("expected identity carried from the first fragment, got id=%q name=%q", got.ID, got.Name)
	}
	if got.Arguments != `{"command":"ls"}` {
		t.Fatalf("expected concatenated arguments, got %q", got.Arguments)
	}
	// tool_result carries the complete content and supersedes the chunks
	// already streamed as tool_output.
	if got.Result != "complete output" {
		t.Fatalf("expected tool_result to replace streamed output, got %q", got.Result)
	}
	if got.Status != "complete" {
		t.Fatalf("expected terminal status, got %q", got.Status)
	}
}

// Parallel tool calls within one assistant iteration share a call_index and
// are distinguished only by index, so both must survive as separate entries.
func TestToolCallAccumulatorSeparatesParallelCallsAndPreservesOrder(t *testing.T) {
	calls := newToolCallAccumulator()
	calls.merge("tool_call", &chat.StreamToolCall{CallIndex: 0, Index: 0, ID: "a", Name: "shell", Arguments: "1", Status: "calling"})
	calls.merge("tool_call", &chat.StreamToolCall{CallIndex: 0, Index: 1, ID: "b", Name: "read", Arguments: "2", Status: "calling"})
	calls.merge("tool_call", &chat.StreamToolCall{CallIndex: 1, Index: 0, ID: "c", Name: "shell", Arguments: "3", Status: "calling"})
	calls.merge("tool_call", &chat.StreamToolCall{CallIndex: 0, Index: 0, Arguments: "9", Status: "calling"})

	var decoded []chat.StreamToolCall
	if err := json.Unmarshal(calls.encode(), &decoded); err != nil {
		t.Fatalf("decode snapshot tool calls: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("expected 3 distinct tool calls, got %d", len(decoded))
	}
	for i, want := range []struct {
		id        string
		arguments string
	}{{"a", "19"}, {"b", "2"}, {"c", "3"}} {
		if decoded[i].ID != want.id || decoded[i].Arguments != want.arguments {
			t.Fatalf("entry %d: expected id=%q arguments=%q, got id=%q arguments=%q",
				i, want.id, want.arguments, decoded[i].ID, decoded[i].Arguments)
		}
	}
}

func TestToolCallAccumulatorEncodesNothingWithoutCalls(t *testing.T) {
	if encoded := newToolCallAccumulator().encode(); encoded != nil {
		t.Fatalf("expected nil for a run with no tool calls, got %s", encoded)
	}
}

// Reproduces the event mix of chat run 1040: 33,526 tool fragments spread
// over 121 invocations.
func TestToolCallAccumulatorHandlesProductionScale(t *testing.T) {
	const (
		invocations = 121
		fragments   = 33526
	)
	calls := newToolCallAccumulator()
	for i := 0; i < fragments; i++ {
		callIndex := i % invocations
		calls.merge("tool_call", &chat.StreamToolCall{
			CallIndex: callIndex, Index: 0,
			ID: fmt.Sprintf("call_%d", callIndex), Name: "shell",
			Arguments: "x", Status: "calling",
		})
	}
	encoded := calls.encode()
	t.Logf("fragments=%d entries=%d snapshot_bytes=%d", fragments, invocations, len(encoded))
	if len(encoded) > 64*1024 {
		t.Fatalf("expected a compact snapshot, got %d bytes", len(encoded))
	}
}
