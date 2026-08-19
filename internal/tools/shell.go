package tools

import (
	"context"
	"fmt"
	"io"
	"os/exec"
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
	output := normalizeCarriageReturns(string(w.retained))
	if w.truncated {
		return "[earlier output omitted]\n" + output
	}
	return output
}

// normalizeCarriageReturns removes transient progress text overwritten by a
// carriage return. Chunks are still reported unchanged so the client can
// render terminal updates as they arrive; this normalization is only for the
// final tool result that is sent to the LLM and persisted.
func normalizeCarriageReturns(output string) string {
	normalized := make([]byte, 0, len(output))
	lineStart := 0
	for index := 0; index < len(output); index++ {
		switch output[index] {
		case '\r':
			if index+1 < len(output) && output[index+1] == '\n' {
				normalized = append(normalized, '\n')
				lineStart = len(normalized)
				index++
				continue
			}
			normalized = normalized[:lineStart]
		case '\n':
			normalized = append(normalized, '\n')
			lineStart = len(normalized)
		default:
			normalized = append(normalized, output[index])
		}
	}
	return string(normalized)
}

var _ io.Writer = (*streamingOutputWriter)(nil)

// ShellTool runs one shell command in the current Project. It intentionally
// exposes ordinary shell semantics only: use standard shell commands for
// inspection, file operations, background jobs, and process management.
type ShellTool struct {
	enabled      bool
	timeout      time.Duration
	backend      shellBackend
	interactions *interaction.Manager
}

// ShellConfig selects the host on which commands run. The tool's public
// parameters are independent of this configuration.
type ShellConfig struct {
	Enabled         bool
	Timeout         time.Duration
	Access          FileAccessConfig
	Backend         string
	SSHDestination  string
	SSHProjectsRoot string
}

type shellBackend interface {
	command(context.Context, string, string) (*exec.Cmd, error)
	workingDirectory(context.Context, string) (string, error)
	hostInfo() string
}

func NewShellTool(enabled bool, timeout time.Duration) *ShellTool {
	return NewShellToolWithAccess(enabled, timeout, FileAccessConfig{})
}

func NewShellToolWithAccess(enabled bool, timeout time.Duration, access FileAccessConfig) *ShellTool {
	tool, err := NewShellToolWithConfig(ShellConfig{Enabled: enabled, Timeout: timeout, Access: access, Backend: "local"})
	if err != nil {
		panic(err)
	}
	return tool
}

// NewShellToolWithConfig constructs a tool whose local or SSH backend is
// selected at process startup. SSH authentication remains the responsibility
// of the user's OpenSSH configuration and agent.
func NewShellToolWithConfig(config ShellConfig) (*ShellTool, error) {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	var backend shellBackend
	switch config.Backend {
	case "", "local":
		backend = localShellBackend{access: config.Access}
	case "ssh":
		sshBackend, err := newSSHShellBackend(config.SSHDestination, config.SSHProjectsRoot, config.Access)
		if err != nil {
			return nil, err
		}
		if config.Enabled {
			if err := sshBackend.probe(); err != nil {
				return nil, err
			}
		}
		backend = sshBackend
	default:
		return nil, fmt.Errorf("shell: unsupported backend %q", config.Backend)
	}
	return &ShellTool{enabled: config.Enabled, timeout: timeout, backend: backend}, nil
}

// SetInteractionManager enables user confirmation requested by the model or
// required by the fallback policy. It is wired from cmd/hephaestus/main.go;
// direct callers can omit it when no interactive approval surface is available.
func (t *ShellTool) SetInteractionManager(manager *interaction.Manager) {
	t.interactions = manager
}

// Example returns a concrete shell invocation (uname -a) together with the
// host's actual response, so the model sees both the tool's I/O format and
// the environment it runs in. It is attached to the tool's description when
// the tool is registered to an LLM request.
func (t *ShellTool) Example() string {
	hostInfo := t.backend.hostInfo()
	if hostInfo == "" {
		return ""
	}
	return `{"command": "uname -a", "requires_confirmation": false}` + "\n\u2192 " + hostInfo
}

func (ShellTool) Name() string       { return "shell" }
func (t *ShellTool) Available() bool { return t.enabled }
func (ShellTool) Audited() bool      { return true }
func (ShellTool) Description() string {
	return "Runs one shell command on the configured execution host in the current Project and returns stdout and stderr. Use ordinary shell commands for file inspection, editing, searching, tests, and process control. Request user confirmation for commands that may be destructive, elevate privileges, change system state, or execute untrusted external code."
}
func (ShellTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"command": map[string]any{"type": "string", "description": "A complete shell command to run."},
		"requires_confirmation": map[string]any{
			"type":        "boolean",
			"description": "Whether the command should require explicit user approval before execution. Set true for potentially destructive operations, privilege elevation, system-state changes, or execution of untrusted external code. Defaults to false; server safety rules may still require approval.",
		},
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
	requiresConfirmation, _ := args["requires_confirmation"].(bool)
	denied, _ := deniedCommand(command)
	if requiresConfirmation || denied {
		if t.interactions == nil {
			return toolkit.ErrorResult("shell: command requires interactive approval, but interactions are not configured")
		}
		if err := t.requestPermission(ctx, command); err != nil {
			return toolkit.ErrorResult("shell: " + err.Error())
		}
	}
	workingDir, _ := args["working_directory"].(string)
	resolvedWorkingDir, err := t.backend.workingDirectory(ctx, workingDir)
	if err != nil {
		return toolkit.ErrorResult("shell: working_directory: " + err.Error())
	}
	timeout := t.timeout
	if value, ok := args["timeout_seconds"].(float64); ok {
		// Values outside the schema's 1..300 range fall back to the tool
		// default rather than being clamped to the maximum: a tiny value must
		// not silently become the most permissive timeout.
		if value < 1 || value > 300 {
			timeout = t.timeout
		} else {
			timeout = time.Duration(value) * time.Second
		}
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := t.backend.command(execCtx, command, resolvedWorkingDir)
	if err != nil {
		return toolkit.ErrorResult("shell: " + err.Error())
	}
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
	err = cmd.Run()
	if execCtx.Err() == context.DeadlineExceeded {
		return toolkit.ErrorResult(fmt.Sprintf("shell: command timed out after %s\n%s", timeout, output.String()))
	}
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("shell: %v\n%s", err, output.String()))
	}
	return toolkit.SilentResult(output.String())
}

func (t *ShellTool) requestPermission(ctx context.Context, command string) error {
	sessionID, ok := toolkit.SessionIDFromContext(ctx)
	if !ok || sessionID == 0 {
		return fmt.Errorf("command requires interactive approval, but no session is available")
	}
	if !interaction.HasReporter(ctx) {
		return fmt.Errorf("command requires interactive approval; use the streaming message endpoint")
	}
	return t.interactions.RequestPermission(ctx, sessionID, "Allow high-risk command?", fmt.Sprintf("Command:\n%s", command))
}
