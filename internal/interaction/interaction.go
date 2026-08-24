// Package interaction coordinates runtime requests that require a user's
// response, such as permission prompts from a tool invocation.
package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	EventAskPermission = "ask_permission"
	EventAskQuestions  = "ask_questions"
)

var (
	ErrDenied          = errors.New("interaction: denied by user")
	ErrNoPending       = errors.New("interaction: no pending request")
	ErrInvalidResponse = errors.New("interaction: invalid response")
	ErrRequestMismatch = errors.New("interaction: request does not match pending interaction")
)

// Question is one choice the user must answer. IDs are supplied by the
// caller so a tool result can unambiguously refer to each question and option.
type Question struct {
	ID          string   `json:"id"`
	Prompt      string   `json:"prompt"`
	MultiSelect bool     `json:"multi_select"`
	Options     []Option `json:"options"`
}

type Option struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Answer struct {
	QuestionID        string   `json:"question_id"`
	SelectedOptionIDs []string `json:"selected_option_ids"`
	CustomText        string   `json:"custom_text,omitempty"`
}

// Request is the client-visible description of an interaction required by
// the running agent. More kinds can be added without changing the stream
// transport or command protocol.
type Request struct {
	ID        uint64     `json:"id"`
	SessionID uint       `json:"session_id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Details   string     `json:"details"`
	Questions []Question `json:"questions,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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
	response chan response
}

type response struct {
	approved *bool
	answers  []Answer
}

// Manager owns at most one visible interaction per session. Concurrent tool
// calls queue behind the visible request, which keeps `/interact approve` and
// `/interact deny` unambiguous without requiring a request id in the command.
type Manager struct {
	mu                      sync.Mutex
	nextID                  uint64
	pending                 map[uint]*pending
	changed                 map[uint]chan struct{}
	autoApprove             map[uint]bool
	parent                  map[uint]uint
	subagentApprovalTimeout time.Duration
}

func NewManager() *Manager {
	return &Manager{
		pending:                 map[uint]*pending{},
		changed:                 map[uint]chan struct{}{},
		autoApprove:             map[uint]bool{},
		parent:                  map[uint]uint{},
		subagentApprovalTimeout: 30 * time.Second,
	}
}

// RegisterSubagent makes childSessionID use the same live authorization
// policy as parentSessionID. A delegated session has no dependable human
// response channel, so an unanswered prompt is approved after a short grace
// period. Availability comes first: a subagent must not remain stuck merely
// because its parent UI is not currently observing the child's event stream.
func (m *Manager) RegisterSubagent(childSessionID, parentSessionID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent[childSessionID] = parentSessionID
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
	if p.request.Kind != "permission" {
		return nil
	}
	approved := true
	select {
	case p.response <- response{approved: &approved}:
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
	return m.autoApprovedLocked(sessionID)
}

func (m *Manager) autoApprovedLocked(sessionID uint) bool {
	for sessionID != 0 {
		if m.autoApprove[sessionID] {
			return true
		}
		sessionID = m.parent[sessionID]
	}
	return false
}

// RequestPermission emits an ask_permission event and blocks until the user
// approves, denies, or the enclosing turn context is canceled.
func (m *Manager) RequestPermission(ctx context.Context, sessionID uint, title, details string) error {
	for {
		m.mu.Lock()
		if m.autoApprovedLocked(sessionID) {
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
			}, response: make(chan response, 1)}
			m.pending[sessionID] = p
			timeout := time.Duration(0)
			if m.parent[sessionID] != 0 {
				timeout = m.subagentApprovalTimeout
			}
			m.mu.Unlock()

			report(ctx, Event{Type: EventAskPermission, Request: p.request})
			var timeoutC <-chan time.Time
			var timer *time.Timer
			if timeout > 0 {
				timer = time.NewTimer(timeout)
				timeoutC = timer.C
				defer timer.Stop()
			}
			select {
			case response := <-p.response:
				m.finish(sessionID, p)
				if response.approved == nil || !*response.approved {
					return ErrDenied
				}
				return nil
			case <-ctx.Done():
				// Respond and cancellation can race; prefer a decision that
				// already landed in the buffered channel over reporting the
				// approval lost to cancellation.
				select {
				case response := <-p.response:
					m.finish(sessionID, p)
					if response.approved == nil || !*response.approved {
						return ErrDenied
					}
					return nil
				default:
				}
				m.finish(sessionID, p)
				return ctx.Err()
			case <-timeoutC:
				// Availability first: delegated work auto-approves when nobody
				// answers, while an explicit decision delivered at the boundary wins.
				select {
				case response := <-p.response:
					m.finish(sessionID, p)
					if response.approved == nil || !*response.approved {
						return ErrDenied
					}
					return nil
				default:
				}
				m.finish(sessionID, p)
				return nil
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
	if p.request.Kind != "permission" {
		return ErrInvalidResponse
	}
	select {
	case p.response <- response{approved: &approved}:
		return nil
	default:
		return fmt.Errorf("interaction: request %d has already been answered", p.request.ID)
	}
}

// RequestQuestions reports a structured question form and blocks until the
// user answers it or the enclosing turn is canceled. Questions do not time
// out because choosing on behalf of the user would defeat their purpose.
func (m *Manager) RequestQuestions(ctx context.Context, sessionID uint, questions []Question) ([]Answer, error) {
	if err := validateQuestions(questions); err != nil {
		return nil, err
	}
	for {
		m.mu.Lock()
		if _, occupied := m.pending[sessionID]; !occupied {
			if m.changed[sessionID] == nil {
				m.changed[sessionID] = make(chan struct{})
			}
			m.nextID++
			p := &pending{request: Request{
				ID: m.nextID, SessionID: sessionID, Kind: "questions", Questions: questions, CreatedAt: time.Now(),
			}, response: make(chan response, 1)}
			m.pending[sessionID] = p
			m.mu.Unlock()

			report(ctx, Event{Type: EventAskQuestions, Request: p.request})
			select {
			case reply := <-p.response:
				m.finish(sessionID, p)
				return normalizeAnswers(reply.answers), nil
			case <-ctx.Done():
				select {
				case reply := <-p.response:
					m.finish(sessionID, p)
					return normalizeAnswers(reply.answers), nil
				default:
				}
				m.finish(sessionID, p)
				return nil, ctx.Err()
			}
		}
		changed := m.changed[sessionID]
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// RespondQuestions applies answers only to the matching visible question
// request. The request id prevents a stale browser form from answering a
// later prompt in the same session.
func (m *Manager) RespondQuestions(sessionID uint, requestID uint64, answers []Answer) error {
	m.mu.Lock()
	p, ok := m.pending[sessionID]
	m.mu.Unlock()
	if !ok {
		return ErrNoPending
	}
	if p.request.Kind != "questions" || p.request.ID != requestID {
		return ErrRequestMismatch
	}
	if err := validateAnswers(p.request.Questions, answers); err != nil {
		return err
	}
	select {
	case p.response <- response{answers: answers}:
		return nil
	default:
		return fmt.Errorf("interaction: request %d has already been answered", p.request.ID)
	}
}

func validateQuestions(questions []Question) error {
	if len(questions) < 1 || len(questions) > 5 {
		return fmt.Errorf("%w: require 1 to 5 questions", ErrInvalidResponse)
	}
	seenQuestions := map[string]bool{}
	for _, question := range questions {
		if question.ID == "" || question.Prompt == "" || seenQuestions[question.ID] || len(question.Options) < 2 || len(question.Options) > 5 {
			return fmt.Errorf("%w: malformed question", ErrInvalidResponse)
		}
		seenQuestions[question.ID] = true
		seenOptions := map[string]bool{}
		for _, option := range question.Options {
			if option.ID == "" || option.Title == "" || option.Description == "" || seenOptions[option.ID] {
				return fmt.Errorf("%w: malformed option", ErrInvalidResponse)
			}
			seenOptions[option.ID] = true
		}
	}
	return nil
}

func validateAnswers(questions []Question, answers []Answer) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("%w: every question requires an answer", ErrInvalidResponse)
	}
	byID := make(map[string]Question, len(questions))
	for _, question := range questions {
		byID[question.ID] = question
	}
	seenAnswers := map[string]bool{}
	for _, answer := range answers {
		question, ok := byID[answer.QuestionID]
		if !ok || seenAnswers[answer.QuestionID] || (len(answer.SelectedOptionIDs) == 0 && strings.TrimSpace(answer.CustomText) == "") || (!question.MultiSelect && len(answer.SelectedOptionIDs) > 1) {
			return fmt.Errorf("%w: malformed answer", ErrInvalidResponse)
		}
		seenAnswers[answer.QuestionID] = true
		options := map[string]bool{}
		for _, option := range question.Options {
			options[option.ID] = true
		}
		selected := map[string]bool{}
		for _, optionID := range answer.SelectedOptionIDs {
			if !options[optionID] || selected[optionID] {
				return fmt.Errorf("%w: invalid selected option", ErrInvalidResponse)
			}
			selected[optionID] = true
		}
	}
	return nil
}

func normalizeAnswers(answers []Answer) []Answer {
	result := append([]Answer(nil), answers...)
	for index := range result {
		result[index].CustomText = strings.TrimSpace(result[index].CustomText)
	}
	return result
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
