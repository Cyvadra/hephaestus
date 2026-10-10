package toolkit

import (
	"context"
	"os"
	"sync"
	"time"
)

// Interrupter lets the agent signal the OS process behind a running tool
// call, e.g. to stop a shell command when the user force-steers the run.
// Tools that spawn a process Attach it; other tools ignore the interrupter
// and only observe context cancellation.
type Interrupter struct {
	mu        sync.Mutex
	pid       int
	command   string
	startedAt time.Time
	signal    func(os.Signal) error
}

// ProcessInfo describes the process attached to an Interrupter.
type ProcessInfo struct {
	PID       int
	Command   string
	StartedAt time.Time
}

type interrupterContextKey struct{}

// WithInterrupter attaches interrupter to ctx for the tool being executed.
func WithInterrupter(ctx context.Context, interrupter *Interrupter) context.Context {
	return context.WithValue(ctx, interrupterContextKey{}, interrupter)
}

// InterrupterFromContext returns the interrupter attached by WithInterrupter.
func InterrupterFromContext(ctx context.Context) (*Interrupter, bool) {
	interrupter, ok := ctx.Value(interrupterContextKey{}).(*Interrupter)
	return interrupter, ok && interrupter != nil
}

// Attach registers the running process and how to signal it (typically its
// whole process group). The returned detach must be called once the process
// has exited so later signals cannot reach a reused pid.
func (i *Interrupter) Attach(pid int, command string, signal func(os.Signal) error) (detach func()) {
	i.mu.Lock()
	i.pid, i.command, i.startedAt, i.signal = pid, command, time.Now(), signal
	i.mu.Unlock()
	return func() {
		i.mu.Lock()
		i.signal = nil
		i.mu.Unlock()
	}
}

// Signal sends sig to the attached process. It reports false when no live
// process is attached.
func (i *Interrupter) Signal(sig os.Signal) bool {
	i.mu.Lock()
	signal := i.signal
	i.mu.Unlock()
	return signal != nil && signal(sig) == nil
}

// Process returns the most recently attached process, if any.
func (i *Interrupter) Process() (ProcessInfo, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return ProcessInfo{PID: i.pid, Command: i.command, StartedAt: i.startedAt}, i.pid != 0
}
