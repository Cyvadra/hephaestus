package tools

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

const (
	// defaultCommandTimeout is used when NewShellTool receives a zero timeout.
	defaultCommandTimeout = 30 * time.Second
	defaultWaitDelay      = 2 * time.Second
	maxRetainedOutput     = 1024 * 1024
)

type streamingOutputWriter struct {
	ctx       context.Context
	mu        sync.Mutex
	retained  []byte
	truncated bool
}

func (w *streamingOutputWriter) Write(chunk []byte) (int, error) {
	w.mu.Lock()
	if len(chunk) >= maxRetainedOutput {
		w.retained = append(w.retained[:0], chunk[len(chunk)-maxRetainedOutput:]...)
		w.truncated = true
	} else {
		overflow := len(w.retained) + len(chunk) - maxRetainedOutput
		if overflow > 0 {
			copy(w.retained, w.retained[overflow:])
			w.retained = w.retained[:len(w.retained)-overflow]
			w.truncated = true
		}
		w.retained = append(w.retained, chunk...)
	}
	w.mu.Unlock()
	// Reported outside the lock: the caller may be a slow SSE consumer, and
	// this must not back-pressure into the command's stdout/stderr drain.
	toolkit.ReportOutput(w.ctx, string(chunk))
	return len(chunk), nil
}

func (w *streamingOutputWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return "[earlier output omitted]\n" + string(w.retained)
	}
	return string(w.retained)
}

var _ io.Writer = (*streamingOutputWriter)(nil)

// ShellTool runs one shell command in the current Project. It intentionally
// exposes ordinary shell semantics only: use standard shell commands for
// inspection, file operations, background jobs, and process management.
type ShellTool struct {
	enabled      bool
	access       FileAccessConfig
	timeout      time.Duration
	hostInfo     string
	interactions *interaction.Manager
}

func NewShellTool(enabled bool, timeout time.Duration) *ShellTool {
	return NewShellToolWithAccess(enabled, timeout, FileAccessConfig{})
}

func NewShellToolWithAccess(enabled bool, timeout time.Duration, access FileAccessConfig) *ShellTool {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return &ShellTool{enabled: enabled, access: access, timeout: timeout, hostInfo: captureHostInfo()}
}

// SetInteractionManager enables user confirmation for commands matched by
// the high-risk policy. It is optional so direct callers can use ShellTool
// without an HTTP interaction surface.
func (t *ShellTool) SetInteractionManager(manager *interaction.Manager) {
	t.interactions = manager
}

// captureHostInfo records the output of `uname -a` once at construction so
// the model sees the exact host environment (kernel, architecture, hostname)
// whenever the shell tool is registered to a request. The platform targets
// Linux/Unix, where uname is always available; on any failure the example is
// simply omitted rather than failing construction.
func captureHostInfo() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uname", "-a").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Example returns a concrete shell invocation (uname -a) together with the
// host's actual response, so the model sees both the tool's I/O format and
// the environment it runs in. It is attached to the tool's description when
// the tool is registered to an LLM request.
func (t *ShellTool) Example() string {
	if t.hostInfo == "" {
		return ""
	}
	return `{"command": "uname -a"}` + "\n\u2192 " + t.hostInfo
}

func (ShellTool) Name() string       { return "shell" }
func (t *ShellTool) Available() bool { return t.enabled }
func (ShellTool) Description() string {
	return "Runs one shell command in the current Project and returns stdout and stderr. Use ordinary shell commands for file inspection, editing, searching, tests, and process control."
}
func (ShellTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"command": map[string]any{"type": "string", "description": "A complete shell command to run."},
		"working_directory": map[string]any{
			"type":        "string",
			"description": "Execution directory. Defaults to the bound Project; shared temporary paths are also allowed.",
		},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
	}, "required": []string{"command"}}
}
func (t *ShellTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	if !t.enabled {
		return toolkit.ErrorResult("shell: disabled by server configuration")
	}
	return t.run(ctx, args)
}

func (t *ShellTool) run(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return toolkit.ErrorResult("shell: command is required")
	}
	if denied, pattern := deniedCommand(command); denied {
		if t.interactions == nil {
			return toolkit.ErrorResult(fmt.Sprintf("shell: command rejected by safety policy (%s)", pattern))
		}
		if err := t.requestPermission(ctx, command, pattern); err != nil {
			return toolkit.ErrorResult("shell: " + err.Error())
		}
	}
	workingDir, _ := args["working_directory"].(string)
	if workingDir == "" {
		var ok bool
		workingDir, ok = toolkit.WorkspaceFromContext(ctx)
		if !ok {
			return toolkit.ErrorResult("shell: requires a Project-bound session or an allowed working_directory")
		}
	} else {
		resolved, err := projectPath(ctx, workingDir, t.access)
		if err != nil {
			return toolkit.ErrorResult("shell: working_directory: " + err.Error())
		}
		workingDir = resolved
	}
	timeout := t.timeout
	if value, ok := args["timeout_seconds"].(float64); ok {
		if value < 1 || value > 300 {
			value = 300 // safety net for direct callers; schema enforces 1..300 via RunTool
		}
		timeout = time.Duration(value) * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := commandFor(execCtx, command, workingDir)
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killProcessGroup(cmd.Process)
	}
	cmd.WaitDelay = defaultWaitDelay
	output := &streamingOutputWriter{ctx: execCtx}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if execCtx.Err() == context.DeadlineExceeded {
		return toolkit.ErrorResult(fmt.Sprintf("shell: command timed out after %s\n%s", timeout, output.String()))
	}
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("shell: %v\n%s", err, output.String()))
	}
	return toolkit.SilentResult(output.String())
}

func commandFor(ctx context.Context, command, dir string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
		cmd.Dir = dir
		return cmd
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	return cmd
}

func (t *ShellTool) requestPermission(ctx context.Context, command, pattern string) error {
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok || sessionID == 0 {
		return fmt.Errorf("command rejected by safety policy (%s): no session available for confirmation", pattern)
	}
	if !interaction.HasReporter(ctx) {
		return fmt.Errorf("command requires interactive approval; use the streaming message endpoint")
	}
	return t.interactions.RequestPermission(ctx, sessionID, "Allow high-risk command?", fmt.Sprintf("Command:\n%s\n\nMatched safety rule: %s", command, pattern))
}
