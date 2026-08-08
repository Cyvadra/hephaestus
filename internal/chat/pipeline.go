// Package chat implements the per-session turn pipeline: assembling
// context, calling the LLM, running the tool loop, invoking Plugin hooks at
// each stage, and persisting the turn once (and only once) it completes.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/compress"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"gorm.io/gorm"
)

const (
	// maxConsecutiveToolCalls bounds repeated calls to one tool without an
	// intervening call to another tool.
	maxConsecutiveToolCalls = 12

	// compressionTriggerRatio matches the design doc's "current context
	// length > 80% max context length" compression trigger.
	compressionTriggerRatio = 0.8
)

// Pipeline runs turns for sessions.
type Pipeline struct {
	db       *gorm.DB
	reg      *registry.Registry
	toolReg  *toolkit.Registry
	plugins  *plugin.Registry
	llm      *llm.Client
	sessions *session.Service
	notify   *notify.Notifier
	projects *project.Service
}

// NewPipeline wires together every dependency a turn needs.
func NewPipeline(
	db *gorm.DB,
	reg *registry.Registry,
	toolReg *toolkit.Registry,
	plugins *plugin.Registry,
	llmClient *llm.Client,
	sessions *session.Service,
	notifier *notify.Notifier,
	projects *project.Service,
) *Pipeline {
	return &Pipeline{
		db:       db,
		reg:      reg,
		toolReg:  toolReg,
		plugins:  plugins,
		llm:      llmClient,
		sessions: sessions,
		notify:   notifier,
		projects: projects,
	}
}

// TurnResult is the outcome of a completed turn: the persisted final
// assistant message plus any metadata Plugins attached along the way (e.g.
// an options plugin's suggested next-user-message alternatives).
type TurnResult struct {
	Message  *store.ChatMessage
	Metadata map[string]any
}

// StreamEvent is one user-visible progress update emitted during a turn.
type StreamEvent struct {
	Type     string
	Text     string
	ToolCall *StreamToolCall
	Session  *store.Session
}

// StreamToolCall identifies one tool invocation across incremental updates.
type StreamToolCall struct {
	CallIndex int    `json:"call_index"`
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	Status    string `json:"status"`
}

// turnPrep bundles the per-session state every turn needs before it can
// assemble context or call the LLM.
type turnPrep struct {
	sess       store.Session
	settings   store.SessionSettings
	identity   registry.Identity
	toolset    []toolkit.Tool
	activePath []store.ChatMessage
	compRow    *store.Compression
	// workspace is the required Project directory bound to this session.
	workspace string
}

func (p *Pipeline) prepare(sessionID uint) (turnPrep, error) {
	var prep turnPrep
	if err := p.db.First(&prep.sess, sessionID).Error; err != nil {
		return prep, fmt.Errorf("chat: load session %d: %w", sessionID, err)
	}
	prep.settings = prep.sess.Settings.Data()

	identity, ok := p.reg.Identities[prep.settings.Identity]
	if !ok {
		p.notify.Error("chat: session %d has missing identity %q; message not persisted", sessionID, prep.settings.Identity)
		return prep, fmt.Errorf("chat: identity %q not found", prep.settings.Identity)
	}
	prep.identity = identity
	for _, name := range prep.settings.Impressions {
		if _, ok := p.reg.Impressions[name]; !ok {
			return prep, fmt.Errorf("chat: impression %q not found", name)
		}
	}
	for _, name := range prep.settings.Plugins {
		if !p.plugins.Has(name) {
			return prep, fmt.Errorf("chat: plugin %q not found", name)
		}
	}

	toolGroups := make(map[string]toolkit.ToolGroupTools, len(p.reg.ToolGroups))
	for name, tg := range p.reg.ToolGroups {
		toolGroups[name] = toolkit.ToolGroupTools{Tools: tg.Tools}
	}
	toolset, err := p.toolReg.Expand(prep.settings.ToolGroups, toolGroups)
	if err != nil {
		p.notify.Error("chat: session %d: %v; message not persisted", sessionID, err)
		return prep, err
	}
	prep.toolset = toolset

	proj, err := p.projects.Get(prep.sess.ProjectID)
	if err != nil {
		p.notify.Error("chat: session %d has missing project id %d; message not persisted", sessionID, prep.sess.ProjectID)
		return prep, fmt.Errorf("chat: project %d not found", prep.sess.ProjectID)
	}
	prep.workspace = p.projects.Path(*proj)

	activePath, err := p.sessions.ActivePath(prep.sess)
	if err != nil {
		return prep, err
	}
	prep.activePath = activePath

	compRow, err := p.sessions.ResolveCompression(&prep.sess, activePath)
	if err != nil {
		return prep, err
	}
	prep.compRow = compRow

	return prep, nil
}

// TurnOptions carries the optional axes of a turn. ExpectedLeaf enables
// optimistic-concurrency on HTTP continuations (the turn commits only if
// that leaf is still active); OnDelta enables streaming of assistant
// progress events. Either may be nil.
type TurnOptions struct {
	ExpectedLeaf *uint
	OnDelta      func(StreamEvent)
}

// Run processes one incoming user message for sessionID: it assembles
// context (identity, impressions, compression, active-path history), calls
// the LLM (running the tool loop as needed), and persists the resulting
// user/assistant/tool messages as a single transaction. On any error, or if
// ctx is cancelled (e.g. /stop), nothing is persisted. With
// opts.ExpectedLeaf set, persistence is skipped when that leaf is no longer
// active; with opts.OnDelta set, assistant content deltas are streamed as
// they arrive while persistence still only happens once the turn completes.
func (p *Pipeline) Run(ctx context.Context, sessionID uint, userText string, opts TurnOptions) (*TurnResult, error) {
	return p.runTurn(ctx, sessionID, userText, opts.ExpectedLeaf, opts.OnDelta)
}

func (p *Pipeline) runTurn(ctx context.Context, sessionID uint, userText string, expectedLeaf *uint, onDelta func(StreamEvent)) (*TurnResult, error) {
	prep, err := p.prepare(sessionID)
	if err != nil {
		return nil, err
	}
	if prep.workspace != "" {
		ctx = toolkit.WithWorkspace(ctx, prep.workspace)
	}
	if !sameMessageID(prep.sess.ActiveLeafMessageID, expectedLeaf) {
		return nil, session.ErrStaleActiveLeaf
	}

	llmContext, err := p.buildContext(prep.settings, prep.activePath, prep.compRow)
	if err != nil {
		return nil, err
	}
	staticMessageCount := len(p.staticContext(prep.settings))

	pendingUser := store.ChatMessage{Role: ds4.RoleUser, Content: userText, Timestamp: time.Now()}
	turn := newTurnContext(sessionID, append(llmContext, pendingUser), len(prep.activePath) == 0, userText)
	turn = p.plugins.Run(ctx, prep.settings.Plugins, plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if turn.IsFirstTurn {
		if firstUser, err := lastUserMessage(turn.Messages); err == nil {
			turn.FirstUserMessage = firstUser.Content
		}
	}
	summaryDone := p.scheduleSessionSummary(ctx, prep.settings.Plugins, turn, onDelta)

	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, prep.activePath, prep.compRow, staticMessageCount, turn)
	if err != nil {
		p.awaitSessionSummary(ctx, summaryDone, onDelta)
		return nil, err
	}

	var parentID *uint
	if len(prep.activePath) > 0 {
		parentID = &prep.activePath[len(prep.activePath)-1].ID
	}
	// The pending user message (possibly edited by the incoming-message
	// hook) is what actually gets persisted, not the raw request text.
	editedUser, err := lastUserMessage(turn.Messages)
	if err != nil {
		return nil, err
	}
	if turn.IsFirstTurn {
		turn.FirstUserMessage = editedUser.Content
	}

	result, err := p.runFrom(ctx, sessionID, prep.settings, prep.identity, prep.toolset, turn, parentID, expectedLeaf, &editedUser, onDelta)
	p.awaitSessionSummary(ctx, summaryDone, onDelta)
	return result, err
}

// scheduleSessionSummary starts the fixed session-summary plugin after
// inbound plugins have finalized the pending user message. It deliberately
// outlives the turn context so an LLM or tool failure cannot prevent the
// title and summary from being refreshed.
func (p *Pipeline) scheduleSessionSummary(ctx context.Context, names []string, turn plugin.TurnContext, onDelta func(StreamEvent)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		result := p.plugins.Run(context.WithoutCancel(ctx), names, plugin.HookSessionSummaryRequested, plugin.PhaseAfter, turn)
		updated, _ := result.Metadata["session_summary_updated"].(bool)
		if !updated {
			return
		}

		var sess store.Session
		if err := p.db.First(&sess, turn.SessionID).Error; err != nil {
			p.notify.Warn("chat: load session %d after summary: %v", turn.SessionID, err)
			return
		}
		if onDelta != nil {
			onDelta(StreamEvent{Type: "session_updated", Session: &sess})
		}
	}()
	return done
}

// awaitSessionSummary keeps a streaming response open long enough to report
// a completed title/summary update. Non-streaming callers do not wait.
func (p *Pipeline) awaitSessionSummary(ctx context.Context, done <-chan struct{}, onDelta func(StreamEvent)) {
	if onDelta == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Regenerate re-answers the nearest ancestor user message on sessionID's
// active path, creating a sibling assistant branch under it instead of
// persisting a new user message. If the active leaf is itself an
// unanswered user message, this is equivalent to answering it fresh. With
// opts.OnDelta set, assistant content deltas are streamed as they arrive.
func (p *Pipeline) Regenerate(ctx context.Context, sessionID uint, opts TurnOptions) (*TurnResult, error) {
	return p.regenerate(ctx, sessionID, opts.OnDelta)
}

func (p *Pipeline) regenerate(ctx context.Context, sessionID uint, onDelta func(StreamEvent)) (*TurnResult, error) {
	prep, err := p.prepare(sessionID)
	if err != nil {
		return nil, err
	}
	if prep.workspace != "" {
		ctx = toolkit.WithWorkspace(ctx, prep.workspace)
	}
	if len(prep.activePath) == 0 {
		return nil, fmt.Errorf("chat: session %d has no messages to regenerate", sessionID)
	}

	userIdx := -1
	for i := len(prep.activePath) - 1; i >= 0; i-- {
		if prep.activePath[i].Role == ds4.RoleUser {
			userIdx = i
			break
		}
	}
	if userIdx == -1 {
		return nil, fmt.Errorf("chat: session %d active path has no user message to regenerate from", sessionID)
	}
	userMsg := prep.activePath[userIdx]
	pathUpToUser := prep.activePath[:userIdx+1]

	llmContext, err := p.buildContext(prep.settings, pathUpToUser, prep.compRow)
	if err != nil {
		return nil, err
	}
	staticMessageCount := len(p.staticContext(prep.settings))

	turn := newTurnContext(sessionID, llmContext, userIdx == 0, userMsg.Content)
	// Compression on the regenerate path covers history up to (but not
	// including) the user message being regenerated from: that message must
	// stay in context, and it is what the regenerated reply re-answers.
	// Passing the path without the user message lands the coverage boundary
	// on the message before it, matching maybeCompress's assumption that the
	// kept tail is the message the reply answers. This is safe because
	// compression coverage always ends on an assistant message, never on a
	// user message, so the message before the user is always at or after the
	// previous coverage boundary.
	compressPath := pathUpToUser[:userIdx]
	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, compressPath, prep.compRow, staticMessageCount, turn)
	if err != nil {
		return nil, err
	}

	return p.runFrom(ctx, sessionID, prep.settings, prep.identity, prep.toolset, turn, &userMsg.ID, prep.sess.ActiveLeafMessageID, nil, onDelta)
}

// runFrom runs converse and persists its output as a single chain parented
// at parentID. newUserMessage, when non-nil, is prepended to that chain and
// persisted as a new user message (Run's case); when nil, the chain is
// parented directly onto an already-persisted user message (Regenerate's
// case) and no new user message is created.
func (p *Pipeline) runFrom(ctx context.Context, sessionID uint, settings store.SessionSettings, identity registry.Identity, toolset []toolkit.Tool, turn plugin.TurnContext, parentID, expectedLeaf *uint, newUserMessage *store.ChatMessage, onDelta func(StreamEvent)) (*TurnResult, error) {
	toPersist, turn, err := p.converse(ctx, settings, identity, toolset, turn, onDelta)
	if err != nil {
		return nil, err
	}

	persistMessages := toPersist
	if newUserMessage != nil {
		persistMessages = append([]store.ChatMessage{*newUserMessage}, toPersist...)
	}

	saved, err := p.sessions.AppendMessagesAtLeaf(sessionID, parentID, expectedLeaf, persistMessages)
	if err != nil {
		return nil, err
	}
	final := saved[len(saved)-1]

	turn.Messages[len(turn.Messages)-1] = final
	go p.plugins.Run(context.WithoutCancel(ctx), settings.Plugins, plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)

	return &TurnResult{Message: &final, Metadata: turn.Metadata}, nil
}

func newTurnContext(sessionID uint, messages []store.ChatMessage, isFirstTurn bool, firstUserMessage string) plugin.TurnContext {
	return plugin.TurnContext{
		SessionID:        sessionID,
		Messages:         messages,
		IsFirstTurn:      isFirstTurn,
		FirstUserMessage: firstUserMessage,
		Metadata:         map[string]any{},
	}
}

// buildContext assembles the messages sent to the LLM ahead of the pending
// user message: enabled impressions (in order), then either the unpacked
// compression plus history after its coverage, or the full active path.
func (p *Pipeline) buildContext(settings store.SessionSettings, activePath []store.ChatMessage, compRow *store.Compression) ([]store.ChatMessage, error) {
	out := p.staticContext(settings)

	if compRow == nil {
		return append(out, activePath...), nil
	}

	var unpacked []compress.Message
	if err := json.Unmarshal(compRow.Messages, &unpacked); err != nil {
		return nil, fmt.Errorf("chat: unpack compression %d: %w", compRow.ID, err)
	}
	for _, m := range unpacked {
		out = append(out, store.ChatMessage{Role: m.Role, Content: m.Content})
	}

	for _, m := range activePath {
		if m.ID > compRow.LastMessageID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (p *Pipeline) staticContext(settings store.SessionSettings) []store.ChatMessage {
	var out []store.ChatMessage
	for _, name := range settings.Impressions {
		imp, ok := p.reg.Impressions[name]
		if !ok || !imp.Enabled {
			continue
		}
		for _, m := range imp.Messages {
			out = append(out, store.ChatMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out
}

// compressIfNeeded fires HookContextCompression around the compression
// decision (so Plugins can observe it) and, when triggered, replaces the
// active path through its last persisted assistant message. Static context
// and the pending user message are deliberately kept out of the compression
// cache.
//
// Unlike every other hook, the decision and the actual LLM-driven
// compaction stay pipeline-owned rather than delegated to a
// Registry-dispatched Plugin: per the design doc, a failed compression must
// abort the whole turn exactly like /stop, which the generic "skip a
// failing plugin and continue" Plugin contract cannot express.
func (p *Pipeline) compressIfNeeded(ctx context.Context, pluginNames []string, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, staticMessageCount int, turn plugin.TurnContext) (plugin.TurnContext, error) {
	turn = p.plugins.Run(ctx, pluginNames, plugin.HookContextCompression, plugin.PhaseBefore, turn)
	turn, err := p.maybeCompress(ctx, sess, identity, activePath, compRow, staticMessageCount, turn)
	if err != nil {
		return plugin.TurnContext{}, err
	}
	return p.plugins.Run(ctx, pluginNames, plugin.HookContextCompression, plugin.PhaseAfter, turn), nil
}

func (p *Pipeline) maybeCompress(ctx context.Context, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, staticMessageCount int, turn plugin.TurnContext) (plugin.TurnContext, error) {
	total := 0
	for _, m := range turn.Messages {
		total += compress.EstimateLength(m.Content)
	}
	if float64(total) <= compressionTriggerRatio*float64(identity.ContextWindowTokens) {
		return turn, nil
	}
	if len(activePath) == 0 || activePath[len(activePath)-1].Role != ds4.RoleAssistant {
		return turn, nil
	}

	toCompress := turn.Messages[staticMessageCount : len(turn.Messages)-1]
	if len(toCompress) == 0 {
		return turn, nil
	}
	keep := turn.Messages[len(turn.Messages)-1:]

	input := make([]compress.Message, len(toCompress))
	for i, m := range toCompress {
		input[i] = compress.Message{Role: m.Role, Content: m.Content}
	}

	compacted, err := compress.Compress(ctx, p.llm, input, int(float64(total)*0.3))
	if err != nil {
		// Compression failure behaves like /stop: abort the turn, leave
		// everything as-is.
		return plugin.TurnContext{}, fmt.Errorf("chat: compression failed, aborting turn: %w", err)
	}

	newMessages := make([]store.ChatMessage, 0, len(compacted)+1)
	for _, m := range compacted {
		newMessages = append(newMessages, store.ChatMessage{Role: m.Role, Content: m.Content})
	}
	newMessages = append(newMessages, keep...)

	firstMessageID := activePath[0].ID
	if compRow != nil {
		firstMessageID = compRow.FirstMessageID
	}
	compactedJSON, err := json.Marshal(compacted)
	if err != nil {
		return plugin.TurnContext{}, fmt.Errorf("chat: marshal compression: %w", err)
	}
	if _, err := p.sessions.StoreCompression(sess.ID, firstMessageID, activePath[len(activePath)-1].ID, compactedJSON); err != nil {
		return plugin.TurnContext{}, fmt.Errorf("chat: persist compression: %w", err)
	}

	turn.Messages = newMessages
	return turn, nil
}

// converse runs the first LLM call and, while the model requests tool
// calls, the tool-execution loop, wrapping each stage in the corresponding
// Plugin hooks. It returns every message generated along the way (assistant
// tool-call messages, tool results, final assistant message) in
// persistence order, plus the turn context as left by the last plugin that
// ran (carrying any Metadata plugins attached, and any content mutation the
// completion hook made to the final assistant message).
func (p *Pipeline) converse(ctx context.Context, settings store.SessionSettings, identity registry.Identity, toolset []toolkit.Tool, turn plugin.TurnContext, onDelta func(StreamEvent)) ([]store.ChatMessage, plugin.TurnContext, error) {
	messages := append([]store.ChatMessage(nil), turn.Messages...)
	var toPersist []store.ChatMessage
	allowedTools := make(map[string]toolkit.Tool, len(toolset))
	for _, tool := range toolset {
		allowedTools[tool.Name()] = tool
	}

	callIndex := -1
	callLLM := func() (*ds4.ChatResponse, error) {
		callIndex++
		if onDelta == nil {
			return p.llm.Call(ctx, identity, messages, toolset)
		}
		return p.llm.CallStream(ctx, identity, messages, toolset, func(d llm.StreamDelta) {
			if d.Content != "" {
				onDelta(StreamEvent{Type: "delta", Text: d.Content})
			}
			if d.ReasoningContent != "" {
				onDelta(StreamEvent{Type: "reasoning", Text: d.ReasoningContent})
			}
			for _, toolCall := range d.ToolCalls {
				onDelta(StreamEvent{Type: "tool_call", ToolCall: &StreamToolCall{
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

	turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookAssistantFirstCallLLM, plugin.PhaseBefore, turn)
	messages = turn.Messages
	resp, err := callLLM()
	if err != nil {
		return nil, turn, err
	}
	turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookAssistantFirstCallLLM, plugin.PhaseAfter, turn)
	messages = turn.Messages

	lastToolName := ""
	consecutiveToolCalls := 0
	for resp.FinishReason() == ds4.FinishReasonToolCalls {

		assistantMsg, err := ds4MessageToStore(*resp.FirstMessage())
		if err != nil {
			return nil, turn, err
		}
		messages = append(messages, assistantMsg)
		toPersist = append(toPersist, assistantMsg)
		turn.Messages = messages

		toolCalls := resp.ToolCalls()
		for _, tc := range toolCalls {
			if err := trackConsecutiveToolCall(&lastToolName, &consecutiveToolCalls, tc.Function.Name); err != nil {
				return nil, turn, err
			}
			turn.Metadata["tool_call"] = tc
			turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookToolCall, plugin.PhaseBefore, turn)
		}

		// Tool calls from a single model response are independent of one
		// another, so they run concurrently; results are collected back in
		// the model's original order for deterministic persistence.
		results := make([]*toolkit.ToolResult, len(toolCalls))
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(idx int, tc ds4.ToolCall) {
				defer wg.Done()
				results[idx] = p.executeTool(ctx, turn.SessionID, allowedTools, tc)
			}(i, tc)
		}
		wg.Wait()

		for toolIndex, tc := range toolCalls {
			result := results[toolIndex]
			turn.Metadata["tool_result"] = result
			turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookToolCall, plugin.PhaseAfter, turn)

			content := result.ContentForLLM()
			if onDelta != nil {
				status := "complete"
				if result.IsError {
					status = "error"
				}
				onDelta(StreamEvent{Type: "tool_result", ToolCall: &StreamToolCall{
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

		turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookAssistantContinuousCallLLM, plugin.PhaseBefore, turn)
		messages = turn.Messages
		resp, err = callLLM()
		if err != nil {
			return nil, turn, err
		}
		turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookAssistantContinuousCallLLM, plugin.PhaseAfter, turn)
		messages = turn.Messages
	}

	final, err := ds4MessageToStore(*resp.FirstMessage())
	if err != nil {
		return nil, turn, err
	}
	toPersist = append(toPersist, final)

	turn.Messages = append(messages, final)
	turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookAssistantMessageCompletion, plugin.PhaseAfter, turn)

	// A plugin may have rewritten the final assistant message's content
	// (e.g. to refresh a storyline-state suffix); splice that back into
	// what actually gets persisted.
	if n := len(turn.Messages); n > 0 {
		toPersist[len(toPersist)-1].Content = turn.Messages[n-1].Content
	}

	return toPersist, turn, nil
}

func (p *Pipeline) executeTool(ctx context.Context, sessionID uint, allowedTools map[string]toolkit.Tool, tc ds4.ToolCall) *toolkit.ToolResult {
	t, ok := allowedTools[tc.Function.Name]
	if !ok {
		return toolkit.ErrorResult(fmt.Sprintf("chat: tool %q is not enabled for this session", tc.Function.Name))
	}

	var args map[string]any
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return toolkit.ErrorResult(fmt.Sprintf("chat: invalid arguments for tool %q: %v", tc.Function.Name, err))
		}
	}

	auditID := p.beginToolAudit(sessionID, tc, args)
	result := toolkit.RunTool(toolkit.WithSessionID(ctx, sessionID), t, args)
	p.finishToolAudit(auditID, result)
	return result
}

var auditedTools = map[string]bool{
	"append_file":    true,
	"create_project": true,
	"edit_file":      true,
	"exec":           true,
	"write_file":     true,
}

func (p *Pipeline) beginToolAudit(sessionID uint, tc ds4.ToolCall, args map[string]any) uint {
	if !auditedTools[tc.Function.Name] {
		return 0
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		p.notify.Warn("chat: marshal audit arguments for tool %q: %v", tc.Function.Name, err)
		return 0
	}
	row := store.ToolAudit{SessionID: sessionID, ToolCallID: tc.ID, ToolName: tc.Function.Name, Arguments: encoded}
	if err := p.db.Create(&row).Error; err != nil {
		p.notify.Warn("chat: persist audit start for tool %q: %v", tc.Function.Name, err)
		return 0
	}
	return row.ID
}

func (p *Pipeline) finishToolAudit(auditID uint, result *toolkit.ToolResult) {
	if auditID == 0 {
		return
	}
	if err := p.db.Model(&store.ToolAudit{}).Where("id = ?", auditID).Updates(map[string]any{
		"result":   result.ContentForLLM(),
		"is_error": result.IsError,
	}).Error; err != nil {
		p.notify.Warn("chat: persist audit result %d: %v", auditID, err)
	}
}

func trackConsecutiveToolCall(lastToolName *string, consecutiveToolCalls *int, toolName string) error {
	if toolName == *lastToolName {
		*consecutiveToolCalls++
	} else {
		*lastToolName = toolName
		*consecutiveToolCalls = 1
	}
	if *consecutiveToolCalls > maxConsecutiveToolCalls {
		return fmt.Errorf("chat: tool %q called consecutively more than %d times", toolName, maxConsecutiveToolCalls)
	}
	return nil
}

func ds4MessageToStore(m ds4.Message) (store.ChatMessage, error) {
	out := store.ChatMessage{
		Role:             m.Role,
		Content:          m.Content,
		ReasoningContent: m.ReasoningContent,
		ToolCallID:       m.ToolCallID,
		Timestamp:        time.Now(),
	}
	if len(m.ToolCalls) > 0 {
		data, err := json.Marshal(m.ToolCalls)
		if err != nil {
			return store.ChatMessage{}, fmt.Errorf("chat: marshal tool_calls: %w", err)
		}
		out.ToolCalls = data
	}
	return out, nil
}

// lastUserMessage returns the trailing message of messages, which must be
// the pending user turn: HookUserMessageIncoming plugins may edit its
// content but must not remove or reorder it away from the tail.
func lastUserMessage(messages []store.ChatMessage) (store.ChatMessage, error) {
	if len(messages) == 0 {
		return store.ChatMessage{}, fmt.Errorf("chat: no messages to persist as the user turn")
	}
	last := messages[len(messages)-1]
	if last.Role != ds4.RoleUser {
		return store.ChatMessage{}, fmt.Errorf("chat: expected trailing message to be role %q, got %q (a plugin likely reordered or appended after the pending user message)", ds4.RoleUser, last.Role)
	}
	return last, nil
}

func sameMessageID(left, right *uint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
