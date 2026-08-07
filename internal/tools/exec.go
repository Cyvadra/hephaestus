package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/creack/pty"
)

const (
	// defaultForegroundTimeout is used when NewExecTool receives a zero
	// timeout for foreground commands.
	defaultForegroundTimeout = 30 * time.Second
	// defaultWriteTimeout bounds how long exec waits for a process to
	// consume input before giving up, so a wedged child can't wedge the
	// session's management API.
	defaultWriteTimeout = 5 * time.Second
	defaultWaitDelay    = 2 * time.Second
)

// ExecTool lets the LLM run commands in the current Project, including
// long-running background and interactive PTY sessions. The sessions it
// starts are owned by a processManager with a bounded count and explicit
// Shutdown; a session is only ever addressable from the chat session that
// started it.
type ExecTool struct {
	enabled      bool
	access       FileAccessConfig
	timeout      time.Duration
	writeTimeout time.Duration
	sessions     *processManager
}

func NewExecTool(enabled bool, timeout time.Duration) *ExecTool {
	return NewExecToolWithAccess(enabled, timeout, FileAccessConfig{})
}

func NewExecToolWithAccess(enabled bool, timeout time.Duration, access FileAccessConfig) *ExecTool {
	if timeout <= 0 {
		timeout = defaultForegroundTimeout
	}
	return &ExecTool{enabled: enabled, access: access, timeout: timeout, writeTimeout: defaultWriteTimeout, sessions: newProcessManager(0)}
}

// Shutdown terminates all background sessions; call on server stop so no
// process outlives the server.
func (t *ExecTool) Shutdown() { t.sessions.shutdown() }

func (ExecTool) Name() string       { return "exec" }
func (t *ExecTool) Available() bool { return t.enabled }
func (ExecTool) Description() string {
	return "Executes commands in the current Project. Supports run, list, poll, read, write, send-keys, and kill for background sessions."
}
func (ExecTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"action": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "session_id": map[string]any{"type": "string"},
		"data": map[string]any{"type": "string"}, "background": map[string]any{"type": "boolean"}, "pty": map[string]any{"type": "boolean"},
		"keys": map[string]any{"type": "string"},
		"working_directory": map[string]any{
			"type":        "string",
			"description": "Execution directory. Defaults to the bound Project; shared temporary paths are also allowed.",
		},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
	}, "required": []string{"action"}}
}
func (t *ExecTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	if !t.enabled {
		return toolkit.ErrorResult("exec: disabled by server configuration")
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "run"
	}
	switch action {
	case "run":
		return t.run(ctx, args)
	case "list":
		return t.list(ctx)
	case "poll":
		return t.poll(ctx, args, false)
	case "read":
		return t.poll(ctx, args, true)
	case "write":
		return t.write(ctx, args)
	case "send-keys":
		return t.sendKeys(ctx, args)
	case "kill":
		return t.kill(ctx, args)
	default:
		return toolkit.ErrorResult("exec: unknown action " + action)
	}
}

func (t *ExecTool) run(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return toolkit.ErrorResult("exec: command is required")
	}
	if denied, pattern := deniedCommand(command); denied {
		return toolkit.ErrorResult(fmt.Sprintf("exec: command rejected by safety policy (%s)", pattern))
	}
	workingDir, _ := args["working_directory"].(string)
	if workingDir == "" {
		var ok bool
		workingDir, ok = toolkit.WorkspaceFromContext(ctx)
		if !ok {
			return toolkit.ErrorResult("exec: requires a Project-bound session or an allowed working_directory")
		}
	} else {
		resolved, err := projectPath(ctx, workingDir, t.access)
		if err != nil {
			return toolkit.ErrorResult("exec: working_directory: " + err.Error())
		}
		workingDir = resolved
	}
	background, _ := args["background"].(bool)
	ptyEnabled, _ := args["pty"].(bool)
	if ptyEnabled && !background {
		return toolkit.ErrorResult("exec: pty requires background=true")
	}
	if background {
		ownerSession, _ := toolkit.SessionIDFromContext(ctx)
		return t.runBackground(command, workingDir, ptyEnabled, ownerSession)
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
	output, err := cmd.CombinedOutput()
	if execCtx.Err() == context.DeadlineExceeded {
		return toolkit.ErrorResult(fmt.Sprintf("exec: command timed out after %s\n%s", timeout, output))
	}
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("exec: %v\n%s", err, output))
	}
	return toolkit.SilentResult(string(output))
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

func (t *ExecTool) runBackground(command, dir string, ptyEnabled bool, ownerSession uint) *toolkit.ToolResult {
	cmd := commandFor(context.Background(), command, dir)
	if ptyEnabled {
		return t.runPTYBackground(cmd, command, ownerSession)
	}
	// Make the child a process-group leader so a kill reaches its whole
	// tree. The PTY path gets the same effect from creack/pty's Setsid and
	// must not also set Setpgid (the two flags conflict in the child).
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	if err := cmd.Start(); err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	session := newSession(command, ownerSession, cmd.Process.Pid, cmd.Process)
	session.stdin = stdin
	if err := t.sessions.add(session); err != nil {
		_ = session.kill()
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	go session.pump(stdout)
	go session.pump(stderr)
	go t.waitFor(cmd, session)
	return execResult(map[string]any{"session_id": session.id, "status": "running", "pid": session.pid})
}

func (t *ExecTool) runPTYBackground(cmd *exec.Cmd, command string, ownerSession uint) *toolkit.ToolResult {
	terminal, err := pty.Start(cmd)
	if err != nil {
		return toolkit.ErrorResult("exec: start PTY: " + err.Error())
	}
	session := newSession(command, ownerSession, cmd.Process.Pid, cmd.Process)
	session.pty = terminal
	if err := t.sessions.add(session); err != nil {
		_ = session.kill()
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	go session.pump(terminal)
	go t.waitFor(cmd, session)
	return execResult(map[string]any{"session_id": session.id, "status": "running", "pid": session.pid, "pty": true})
}

// waitFor records a session's terminal state once the process exits. It
// only overwrites "running" so an explicit kill's status wins.
func (t *ExecTool) waitFor(cmd *exec.Cmd, session *processSession) {
	err := cmd.Wait()
	session.mu.Lock()
	defer session.mu.Unlock()
	if cmd.ProcessState != nil {
		session.exitCode = cmd.ProcessState.ExitCode()
	}
	if session.status == "running" {
		if err != nil {
			session.status = "failed"
		} else {
			session.status = "done"
		}
	}
	if session.pty != nil {
		_ = session.pty.Close()
	} else if session.stdin != nil {
		_ = session.stdin.Close()
	}
}

func (t *ExecTool) list(ctx context.Context) *toolkit.ToolResult {
	ownerSession, _ := toolkit.SessionIDFromContext(ctx)
	sessions := t.sessions.listFor(ownerSession)
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionInfo(s, false))
	}
	return execResult(map[string]any{"sessions": out})
}

func (t *ExecTool) poll(ctx context.Context, args map[string]any, drain bool) *toolkit.ToolResult {
	s, result := t.session(ctx, args)
	if result != nil {
		return result
	}
	return execResult(sessionInfo(s, drain))
}

func (t *ExecTool) write(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	s, result := t.session(ctx, args)
	if result != nil {
		return result
	}
	data, ok := args["data"].(string)
	if !ok {
		return toolkit.ErrorResult("exec: data is required")
	}
	if s.pty != nil {
		for _, line := range strings.Split(data, "\n") {
			if denied, pattern := deniedCommand(line); denied {
				return toolkit.ErrorResult(fmt.Sprintf("exec: input rejected by safety policy (%s)", pattern))
			}
		}
	}
	if err := s.write(ctx, data, t.writeTimeout); err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	return execResult(map[string]any{"session_id": s.id, "status": s.statusValue()})
}

func (t *ExecTool) sendKeys(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	s, result := t.session(ctx, args)
	if result != nil {
		return result
	}
	keys, _ := args["keys"].(string)
	if strings.TrimSpace(keys) == "" {
		return toolkit.ErrorResult("exec: keys is required")
	}
	if s.pty == nil {
		return toolkit.ErrorResult("exec: send-keys requires a PTY session")
	}
	sequence, err := encodeKeys(strings.Fields(keys))
	if err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	return t.write(ctx, map[string]any{"session_id": s.id, "data": sequence})
}

func (t *ExecTool) kill(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	s, result := t.session(ctx, args)
	if result != nil {
		return result
	}
	if err := s.kill(); err != nil {
		return toolkit.ErrorResult("exec: " + err.Error())
	}
	return execResult(map[string]any{"session_id": s.id, "status": "killed"})
}

// session resolves a session by id, enforcing exact owner scoping.
func (t *ExecTool) session(ctx context.Context, args map[string]any) (*processSession, *toolkit.ToolResult) {
	id, _ := args["session_id"].(string)
	if id == "" {
		return nil, toolkit.ErrorResult("exec: session_id is required")
	}
	ownerSession, _ := toolkit.SessionIDFromContext(ctx)
	s, ok := t.sessions.getFor(ownerSession, id)
	if !ok {
		return nil, toolkit.ErrorResult("exec: session not found: " + id)
	}
	return s, nil
}

func sessionInfo(s *processSession, drain bool) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := s.output.String()
	if drain {
		s.output.reset()
	}
	return map[string]any{"session_id": s.id, "command": s.command, "pid": s.pid, "status": s.status, "exit_code": s.exitCode, "output": output}
}

func execResult(value any) *toolkit.ToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return toolkit.ErrorResult("exec: marshal result: " + err.Error())
	}
	return toolkit.SilentResult(string(data))
}
