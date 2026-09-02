// Package steering manages pending human instructions for active chat runs.
package steering

import (
	"fmt"
	"sync"
)

// Mode controls whether the next selected tool call executes.
type Mode string

const (
	ModeNormal     Mode = "normal"
	ModeAggressive Mode = "aggressive"
)

// Pending is a single human instruction attached to one chat run.
type Pending struct {
	RunID     uint
	SessionID uint
	Text      string
	Mode      Mode
	version   uint64
}

// Lease identifies an instruction claimed by the agent. It must be either
// acknowledged after persistence or released if the turn does not commit.
type Lease struct {
	Pending
	Token uint64
}

// Outcome reports whether Put created or replaced the run's pending message.
type Outcome string

const (
	OutcomeQueued   Outcome = "queued"
	OutcomeReplaced Outcome = "replaced"
)

// Manager keeps one replaceable pending instruction for each active chat run.
// It is intentionally process-local; durable chat-run ownership prevents a
// pending message from being attached to a later run.
type Manager struct {
	mu      sync.Mutex
	next    uint64
	pending map[uint]Pending
	claimed map[uint]map[uint64]Lease
}

func NewManager() *Manager {
	return &Manager{pending: map[uint]Pending{}, claimed: map[uint]map[uint64]Lease{}}
}

// Put creates or replaces the next instruction to be claimed. An instruction
// that has already crossed the agent boundary remains leased for persistence,
// while a newer submission becomes the next pending instruction.
func (m *Manager) Put(runID, sessionID uint, text string, mode Mode) (Outcome, error) {
	if runID == 0 || sessionID == 0 {
		return "", fmt.Errorf("steering: run and session are required")
	}
	if text == "" {
		return "", fmt.Errorf("steering: text is required")
	}
	if mode != ModeNormal && mode != ModeAggressive {
		return "", fmt.Errorf("steering: invalid mode %q", mode)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	pending := Pending{RunID: runID, SessionID: sessionID, Text: text, Mode: mode, version: m.next}
	_, replaced := m.pending[runID]
	m.pending[runID] = pending
	if replaced {
		return OutcomeReplaced, nil
	}
	return OutcomeQueued, nil
}

// Get returns the currently replaceable instruction for runID.
func (m *Manager) Get(runID uint) (Pending, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, ok := m.pending[runID]
	return pending, ok
}

// Cancel removes a pending instruction. Claimed instructions are immutable.
func (m *Manager) Cancel(runID uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pending[runID]; !ok {
		return false
	}
	delete(m.pending, runID)
	return true
}

// Claim atomically transfers the pending instruction to an agent lease.
func (m *Manager) Claim(runID uint) (Lease, bool) {
	return m.claim(runID, false)
}

// ClaimNormal transfers a pending Normal instruction to a lease without
// consuming an Aggressive instruction that must be considered before a tool
// starts. It lets Normal steering submitted during execution join that tool's
// result when the execution completes.
func (m *Manager) ClaimNormal(runID uint) (Lease, bool) {
	return m.claim(runID, true)
}

func (m *Manager) claim(runID uint, normalOnly bool) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, ok := m.pending[runID]
	if !ok {
		return Lease{}, false
	}
	if normalOnly && pending.Mode != ModeNormal {
		return Lease{}, false
	}
	delete(m.pending, runID)
	lease := Lease{Pending: pending, Token: pending.version}
	if m.claimed[runID] == nil {
		m.claimed[runID] = map[uint64]Lease{}
	}
	m.claimed[runID][lease.Token] = lease
	return lease, true
}

// Acknowledge permanently removes a successfully persisted lease.
func (m *Manager) Acknowledge(lease Lease) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.claimed[lease.RunID][lease.Token]
	if !ok {
		return false
	}
	delete(m.claimed[lease.RunID], lease.Token)
	if len(m.claimed[lease.RunID]) == 0 {
		delete(m.claimed, lease.RunID)
	}
	return true
}

// Release makes an unpersisted lease available again unless newer pending
// steering has already been submitted for the same run.
func (m *Manager) Release(lease Lease) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	claimed, ok := m.claimed[lease.RunID][lease.Token]
	if !ok {
		return false
	}
	delete(m.claimed[lease.RunID], lease.Token)
	if len(m.claimed[lease.RunID]) == 0 {
		delete(m.claimed, lease.RunID)
	}
	if _, exists := m.pending[lease.RunID]; exists {
		return true
	}
	m.pending[lease.RunID] = claimed.Pending
	return true
}

// Finish returns an unresolved instruction for fallback after the source run
// ends. It also removes any state for that run.
func (m *Manager) Finish(runID uint) (Pending, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pending, ok := m.pending[runID]; ok {
		delete(m.pending, runID)
		return pending, true
	}
	leases, ok := m.claimed[runID]
	if !ok {
		return Pending{}, false
	}
	delete(m.claimed, runID)
	var latest Lease
	for _, lease := range leases {
		if lease.Token > latest.Token {
			latest = lease
		}
	}
	return latest.Pending, true
}
