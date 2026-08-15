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
	"sync"
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
	// OnInteraction forwards ask_permission requests to a visible client;
	// nil disables interactive approval (headless workflow runs).
	OnInteraction func(*interaction.Request)
	// ClaimNotifications atomically claims durable completion notifications
	// before an outbound model request. Claimed notifications are at-most-once.
	ClaimNotifications func() ([]Notification, error)
}

type Notification struct {
	ID   uint
	Text string
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
				notificationIDs = append(notificationIDs, notification.ID)
				notificationMessage := store.ChatMessage{Role: ds4.RoleSystem, Content: notification.Text, Timestamp: time.Now()}
				messages = append(messages, notificationMessage)
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
		return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs}, err
	}
	turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantFirstCallLLM, plugin.PhaseAfter, turn)
	messages = turn.Messages

	lastToolName := ""
	consecutiveToolCalls := 0
	for resp.FinishReason() == ds4.FinishReasonToolCalls {
		assistantMsg, err := StoreMessageFromDS4(*resp.FirstMessage())
		if err != nil {
			return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs}, err
		}
		messages = append(messages, assistantMsg)
		toPersist = append(toPersist, assistantMsg)
		turn.Messages = messages

		toolCalls := resp.ToolCalls()
		for _, tc := range toolCalls {
			if err := r.trackConsecutiveToolCall(ctx, req, &lastToolName, &consecutiveToolCalls, tc.Function.Name); err != nil {
				return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs}, err
			}
			turn.ToolCall = &tc
			turn.ToolResult = nil
			turn = r.plugins.Run(ctx, req.Plugins, plugin.HookToolCall, plugin.PhaseBefore, turn)
			turn.ToolCall = nil
		}

		var batchDeliveries []toolkit.FileDelivery
		messages, toPersist, turn, batchDeliveries = r.runToolCalls(ctx, req, callIndex, allowedTools, toolCalls, messages, toPersist, turn)
		deliveries = appendUniqueDeliveries(deliveries, batchDeliveries)

		turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantContinuousCallLLM, plugin.PhaseBefore, turn)
		messages = turn.Messages
		resp, err = callLLM()
		if err != nil {
			return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs}, err
		}
		turn = r.plugins.Run(ctx, req.Plugins, plugin.HookAssistantContinuousCallLLM, plugin.PhaseAfter, turn)
		messages = turn.Messages
	}

	final, err := StoreMessageFromDS4(*resp.FirstMessage())
	if err != nil {
		return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs}, err
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

	return Result{Messages: toPersist, Deliveries: deliveries, Turn: turn, NotificationIDs: notificationIDs}, nil
}

// runToolCalls executes a single model response's independent tool calls
// concurrently, runs the after-phase ToolCall hook for each with its
// outcome, and appends the resulting tool messages to messages/toPersist in
// the model's original order for deterministic persistence.
func (r *Runner) runToolCalls(ctx context.Context, req Request, callIndex int, allowedTools map[string]toolkit.Tool, toolCalls []ds4.ToolCall, messages, toPersist []store.ChatMessage, turn plugin.TurnContext) ([]store.ChatMessage, []store.ChatMessage, plugin.TurnContext, []toolkit.FileDelivery) {
	results := make([]*toolkit.ToolResult, len(toolCalls))
	var wg sync.WaitGroup
	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, tc ds4.ToolCall) {
			defer wg.Done()
			streamedBytes := 0
			results[idx] = r.executeTool(ctx, req, allowedTools, tc, turn.History, func(chunk string) {
				if req.OnDelta != nil {
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
		}(i, tc)
	}
	wg.Wait()

	var deliveries []toolkit.FileDelivery
	for toolIndex, tc := range toolCalls {
		result := results[toolIndex]
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
	return messages, toPersist, turn, deliveries
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
	toolCtx = toolkit.WithToolCall(toolCtx, tc)
	if reportOutput != nil {
		toolCtx = toolkit.WithOutputReporter(toolCtx, reportOutput)
	}
	if r.interactions != nil && req.OnInteraction != nil {
		toolCtx = interaction.WithReporter(toolCtx, func(event interaction.Event) {
			if event.Type == interaction.EventAskPermission {
				req.OnInteraction(&event.Request)
			}
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
		if event.Type == interaction.EventAskPermission {
			req.OnInteraction(&event.Request)
		}
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
