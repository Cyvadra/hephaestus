// Package agent implements the reusable LLM + tool-loop runtime shared by
// chat turns and workflow steps: the first model call, the tool-calling
// loop, plugin hooks, consecutive-call protection, and tool auditing.
// Execution-scope policy (session vs workflow) is applied here so headless
// runs never surface interactive approval or session-only plugins/tools.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/gorm"
)

// maxConsecutiveToolCalls bounds repeated calls to one tool without an
// intervening call to another tool.
const (
	maxConsecutiveToolCalls = 12
)

// LLM is the provider-facing interface the runner needs. *llm.Client
// satisfies it; tests may substitute a deterministic fake.
type LLM interface {
	Call(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool) (*ds4.ChatResponse, error)
	CallStream(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool, onDelta func(llm.StreamDelta)) (*ds4.ChatResponse, error)
}

// AuditOwner keys the ToolAudit rows created for audited tools. Exactly one
// of these is non-nil depending on the execution scope.
type AuditOwner struct {
	SessionID         *uint
	WorkflowRunID     *uint
	WorkflowStepRunID *uint
}

// Request describes one agent turn: the resolved identity, the expanded
// toolset, the plugin names, and the context messages ending in the pending
// user message.
type Request struct {
	Identity registry.Identity
	Toolset  []toolkit.Tool
	Plugins  []string
	// Turn carries the context message list and plugin state the runner
	// continues from and returns.
	Turn plugin.TurnContext
	// Scope selects the execution policy (session or workflow). Session-only
	// plugins and tools are filtered out for workflow scope.
	Scope toolkit.Scope
	// Audit records where generated ToolAudit rows belong.
	Audit AuditOwner
	// OwnerID keys interactive approval; sessions use their session id,
	// headless workflow runs pass 0.
	OwnerID uint
	// OnDelta streams assistant progress; nil disables streaming.
	OnDelta func(StreamEvent)
	// OnInteraction forwards interactive requests to a visible client;
	// nil disables interactive approval (headless workflow runs).
	OnInteraction func(interaction.Event)
	// ClaimNotifications atomically claims durable completion notifications
	// before an outbound model request. Claimed notifications are at-most-once.
	ClaimNotifications func() ([]Notification, error)
	// ClaimSteering claims one human instruction for the current chat run. It
	// is called only at a tool-call boundary and is ignored for workflow runs.
	ClaimSteering func() (*Steering, error)
	// ClaimNormalSteering claims a Normal instruction after a tool completes.
	// Aggressive instructions remain pending for the next pre-execution gate.
	ClaimNormalSteering func() (*Steering, error)
	// ClaimAggressiveSteering claims an Aggressive instruction while tools
	// run. A claimed instruction interrupts every running tool: after
	// steerGracePeriod the tool processes get SIGTERM, then SIGKILL, and a
	// tool still running after that is abandoned (see LateToolRecorder).
	ClaimAggressiveSteering func() (*Steering, error)
	// ReleaseSteering returns a claimed instruction that never reached the
	// transcript, e.g. one claimed by a tool call abandoned mid-claim.
	ReleaseSteering func(Steering)
}

type Notification struct {
	ID   uint
	Text string
}

// Steering is a human instruction injected into one tool result.
type Steering struct {
	Token      uint64
	Text       string
	Aggressive bool
}

// Result is the outcome of an agent turn before any persistence.
type Result struct {
	// Messages are the assistant/tool messages generated in persistence
	// order (assistant tool-call message, tool results, final assistant
	// message).
	Messages []store.ChatMessage
	// Deliveries are explicit file attachments collected from successful tool
	// calls in model-call order. The chat pipeline binds them to the final
	// assistant message when it persists the turn.
	Deliveries []toolkit.FileDelivery
	// Turn is the final plugin.TurnContext carrying metadata and any
	// completion-hook content mutation.
	Turn plugin.TurnContext
	// NotificationIDs are completion events included in this turn. The caller
	// acknowledges them only after the generated transcript is durable.
	NotificationIDs []uint
	// Steering contains human instructions injected into tool results. The
	// caller acknowledges them only after the generated transcript is durable.
	Steering []Steering
}

// Runner executes one agent turn: the first LLM call and, while the model
// requests tool calls, the tool-execution loop, wrapping each stage in the
// corresponding Plugin hooks and emitting stream events for visible deltas.
type Runner struct {
	llm          LLM
	plugins      *plugin.Registry
	interactions *interaction.Manager
	db           *gorm.DB
	notify       *notify.Notifier
	lateTools    LateToolRecorder
}

// NewRunner wires the agent runtime to its dependencies. interactions may be
// nil when interactive approval is never available.
func NewRunner(llm LLM, plugins *plugin.Registry, interactions *interaction.Manager, db *gorm.DB, notify *notify.Notifier) *Runner {
	return &Runner{llm: llm, plugins: plugins, interactions: interactions, db: db, notify: notify}
}

// Run executes one turn and returns the generated messages in persistence
// order plus the final turn context. On error, the returned messages contain
// whatever was generated before the failure.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	turn := req.Turn
	turn.Scope = req.Scope
	messages := append([]store.ChatMessage(nil), turn.Messages...)
	var toPersist []store.ChatMessage
	var deliveries []toolkit.FileDelivery
	var notificationIDs []uint
	var steering []Steering
	// injected guards against a long turn re-claiming the same completion
	// event at a later model boundary (the lease can expire mid-turn).
	injected := map[uint]struct{}{}
	toolset := toolkit.FilterScope(req.Toolset, req.Scope)
	allowedTools := make(map[string]toolkit.Tool, len(toolset))
	for _, tool := range toolset {
		allowedTools[tool.Name()] = tool
	}

	callIndex := -1
	callLLM := func() (*ds4.ChatResponse, error) {
		callIndex++
		if req.ClaimNotifications != nil {
			pending, err := req.ClaimNotifications()
			if err != nil {
				return nil, err
			}
			for _, notification := range pending {
				if _, ok := injected[notification.ID]; ok {
					continue
				}
				injected[notification.ID] = struct{}{}
				notificationIDs = append(notificationIDs, notification.ID)
				notificationMessage := store.ChatMessage{Role: ds4.RoleUser, Content: notification.Text, Timestamp: time.Now()}
				messages = append(messages, notificationMessage)
				// Persist the completion as a durable user message so the
				// transcript reflects when the parent learned of the result;
				// the ack happens only after this message is stored.
				toPersist = append(toPersist, notificationMessage)
				turn.Messages = messages
			}
		}
		if req.OnDelta == nil {
			return r.llm.Call(ctx, req.Identity, messages, toolset)
		}
		return r.llm.CallStream(ctx, req.Identity, messages, toolset, func(d llm.StreamDelta) {
			if d.Content != "" {
				req.OnDelta(StreamEvent{Type: "delta", Text: d.Content})
			}
			if d.ReasoningContent != "" {
				req.OnDelta(StreamEvent{Type: "reasoning", Text: d.ReasoningContent})
			}
			for _, toolCall := range d.ToolCalls {
				req.OnDelta(StreamEvent{Type: "tool_call", ToolCall: &StreamToolCall{
					CallIndex: callIndex,
					Index:     toolCall.Index,
					ID:        toolCall.ID,
					Name:      toolCall.Name,
					Arguments: toolCall.Arguments,
					Status:    "calling",
				}})
			}
		})
	}

	turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantFirstCallLLM, plugin.PhaseBefore, turn)
	messages = turn.Messages
	resp, err := callLLM()
	if err != nil {
		return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, err
	}
	turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantFirstCallLLM, plugin.PhaseAfter, turn)
	messages = turn.Messages

	lastToolName := ""
	consecutiveToolCalls := 0
	for resp.FinishReason() == ds4.FinishReasonToolCalls {
		assistantMsg, err := StoreMessageFromDS4(*resp.FirstMessage())
		if err != nil {
			return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, err
		}
		messages = append(messages, assistantMsg)
		toPersist = append(toPersist, assistantMsg)
		turn.Messages = messages

		toolCalls := resp.ToolCalls()
		for _, tc := range toolCalls {
			if err := r.trackConsecutiveToolCall(ctx, req, &lastToolName, &consecutiveToolCalls, tc.Function.Name); err != nil {
				return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, err
			}
			turn.ToolCall = &tc
			turn.ToolResult = nil
			turn = r.plugins.Run(ctx, req.Plugins, plugin.HookToolCall, plugin.PhaseBefore, turn)
			turn.ToolCall = nil
		}

		var batchDeliveries []toolkit.FileDelivery
		var batchSteering []Steering
		messages, toPersist, turn, batchDeliveries, batchSteering = r.runToolCalls(ctx, req, callIndex, allowedTools, toolCalls, messages, toPersist, turn)
		deliveries = appendUniqueDeliveries(deliveries, batchDeliveries)
		steering = append(steering, batchSteering...)
		if err := ctx.Err(); err != nil {
			return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, err
		}

		turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantContinuousCallLLM, plugin.PhaseBefore, turn)
		messages = turn.Messages
		resp, err = callLLM()
		if err != nil {
			return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, err
		}
		turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantContinuousCallLLM, plugin.PhaseAfter, turn)
		messages = turn.Messages
	}

	final, err := StoreMessageFromDS4(*resp.FirstMessage())
	if err != nil {
		return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, err
	}
	toPersist = append(toPersist, final)

	turn.Messages = append(messages, final)
	turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantMessageCompletion, plugin.PhaseAfter, turn)

	// A plugin may have rewritten the final assistant message's content
	// (e.g. to refresh a storyline-state suffix); splice that back into
	// what actually gets persisted.
	if n := len(turn.Messages); n > 0 {
		toPersist[len(toPersist)-1].Content = turn.Messages[n-1].Content
	}

	return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs, Steering: steering}, nil
}

// toolExecution is one finished tool call of a batch.
type toolExecution struct {
	index    int
	result   *toolkit.ToolResult
	steering *Steering
	endedAt  time.Time
}

// toolCallState is the per-call execution state runToolCalls uses to
// interrupt a call on a force steer.
type toolCallState struct {
	interrupter *toolkit.Interrupter
	cancel      context.CancelFunc
	detached    atomic.Bool
	// signaled records the strongest signal actually delivered to the call's
	// process, so the steering note never claims a signal that was not sent.
	signaled  string
	startedAt time.Time
}

// runToolCalls executes a single model response's independent tool calls
// concurrently, runs the after-phase ToolCall hook for each with its
// outcome, and appends the resulting tool messages to messages/toPersist in
// the model's original order for deterministic persistence.
func (r *Runner) runToolCalls(ctx context.Context, req Request, callIndex int, allowedTools map[string]toolkit.Tool, toolCalls []ds4.ToolCall, messages, toPersist []store.ChatMessage, turn plugin.TurnContext) ([]store.ChatMessage, []store.ChatMessage, plugin.TurnContext, []toolkit.FileDelivery, []Steering) {
	results := make([]*toolkit.ToolResult, len(toolCalls))
	sessionScope := req.Scope == toolkit.ScopeSession
	claimed := make([]*Steering, len(toolCalls))
	completed := make(chan toolExecution, len(toolCalls))
	calls := make([]toolCallState, len(toolCalls))
	defer func() {
		for idx := range calls {
			if calls[idx].cancel != nil {
				calls[idx].cancel()
			}
		}
	}()
	// inFlight reports whether call idx was started and has no result yet.
	inFlight := func(idx int) bool { return results[idx] == nil && calls[idx].interrupter != nil }

	// One pre-execution steering gate per batch: an Aggressive instruction
	// skips every call in the batch, a Normal one joins the first result.
	var gate *Steering
	var gateErr error
	if sessionScope && req.ClaimSteering != nil {
		gate, gateErr = req.ClaimSteering()
	}
	running := 0
	for i, tc := range toolCalls {
		switch {
		case gateErr != nil && i == 0:
			// Fail only the first call, as the gate would have joined only
			// its result; the rest of the batch still runs.
			results[i] = toolkit.ErrorResult(fmt.Sprintf("agent: claim steering: %v", gateErr))
			continue
		case gate != nil && gate.Aggressive:
			if i == 0 {
				results[i] = toolkit.NewToolResult(formatSteering(gate))
				claimed[i] = gate
			} else {
				results[i] = toolkit.NewToolResult(skippedBySteering)
			}
			continue
		}
		var steering *Steering
		if i == 0 {
			steering = gate
		}
		toolCtx, cancel := context.WithCancel(ctx)
		calls[i].interrupter = &toolkit.Interrupter{}
		calls[i].cancel = cancel
		calls[i].startedAt = time.Now()
		toolCtx = toolkit.WithInterrupter(toolCtx, calls[i].interrupter)
		running++
		go func(idx int, tc ds4.ToolCall) {
			streamedBytes := 0
			result := r.executeTool(toolCtx, req, allowedTools, tc, turn.History, func(chunk string) {
				if req.OnDelta != nil && !calls[idx].detached.Load() {
					chunk = transform.LimitToolExchangeContent(tc.Function.Arguments, chunk)
					remaining := transform.MaxToolExchangeBytes - 1 - len(tc.Function.Arguments) - streamedBytes
					chunk = transform.LimitTextBytes(chunk, remaining)
					streamedBytes += len(chunk)
					if chunk == "" {
						return
					}
					req.OnDelta(StreamEvent{Type: "tool_output", ToolCall: &StreamToolCall{
						CallIndex: callIndex,
						Index:     idx,
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Result:    chunk,
						Status:    "calling",
					}})
				}
			})
			if steering == nil && sessionScope && req.ClaimNormalSteering != nil && !calls[idx].detached.Load() {
				claimed, err := req.ClaimNormalSteering()
				if err != nil {
					result = toolkit.ErrorResult(fmt.Sprintf("agent: claim normal steering: %v", err))
				} else {
					steering = claimed
				}
			}
			if steering != nil {
				result.ForLLM = appendSteering(result.ContentForLLM(), formatSteering(steering))
				result.ArtifactTags = nil
			}
			completed <- toolExecution{index: idx, result: result, steering: steering, endedAt: time.Now()}
		}(i, tc)
	}

	// While tools run, poll for a force (Aggressive) steer. Once claimed it
	// escalates against every still-running call: grace period, SIGTERM,
	// SIGKILL, then abandonment, so the turn never waits on a stuck process.
	var force *Steering
	var forceAt time.Time
	forceTarget := -1
	stage := steerStageNone
	var poll <-chan time.Time
	if running > 0 && sessionScope && req.ClaimAggressiveSteering != nil {
		ticker := time.NewTicker(steerPollInterval)
		defer ticker.Stop()
		poll = ticker.C
	}
	var stageC <-chan time.Time
	forceNote := func(idx int, execution string, extra string) string {
		text := ""
		if idx == forceTarget {
			text = force.Text
		} else {
			text = "(The user's steering instruction is attached to another tool result in this batch.)"
		}
		return "<user-steering mode=\"aggressive\" tool-execution=\"" + execution + "\"" + extra + ">\n" + text + "\n</user-steering>"
	}
	record := func(execution toolExecution) {
		results[execution.index] = execution.result
		claimed[execution.index] = execution.steering
		if force != nil && calls[execution.index].interrupter != nil {
			status := "completed"
			extra := ""
			if stage >= steerStageTerm {
				status = "interrupted"
				if signal := calls[execution.index].signaled; signal != "" {
					extra = fmt.Sprintf(" signal=%q", signal)
				}
			}
			execution.result.ForLLM = appendSteering(execution.result.ContentForLLM(), forceNote(execution.index, status, extra))
			execution.result.ArtifactTags = nil
		}
	}
	for pending := running; pending > 0; {
		select {
		case execution := <-completed:
			pending--
			record(execution)
		case <-poll:
			steering, err := req.ClaimAggressiveSteering()
			if err != nil || steering == nil {
				continue
			}
			force, forceAt, stage, poll = steering, time.Now(), steerStageGrace, nil
			for idx := range results {
				if inFlight(idx) {
					forceTarget = idx
					break
				}
			}
			stageC = time.After(steerGracePeriod)
		case <-stageC:
			switch stage {
			case steerStageGrace:
				stage, stageC = steerStageTerm, time.After(steerTermWait)
				for idx := range results {
					if !inFlight(idx) {
						continue
					}
					if calls[idx].interrupter.Signal(syscall.SIGTERM) {
						calls[idx].signaled = "SIGTERM"
					} else {
						calls[idx].cancel()
					}
				}
			case steerStageTerm:
				stage, stageC = steerStageKill, time.After(steerKillWait)
				for idx := range results {
					if !inFlight(idx) {
						continue
					}
					if calls[idx].interrupter.Signal(syscall.SIGKILL) {
						calls[idx].signaled = "SIGKILL"
					}
					calls[idx].cancel()
				}
			default:
				// Take completions that raced the abandon tick as real
				// results rather than mislabelling them abandoned.
			drain:
				for pending > 0 {
					select {
					case execution := <-completed:
						pending--
						record(execution)
					default:
						break drain
					}
				}
				abandoned := 0
				for idx := range results {
					if !inFlight(idx) {
						continue
					}
					calls[idx].detached.Store(true)
					abandoned++
					extra := ""
					if process, ok := calls[idx].interrupter.Process(); ok {
						extra = fmt.Sprintf(" pid=\"%d\"", process.PID)
					}
					results[idx] = toolkit.ErrorResult(abandonedByTool + "\n\n" + forceNote(idx, "abandoned", extra))
				}
				go r.collectAbandoned(req, toolCalls, calls, forceAt, abandoned, completed)
				pending = 0
			}
		case <-ctx.Done():
			for index := range results {
				if results[index] == nil {
					results[index] = toolkit.ErrorResult(ctx.Err().Error())
				}
			}
			pending = 0
		}
	}

	var deliveries []toolkit.FileDelivery
	var claimedSteering []Steering
	if force != nil {
		claimedSteering = append(claimedSteering, *force)
	}
	for toolIndex, tc := range toolCalls {
		result := results[toolIndex]
		if claimed[toolIndex] != nil {
			claimedSteering = append(claimedSteering, *claimed[toolIndex])
		}
		if !result.IsError {
			deliveries = appendUniqueDeliveries(deliveries, result.Deliveries)
		}
		turn.ToolCall = &tc
		turn.ToolResult = result
		turn = r.plugins.Run(ctx, req.Plugins, plugin.HookToolCall, plugin.PhaseAfter, turn)
		turn.ToolCall = nil
		turn.ToolResult = nil

		content := transform.LimitToolExchangeContent(tc.Function.Arguments, result.ContentForLLM())
		if req.OnDelta != nil {
			status := "complete"
			if result.IsError {
				status = "error"
			}
			req.OnDelta(StreamEvent{Type: "tool_result", ToolCall: &StreamToolCall{
				CallIndex: callIndex,
				Index:     toolIndex,
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Result:    content,
				Status:    status,
			}})
		}

		toolMsg := store.ChatMessage{Role: ds4.RoleTool, Content: content, ToolCallID: tc.ID, Timestamp: time.Now()}
		messages = append(messages, toolMsg)
		toPersist = append(toPersist, toolMsg)
		turn.Messages = messages
	}
	return messages, toPersist, turn, deliveries, claimedSteering
}

const (
	skippedBySteering = "Tool execution skipped: the user force-steered the run before this call started."
	abandonedByTool   = "Tool execution abandoned: the process did not exit after SIGTERM and SIGKILL following the user's force steer. It may still be running; a completion notice will follow if it finishes."
)

// Force-steer escalation timings; variables so tests can shorten them.
var (
	steerGracePeriod  = 3 * time.Second
	steerTermWait     = 10 * time.Second
	steerKillWait     = 10 * time.Second
	steerPollInterval = 250 * time.Millisecond
)

type steerStage int

const (
	steerStageNone steerStage = iota
	steerStageGrace
	steerStageTerm
	steerStageKill
)

// LateToolResult is a tool call abandoned by a force steer that finished
// after its turn moved on.
type LateToolResult struct {
	SessionID  uint      `json:"session_id"`
	ToolCallID string    `json:"tool_call_id"`
	Tool       string    `json:"tool"`
	Arguments  string    `json:"arguments"`
	PID        int       `json:"pid,omitempty"`
	Command    string    `json:"command,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	SteeredAt  time.Time `json:"steered_at"`
	EndedAt    time.Time `json:"ended_at"`
	IsError    bool      `json:"is_error"`
	Output     string    `json:"output"`
}

// LateToolRecorder durably records late tool completions so they reach the
// session as notifications, like subagent completions.
type LateToolRecorder interface {
	RecordLateToolResult(LateToolResult) error
}

// SetLateToolRecorder enables late-completion notices for abandoned tools.
func (r *Runner) SetLateToolRecorder(recorder LateToolRecorder) { r.lateTools = recorder }

// lateToolOutputBytes bounds the output tail kept in a late notice;
// lateToolFieldBytes bounds its arguments and command.
const (
	lateToolOutputBytes = 4 << 10
	lateToolFieldBytes  = 2 << 10
)

// lateToolWaitLimit bounds how long abandoned calls are watched for a late
// completion, so a call that never returns does not pin its turn forever.
var lateToolWaitLimit = time.Hour

// stripNUL removes NUL bytes, which Postgres rejects in text and jsonb.
func stripNUL(text string) string { return strings.ReplaceAll(text, "\x00", "") }

func truncateHead(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return transform.TrimUTF8Prefix(text, limit) + "…"
}

// collectAbandoned waits for the abandoned (detached) tool calls to finish and
// records each as a late completion. completed is buffered, so finished calls
// never block.
func (r *Runner) collectAbandoned(req Request, toolCalls []ds4.ToolCall, calls []toolCallState, steeredAt time.Time, abandoned int, completed <-chan toolExecution) {
	deadline := time.After(lateToolWaitLimit)
	unfinished := map[int]bool{}
	for index := range calls {
		if calls[index].detached.Load() {
			unfinished[index] = true
		}
	}
	for remaining := abandoned; remaining > 0; {
		var execution toolExecution
		select {
		case execution = <-completed:
		case <-deadline:
			log.Printf("agent: stopped waiting for %d abandoned tool call(s) after %s", remaining, lateToolWaitLimit)
			r.recordGaveUp(req, toolCalls, calls, steeredAt, unfinished)
			return
		}
		if !calls[execution.index].detached.Load() {
			continue
		}
		delete(unfinished, execution.index)
		remaining--
		// The call may have claimed a Normal instruction just before it was
		// detached; its result never reaches the transcript, so return it.
		if execution.steering != nil && req.ReleaseSteering != nil {
			req.ReleaseSteering(*execution.steering)
		}
		if r.lateTools == nil || req.Audit.SessionID == nil {
			continue
		}
		tc := toolCalls[execution.index]
		output := execution.result.ContentForLLM()
		if len(output) > lateToolOutputBytes {
			output = "…" + transform.TrimUTF8Suffix(output, lateToolOutputBytes)
		}
		late := LateToolResult{
			SessionID:  *req.Audit.SessionID,
			ToolCallID: tc.ID,
			Tool:       tc.Function.Name,
			Arguments:  stripNUL(truncateHead(tc.Function.Arguments, lateToolFieldBytes)),
			StartedAt:  calls[execution.index].startedAt,
			SteeredAt:  steeredAt,
			EndedAt:    execution.endedAt,
			IsError:    execution.result.IsError,
			Output:     stripNUL(output),
		}
		if process, ok := calls[execution.index].interrupter.Process(); ok {
			late.PID, late.Command, late.StartedAt = process.PID, stripNUL(truncateHead(process.Command, lateToolFieldBytes)), process.StartedAt
		}
		if err := r.lateTools.RecordLateToolResult(late); err != nil {
			log.Printf("agent: record late result of tool call %s: %v", tc.ID, err)
		}
	}
}

// recordGaveUp records a notice for abandoned calls still unfinished when the
// watcher stops, so the session learns they may still be running.
func (r *Runner) recordGaveUp(req Request, toolCalls []ds4.ToolCall, calls []toolCallState, steeredAt time.Time, unfinished map[int]bool) {
	if r.lateTools == nil || req.Audit.SessionID == nil {
		return
	}
	for index := range unfinished {
		tc := toolCalls[index]
		late := LateToolResult{
			SessionID:  *req.Audit.SessionID,
			ToolCallID: tc.ID,
			Tool:       tc.Function.Name,
			Arguments:  stripNUL(truncateHead(tc.Function.Arguments, lateToolFieldBytes)),
			StartedAt:  calls[index].startedAt,
			SteeredAt:  steeredAt,
			EndedAt:    time.Now(),
			IsError:    true,
			Output:     fmt.Sprintf("Stopped watching this call after %s; it may still be running.", lateToolWaitLimit),
		}
		if process, ok := calls[index].interrupter.Process(); ok {
			late.PID, late.Command, late.StartedAt = process.PID, stripNUL(truncateHead(process.Command, lateToolFieldBytes)), process.StartedAt
		}
		if err := r.lateTools.RecordLateToolResult(late); err != nil {
			log.Printf("agent: record unfinished tool call %s: %v", tc.ID, err)
		}
	}
}

func formatSteering(steering *Steering) string {
	if steering.Aggressive {
		return "<user-steering mode=\"aggressive\" tool-execution=\"skipped\">\n" + steering.Text + "\n</user-steering>"
	}
	return "<user-steering mode=\"normal\">\n" + steering.Text + "\n</user-steering>"
}

func appendSteering(content, steering string) string {
	if content == "" {
		return steering
	}
	return content + "\n\n" + steering
}

func appendUniqueDeliveries(existing, additions []toolkit.FileDelivery) []toolkit.FileDelivery {
	seen := make(map[string]bool, len(existing)+len(additions))
	for _, delivery := range existing {
		seen[delivery.Path] = true
	}
	for _, delivery := range additions {
		if delivery.Path == "" || seen[delivery.Path] {
			continue
		}
		seen[delivery.Path] = true
		existing = append(existing, delivery)
	}
	return existing
}

func (r *Runner) executeTool(ctx context.Context, req Request, allowedTools map[string]toolkit.Tool, tc ds4.ToolCall, turnMessages []store.ChatMessage, reportOutput func(string)) *toolkit.ToolResult {
	t, ok := allowedTools[tc.Function.Name]
	if !ok {
		return toolkit.ErrorResult(fmt.Sprintf("agent: tool %q is not enabled for this run", tc.Function.Name))
	}

	var args map[string]any
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return toolkit.ErrorResult(fmt.Sprintf("agent: invalid arguments for tool %q: %v", tc.Function.Name, err))
		}
	}

	auditID := r.beginToolAudit(req.Audit, t, tc, args)
	toolCtx := toolkit.WithSessionID(ctx, req.OwnerID)
	toolCtx = toolkit.WithTurnMessages(toolCtx, turnMessages)
	if reportOutput != nil {
		toolCtx = toolkit.WithOutputReporter(toolCtx, reportOutput)
	}
	if r.interactions != nil && req.OnInteraction != nil {
		toolCtx = interaction.WithReporter(toolCtx, func(event interaction.Event) {
			req.OnInteraction(event)
		})
	}
	result := toolkit.RunTool(toolCtx, t, args)
	r.finishToolAudit(auditID, tc.Function.Arguments, result)
	return result
}

func (r *Runner) beginToolAudit(audit AuditOwner, tool toolkit.Tool, tc ds4.ToolCall, args map[string]any) uint {
	audited, ok := tool.(toolkit.Audited)
	if !ok || !audited.Audited() {
		return 0
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		r.notify.Warn("agent: marshal audit arguments for tool %q: %v", tc.Function.Name, err)
		return 0
	}
	row := store.ToolAudit{
		SessionID:         audit.SessionID,
		WorkflowRunID:     audit.WorkflowRunID,
		WorkflowStepRunID: audit.WorkflowStepRunID,
		ToolCallID:        tc.ID,
		ToolName:          tc.Function.Name,
		Arguments:         encoded,
	}
	if err := r.db.Create(&row).Error; err != nil {
		r.notify.Warn("agent: persist audit start for tool %q: %v", tc.Function.Name, err)
		return 0
	}
	return row.ID
}

func (r *Runner) finishToolAudit(auditID uint, arguments string, result *toolkit.ToolResult) {
	if auditID == 0 {
		return
	}
	if err := r.db.Model(&store.ToolAudit{}).Where("id = ?", auditID).Updates(map[string]any{
		"result":   transform.LimitToolExchangeContent(arguments, result.ContentForLLM()),
		"is_error": result.IsError,
	}).Error; err != nil {
		r.notify.Warn("agent: persist audit result %d: %v", auditID, err)
	}
}

func (r *Runner) trackConsecutiveToolCall(ctx context.Context, req Request, lastToolName *string, consecutiveToolCalls *int, toolName string) error {
	if toolName == *lastToolName {
		*consecutiveToolCalls++
	} else {
		*lastToolName = toolName
		*consecutiveToolCalls = 1
	}
	if *consecutiveToolCalls <= maxConsecutiveToolCalls {
		return nil
	}
	if r.interactions == nil || req.OnInteraction == nil {
		return fmt.Errorf("agent: tool %q called consecutively more than %d times; interactive approval is unavailable", toolName, maxConsecutiveToolCalls)
	}
	permissionCtx := interaction.WithReporter(ctx, func(event interaction.Event) {
		req.OnInteraction(event)
	})
	if err := r.interactions.RequestPermission(
		permissionCtx,
		req.OwnerID,
		"Continue repeated tool calls?",
		fmt.Sprintf("Tool %q has been called more than %d times consecutively. Approve to continue and reset the counter.", toolName, maxConsecutiveToolCalls),
	); err != nil {
		return fmt.Errorf("agent: tool %q called consecutively more than %d times: %w", toolName, maxConsecutiveToolCalls, err)
	}
	*consecutiveToolCalls = 1
	return nil
}
