package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// processTool attaches a fake process and finishes when it receives one of
// exitOn, or when release closes.
type processTool struct {
	exitOn  map[os.Signal]bool
	release chan struct{}
	signals chan os.Signal
}

func (processTool) Name() string               { return "process" }
func (processTool) Description() string        { return "" }
func (processTool) Parameters() map[string]any { return nil }
func (t processTool) Execute(ctx context.Context, _ map[string]any) *toolkit.ToolResult {
	exited := make(chan os.Signal, 1)
	if interrupter, ok := toolkit.InterrupterFromContext(ctx); ok {
		detach := interrupter.Attach(4242, "sleep 600", func(sig os.Signal) error {
			t.signals <- sig
			if t.exitOn[sig] {
				exited <- sig
			}
			return nil
		})
		defer detach()
	}
	select {
	case sig := <-exited:
		return toolkit.ErrorResult("terminated by " + sig.String())
	case <-t.release:
		return toolkit.NewToolResult("finished late")
	}
}

type recorder struct {
	mu      sync.Mutex
	results []LateToolResult
	done    chan struct{}
}

func (r *recorder) RecordLateToolResult(result LateToolResult) error {
	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()
	close(r.done)
	return nil
}

func withFastSteer(t *testing.T) {
	t.Helper()
	grace, term, kill, poll := steerGracePeriod, steerTermWait, steerKillWait, steerPollInterval
	steerGracePeriod, steerTermWait, steerKillWait, steerPollInterval = 20*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { steerGracePeriod, steerTermWait, steerKillWait, steerPollInterval = grace, term, kill, poll })
}

func forceSteerRequest(fake *fakeLLM, tool toolkit.Tool) Request {
	turn := plugin.TurnContext{SessionID: 7, Scope: toolkit.ScopeSession, Messages: []store.ChatMessage{{Role: ds4.RoleUser, Content: "hi"}}, Metadata: map[string]any{}}
	req := sessionRequest(fake, []toolkit.Tool{tool}, turn)
	sessionID := uint(7)
	req.Audit.SessionID = &sessionID
	req.ClaimSteering = func() (*Steering, error) { return nil, nil }
	var once sync.Once
	req.ClaimAggressiveSteering = func() (steering *Steering, err error) {
		once.Do(func() { steering = &Steering{Token: 9, Text: "stop now", Aggressive: true} })
		return steering, nil
	}
	return req
}

func TestRun_ForceSteerTerminatesRunningTool(t *testing.T) {
	withFastSteer(t)
	fake := &fakeLLM{responses: []*ds4.ChatResponse{
		respWith([]ds4.ToolCall{toolCall("call-1", "process", "")}, ""),
		respWith(nil, "done"),
	}}
	tool := processTool{exitOn: map[os.Signal]bool{syscall.SIGTERM: true}, release: make(chan struct{}), signals: make(chan os.Signal, 4)}
	result, err := testRunner(fake, nil).Run(context.Background(), forceSteerRequest(fake, tool))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := result.Messages[1].Content
	for _, want := range []string{"terminated by terminated", `tool-execution="interrupted"`, `signal="SIGTERM"`, "stop now"} {
		if !strings.Contains(content, want) {
			t.Fatalf("tool result %q missing %q", content, want)
		}
	}
	if len(result.Steering) != 1 || result.Steering[0].Token != 9 {
		t.Fatalf("claimed steering = %+v", result.Steering)
	}
}

func TestRun_ForceSteerAbandonsStuckToolAndReportsLateCompletion(t *testing.T) {
	withFastSteer(t)
	fake := &fakeLLM{responses: []*ds4.ChatResponse{
		respWith([]ds4.ToolCall{toolCall("call-1", "process", `{"x":1}`)}, ""),
		respWith(nil, "done"),
	}}
	tool := processTool{release: make(chan struct{}), signals: make(chan os.Signal, 4)}
	runner := testRunner(fake, nil)
	late := &recorder{done: make(chan struct{})}
	runner.SetLateToolRecorder(late)

	result, err := runner.Run(context.Background(), forceSteerRequest(fake, tool))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	content := result.Messages[1].Content
	for _, want := range []string{`tool-execution="abandoned"`, `pid="4242"`, "stop now"} {
		if !strings.Contains(content, want) {
			t.Fatalf("tool result %q missing %q", content, want)
		}
	}
	if got := []os.Signal{<-tool.signals, <-tool.signals}; got[0] != syscall.SIGTERM || got[1] != syscall.SIGKILL {
		t.Fatalf("signals = %v", got)
	}

	close(tool.release)
	select {
	case <-late.done:
	case <-time.After(2 * time.Second):
		t.Fatal("late completion was not recorded")
	}
	got := late.results[0]
	if got.SessionID != 7 || got.PID != 4242 || got.Command != "sleep 600" || got.Output != "finished late" || got.ToolCallID != "call-1" || got.EndedAt.IsZero() {
		t.Fatalf("late result = %+v", got)
	}
}
