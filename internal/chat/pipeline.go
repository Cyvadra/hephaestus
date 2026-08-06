// Package chat implements the per-session turn pipeline: assembling
// context, calling the LLM, running the tool loop, invoking Plugin hooks at
// each stage, and persisting the turn once (and only once) it completes.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/compress"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/tools"
	"gorm.io/gorm"
)

const (
	// maxToolLoopIterations bounds the LLM<->tool round trips within a
	// single turn so a misbehaving tool/model can't loop forever.
	maxToolLoopIterations = 8

	// compressionTriggerRatio matches the design doc's "current context
	// length > 80% max context length" compression trigger.
	compressionTriggerRatio = 0.8
)

// Pipeline runs turns for sessions.
type Pipeline struct {
	db       *gorm.DB
	reg      *registry.Registry
	toolReg  *tools.Registry
	plugins  *plugin.Registry
	llm      *llm.Client
	sessions *session.Service
	notify   *notify.Notifier
}

// NewPipeline wires together every dependency a turn needs.
func NewPipeline(
	db *gorm.DB,
	reg *registry.Registry,
	toolReg *tools.Registry,
	plugins *plugin.Registry,
	llmClient *llm.Client,
	sessions *session.Service,
	notifier *notify.Notifier,
) *Pipeline {
	return &Pipeline{
		db:       db,
		reg:      reg,
		toolReg:  toolReg,
		plugins:  plugins,
		llm:      llmClient,
		sessions: sessions,
		notify:   notifier,
	}
}

// TurnResult is the outcome of a completed turn: the persisted final
// assistant message plus any metadata Plugins attached along the way (e.g.
// an options plugin's suggested next-user-message alternatives).
type TurnResult struct {
	Message  *store.ChatMessage
	Metadata map[string]any
}

// turnPrep bundles the per-session state every turn needs before it can
// assemble context or call the LLM.
type turnPrep struct {
	sess       store.Session
	settings   store.SessionSettings
	identity   registry.Identity
	toolset    []tools.Tool
	activePath []store.ChatMessage
	compRow    *store.Compression
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

	toolGroups := make(map[string]tools.ToolGroupTools, len(p.reg.ToolGroups))
	for name, tg := range p.reg.ToolGroups {
		toolGroups[name] = tools.ToolGroupTools{Tools: tg.Tools}
	}
	toolset, err := p.toolReg.Expand(prep.settings.ToolGroups, toolGroups)
	if err != nil {
		p.notify.Error("chat: session %d: %v; message not persisted", sessionID, err)
		return prep, err
	}
	prep.toolset = toolset

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

// RunTurn processes one incoming user message for sessionID: it assembles
// context (identity, impressions, compression, active-path history), calls
// the LLM (running the tool loop as needed), and persists the resulting
// user/assistant/tool messages as a single transaction. On any error, or if
// ctx is cancelled (e.g. /stop), nothing is persisted.
func (p *Pipeline) RunTurn(ctx context.Context, sessionID uint, userText string) (*TurnResult, error) {
	return p.runTurn(ctx, sessionID, userText, nil, nil)
}

// RunTurnAtLeaf commits only if expectedLeaf is still active after the turn
// completes. It is the HTTP continuation entry point.
func (p *Pipeline) RunTurnAtLeaf(ctx context.Context, sessionID uint, expectedLeaf *uint, userText string) (*TurnResult, error) {
	return p.runTurn(ctx, sessionID, userText, expectedLeaf, nil)
}

// RunTurnStream behaves like RunTurn but streams assistant content deltas to
// onDelta as they arrive from the model. Persistence only happens once the
// full turn completes, exactly as in RunTurn.
func (p *Pipeline) RunTurnStream(ctx context.Context, sessionID uint, userText string, onDelta func(string)) (*TurnResult, error) {
	return p.runTurn(ctx, sessionID, userText, nil, onDelta)
}

// RunTurnStreamAtLeaf is RunTurnAtLeaf with incremental assistant deltas.
func (p *Pipeline) RunTurnStreamAtLeaf(ctx context.Context, sessionID uint, expectedLeaf *uint, userText string, onDelta func(string)) (*TurnResult, error) {
	return p.runTurn(ctx, sessionID, userText, expectedLeaf, onDelta)
}

func (p *Pipeline) runTurn(ctx context.Context, sessionID uint, userText string, expectedLeaf *uint, onDelta func(string)) (*TurnResult, error) {
	prep, err := p.prepare(sessionID)
	if err != nil {
		return nil, err
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
	turn := plugin.TurnContext{SessionID: sessionID, Messages: append(llmContext, pendingUser), Metadata: map[string]any{}}
	turn = p.plugins.Run(ctx, prep.settings.Plugins, plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)

	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, prep.activePath, prep.compRow, staticMessageCount, turn)
	if err != nil {
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

	return p.runFrom(ctx, sessionID, prep.settings, prep.identity, prep.toolset, turn, parentID, expectedLeaf, &editedUser, onDelta)
}

// Regenerate re-answers the nearest ancestor user message on sessionID's
// active path, creating a sibling assistant branch under it instead of
// persisting a new user message. If the active leaf is itself an
// unanswered user message, this is equivalent to answering it fresh.
func (p *Pipeline) Regenerate(ctx context.Context, sessionID uint) (*TurnResult, error) {
	prep, err := p.prepare(sessionID)
	if err != nil {
		return nil, err
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

	turn := plugin.TurnContext{SessionID: sessionID, Messages: llmContext, Metadata: map[string]any{}}
	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, pathUpToUser, prep.compRow, staticMessageCount, turn)
	if err != nil {
		return nil, err
	}

	return p.runFrom(ctx, sessionID, prep.settings, prep.identity, prep.toolset, turn, &userMsg.ID, prep.sess.ActiveLeafMessageID, nil, nil)
}

// runFrom runs converse and persists its output as a single chain parented
// at parentID. newUserMessage, when non-nil, is prepended to that chain and
// persisted as a new user message (RunTurn's case); when nil, the chain is
// parented directly onto an already-persisted user message (Regenerate's
// case) and no new user message is created.
func (p *Pipeline) runFrom(ctx context.Context, sessionID uint, settings store.SessionSettings, identity registry.Identity, toolset []tools.Tool, turn plugin.TurnContext, parentID, expectedLeaf *uint, newUserMessage *store.ChatMessage, onDelta func(string)) (*TurnResult, error) {
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

	turn.Messages = append(turn.Messages, final)
	turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)

	return &TurnResult{Message: &final, Metadata: turn.Metadata}, nil
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
func (p *Pipeline) converse(ctx context.Context, settings store.SessionSettings, identity registry.Identity, toolset []tools.Tool, turn plugin.TurnContext, onDelta func(string)) ([]store.ChatMessage, plugin.TurnContext, error) {
	messages := append([]store.ChatMessage(nil), turn.Messages...)
	var toPersist []store.ChatMessage
	allowedTools := make(map[string]tools.Tool, len(toolset))
	for _, tool := range toolset {
		allowedTools[tool.Name()] = tool
	}

	callLLM := func() (*ds4.ChatResponse, error) {
		if onDelta == nil {
			return p.llm.Call(ctx, identity, messages, toolset)
		}
		return p.llm.CallStream(ctx, identity, messages, toolset, func(d llm.StreamDelta) {
			if d.Content != "" {
				onDelta(d.Content)
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

	for iteration := 0; resp.FinishReason() == ds4.FinishReasonToolCalls; iteration++ {
		if iteration >= maxToolLoopIterations {
			return nil, turn, fmt.Errorf("chat: exceeded %d tool-loop iterations", maxToolLoopIterations)
		}

		assistantMsg, err := ds4MessageToStore(*resp.FirstMessage())
		if err != nil {
			return nil, turn, err
		}
		messages = append(messages, assistantMsg)
		toPersist = append(toPersist, assistantMsg)

		for _, tc := range resp.ToolCalls() {
			turn.Metadata["tool_call"] = tc
			turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookToolCall, plugin.PhaseBefore, turn)

			result, err := p.executeTool(ctx, turn.SessionID, allowedTools, tc)
			turn.Metadata["tool_result"] = result
			turn = p.plugins.Run(ctx, settings.Plugins, plugin.HookToolCall, plugin.PhaseAfter, turn)
			if err != nil {
				result = fmt.Sprintf("error: %v", err)
			}

			toolMsg := store.ChatMessage{Role: ds4.RoleTool, Content: result, ToolCallID: tc.ID, Timestamp: time.Now()}
			messages = append(messages, toolMsg)
			toPersist = append(toPersist, toolMsg)
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

func (p *Pipeline) executeTool(ctx context.Context, sessionID uint, allowedTools map[string]tools.Tool, tc ds4.ToolCall) (string, error) {
	t, ok := allowedTools[tc.Function.Name]
	if !ok {
		return "", fmt.Errorf("chat: tool %q is not enabled for this session", tc.Function.Name)
	}
	return t.Execute(tools.WithSessionID(ctx, sessionID), tc.Function.Arguments)
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
