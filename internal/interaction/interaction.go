// Package interaction coordinates runtime requests that require a user's
// response, such as permission prompts from a tool invocation.
package interaction

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const EventAskPermission = "ask_permission"

var (
	ErrDenied    = errors.New("interaction: denied by user")
	ErrNoPending = errors.New("interaction: no pending request")
)

// Request is the client-visible description of an interaction required by
// the running agent. More kinds can be added without changing the stream
// transport or command protocol.
type Request struct {
	ID        uint64    `json:"id"`
	SessionID uint      `json:"session_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Details   string    `json:"details"`
	CreatedAt time.Time `json:"created_at"`
}

// Event is reported by a running agent to its stream consumer.
type Event struct {
	Type    string  `json:"type"`
	Request Request `json:"request"`
}

type reporterKey struct{}

// WithReporter attaches a stream reporter to ctx. Runtime components use it
// without importing the chat or HTTP packages.
func WithReporter(ctx context.Context, report func(Event)) context.Context {
	return context.WithValue(ctx, reporterKey{}, report)
}

// HasReporter reports whether ctx can deliver a visible interaction request
// to a client. Tools use this to avoid waiting invisibly in non-streaming
// request paths.
func HasReporter(ctx context.Context) bool {
	_, ok := ctx.Value(reporterKey{}).(func(Event))
	return ok
}

func report(ctx context.Context, event Event) {
	if reporter, ok := ctx.Value(reporterKey{}).(func(Event)); ok && reporter != nil {
		reporter(event)
	}
}

type pending struct {
	request  Request
	decision chan bool
}

// Manager owns at most one visible interaction per session. Concurrent tool
// calls queue behind the visible request, which keeps `/interact approve` and
// `/interact deny` unambiguous without requiring a request id in the command.
type Manager struct {
	mu          sync.Mutex
	nextID      uint64
	pending     map[uint]*pending
	changed     map[uint]chan struct{}
	autoApprove map[uint]bool
}

func NewManager() *Manager {
	return &Manager{
		pending:     map[uint]*pending{},
		changed:     map[uint]chan struct{}{},
		autoApprove: map[uint]bool{},
	}
}

// SetAutoApprove changes whether permission requests in sessionID are
// automatically approved. The setting applies only to this runtime.
func (m *Manager) SetAutoApprove(sessionID uint, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if enabled {
		m.autoApprove[sessionID] = true
		return
	}
	delete(m.autoApprove, sessionID)
}

// EnableAutoApprove enables automatic approval and approves the request, if
// any, currently awaiting a response in sessionID.
func (m *Manager) EnableAutoApprove(sessionID uint) error {
	m.mu.Lock()
	m.autoApprove[sessionID] = true
	p := m.pending[sessionID]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	select {
	case p.decision <- true:
		return nil
	default:
		return fmt.Errorf("interaction: request %d has already been answered", p.request.ID)
	}
}

// AutoApprove reports whether permission requests for sessionID are
// automatically approved.
func (m *Manager) AutoApprove(sessionID uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.autoApprove[sessionID]
}

// RequestPermission emits an ask_permission event and blocks until the user
// approves, denies, or the enclosing turn context is canceled.
func (m *Manager) RequestPermission(ctx context.Context, sessionID uint, title, details string) error {
	for {
		m.mu.Lock()
		if m.autoApprove[sessionID] {
			m.mu.Unlock()
			return nil
		}
		if _, occupied := m.pending[sessionID]; !occupied {
			if m.changed[sessionID] == nil {
				m.changed[sessionID] = make(chan struct{})
			}
			m.nextID++
			p := &pending{request: Request{
				ID: m.nextID, SessionID: sessionID, Kind: "permission",
				Title: title, Details: details, CreatedAt: time.Now(),
			}, decision: make(chan bool, 1)}
			m.pending[sessionID] = p
			m.mu.Unlock()

			report(ctx, Event{Type: EventAskPermission, Request: p.request})
			select {
			case approved := <-p.decision:
				m.finish(sessionID, p)
				if !approved {
					return ErrDenied
				}
				return nil
			case <-ctx.Done():
				// Respond and cancellation can race; prefer a decision that
				// already landed in the buffered channel over reporting the
				// approval lost to cancellation.
				select {
				case approved := <-p.decision:
					m.finish(sessionID, p)
					if !approved {
						return ErrDenied
					}
					return nil
				default:
				}
				m.finish(sessionID, p)
				return ctx.Err()
			}
		}
		changed := m.changed[sessionID]
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Respond applies the user's decision to the currently visible request.
func (m *Manager) Respond(sessionID uint, approved bool) error {
	m.mu.Lock()
	p, ok := m.pending[sessionID]
	m.mu.Unlock()
	if !ok {
		return ErrNoPending
	}
	select {
	case p.decision <- approved:
		return nil
	default:
		return fmt.Errorf("interaction: request %d has already been answered", p.request.ID)
	}
}

func (m *Manager) finish(sessionID uint, p *pending) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending[sessionID] != p {
		return
	}
	delete(m.pending, sessionID)
	if changed := m.changed[sessionID]; changed != nil {
		close(changed)
	}
	m.changed[sessionID] = make(chan struct{})
}
