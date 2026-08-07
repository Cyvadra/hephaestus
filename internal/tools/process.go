package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// maxProcessOutput is the cap on retained output per session. Sessions keep
// the most recent output (a ring buffer), so long-running or interactive
// PTY sessions remain readable even after producing more than this amount.
const maxProcessOutput = 1 * 1024 * 1024

// maxSessions caps how many background sessions one server instance tracks
// at once. Finished sessions are evicted first, so the cap only bites when
// too many are still running.
const maxSessions = 64

// ringBuffer retains at most max bytes of output, keeping the tail so the
// most recent output of a long-running session is always visible.
type ringBuffer struct {
	buf  []byte
	pos  int
	full bool
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, max)}
}

func (r *ringBuffer) write(p []byte) {
	for _, b := range p {
		r.buf[r.pos] = b
		r.pos++
		if r.pos == len(r.buf) {
			r.pos = 0
			r.full = true
		}
	}
}

func (r *ringBuffer) reset() {
	r.pos = 0
	r.full = false
}

func (r *ringBuffer) String() string {
	if !r.full {
		return string(r.buf[:r.pos])
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.pos:]...)
	out = append(out, r.buf[:r.pos]...)
	return string(out)
}

// processSession is one running or finished background process.
type processSession struct {
	id           string
	command      string
	ownerSession uint
	startedAt    time.Time

	mu       sync.Mutex // guards all fields below plus output
	pid      int
	status   string
	exitCode int
	stdin    io.WriteCloser
	pty      *os.File
	proc     *os.Process
	output   *ringBuffer
}

func newSession(command string, ownerSession uint, pid int, proc *os.Process) *processSession {
	return &processSession{
		id:           uuid.NewString(),
		command:      command,
		ownerSession: ownerSession,
		pid:          pid,
		proc:         proc,
		status:       "running",
		startedAt:    time.Now(),
		output:       newRingBuffer(maxProcessOutput),
	}
}

func (s *processSession) appendOutput(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output.write(data)
}

func (s *processSession) statusValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *processSession) terminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status != "running"
}

// write sends data to the process's input. It never holds the session lock
// while blocked on a child that has stopped draining its input, so a wedged
// child cannot wedge read/kill on the same session. The write itself is
// bounded by timeout (or ctx cancellation for /stop).
func (s *processSession) write(ctx context.Context, data string, timeout time.Duration) error {
	s.mu.Lock()
	if s.status != "running" {
		s.mu.Unlock()
		return fmt.Errorf("process is no longer running")
	}
	var writer io.Writer
	if s.pty != nil {
		writer = s.pty
	} else if s.stdin != nil {
		writer = s.stdin
	}
	s.mu.Unlock()
	if writer == nil {
		return fmt.Errorf("process has no stdin")
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, data)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("write timed out after %s (process is not consuming input; try kill)", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// kill terminates the process and its whole process group, so descendant
// processes cannot keep the PTY or pipes open after a kill.
func (s *processSession) kill() error {
	s.mu.Lock()
	proc := s.proc
	running := s.status == "running"
	s.mu.Unlock()
	if !running || proc == nil {
		return fmt.Errorf("process is no longer running")
	}
	if err := killProcessGroup(proc); err != nil {
		return err
	}
	s.mu.Lock()
	s.status = "killed"
	s.mu.Unlock()
	return nil
}

// pump copies r into the session's retained output until EOF or error.
func (s *processSession) pump(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.appendOutput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// processManager owns every background process a server instance started,
// with a bounded session count and explicit shutdown so nothing outlives
// its server.
type processManager struct {
	mu       sync.RWMutex
	sessions map[string]*processSession
	max      int
}

func newProcessManager(max int) *processManager {
	if max <= 0 {
		max = maxSessions
	}
	return &processManager{sessions: make(map[string]*processSession), max: max}
}

func (m *processManager) add(s *processSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= m.max {
		if err := m.evictFinishedLocked(); err != nil {
			return err
		}
	}
	if _, exists := m.sessions[s.id]; exists {
		return fmt.Errorf("exec: duplicate process session id")
	}
	m.sessions[s.id] = s
	return nil
}

// evictFinishedLocked drops the oldest finished session to make room.
// Caller holds m.mu; lock order is always manager then session.
func (m *processManager) evictFinishedLocked() error {
	var oldest *processSession
	for _, s := range m.sessions {
		if s.terminal() {
			if oldest == nil || s.startedAt.Before(oldest.startedAt) {
				oldest = s
			}
		}
	}
	if oldest == nil {
		return fmt.Errorf("exec: too many running background sessions (limit %d); kill one first", m.max)
	}
	delete(m.sessions, oldest.id)
	return nil
}

// getFor returns a session only if it is owned by owner. Ownership is
// matched exactly: a session started from an unauthenticated context (owner
// 0) is only reachable from owner 0, never from other sessions and never
// across sessions.
func (m *processManager) getFor(owner uint, id string) (*processSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if s.ownerSession != owner {
		return nil, false
	}
	return s, true
}

func (m *processManager) listFor(owner uint) []*processSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*processSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.ownerSession == owner {
			out = append(out, s)
		}
	}
	return out
}

func (m *processManager) listAll() []*processSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*processSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// shutdown kills every live session; call on server stop so no background
// process outlives its server.
func (m *processManager) shutdown() {
	for _, s := range m.listAll() {
		_ = s.kill()
	}
}

// setProcessGroup makes the child a process-group leader on Unix so a kill
// can reach its descendants too. creack/pty already sets Setsid for PTY
// sessions, which has the same effect.
func setProcessGroup(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

// killProcessGroup terminates a process and its process group.
func killProcessGroup(p *os.Process) error {
	if p == nil {
		return os.ErrProcessDone
	}
	if runtime.GOOS == "windows" {
		return p.Kill()
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
