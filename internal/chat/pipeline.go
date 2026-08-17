// Package chat implements the per-session turn pipeline: assembling
// context, calling the LLM, running the tool loop, invoking Plugin hooks at
// each stage, and persisting the turn once (and only once) it completes.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	// compressionTriggerRatio matches the design doc's "current context
	// length > 80% max context length" compression trigger.
	compressionTriggerRatio = 0.8
)

// Pipeline runs turns for sessions.
type Pipeline struct {
	db            *gorm.DB
	registries    *registry.Store
	toolReg       *toolkit.Registry
	plugins       *plugin.Registry
	llm           *llm.Client
	agent         *agent.Runner
	sessions      *session.Service
	notify        *notify.Notifier
	projects      *project.Service
	interactions  *interaction.Manager
	notifications NotificationSource
}

type NotificationSource interface {
	ClaimNotifications(uint) ([]agent.Notification, error)
	AcknowledgeNotificationsTx(*gorm.DB, []uint) error
	ReleaseNotifications([]uint) error
}

func (p *Pipeline) SetNotificationSource(source NotificationSource) { p.notifications = source }

// NewPipeline wires together every dependency a turn needs.
func NewPipeline(
	db *gorm.DB,
	registries *registry.Store,
	toolReg *toolkit.Registry,
	plugins *plugin.Registry,
	llmClient *llm.Client,
	runner *agent.Runner,
	sessions *session.Service,
	notifier *notify.Notifier,
	projects *project.Service,
	interactions *interaction.Manager,
) *Pipeline {
	return &Pipeline{
		db:           db,
		registries:   registries,
		toolReg:      toolReg,
		plugins:      plugins,
		llm:          llmClient,
		agent:        runner,
		sessions:     sessions,
		notify:       notifier,
		projects:     projects,
		interactions: interactions,
	}
}

// TurnResult is the outcome of a completed turn: the persisted final
// assistant message plus any metadata Plugins attached along the way (e.g.
// an options plugin's suggested next-user-message alternatives).
type TurnResult struct {
	Message  *store.ChatMessage
	Metadata map[string]any
	turn     plugin.TurnContext
}

// StreamEvent is one user-visible progress update emitted during a turn. It
// aliases the agent runtime's event so handlers and the shared loop emit the
// same type.
type StreamEvent = agent.StreamEvent

// StreamToolCall identifies one tool invocation across incremental updates.
type StreamToolCall = agent.StreamToolCall

// turnPrep bundles the per-session state every turn needs before it can
// assemble context or call the LLM.
type turnPrep struct {
	sess       store.Session
	settings   store.SessionSettings
	registry   *registry.Registry
	identity   registry.Identity
	toolset    []toolkit.Tool
	activePath []store.ChatMessage
	compRow    *store.Compression
	vars       registry.PromptVars
	// workspace is the required Project directory bound to this session.
	workspace string
}

func (p *Pipeline) prepare(sessionID uint) (turnPrep, error) {
	var prep turnPrep
	prep.registry = p.registries.Current()
	if err := p.db.First(&prep.sess, sessionID).Error; err != nil {
		return prep, fmt.Errorf("chat: load session %d: %w", sessionID, err)
	}
	settings, err := p.resolveSettings(&prep.sess)
	if err != nil {
		return prep, err
	}
	prep.settings = settings

	toolGroups := make(map[string]toolkit.ToolGroupTools, len(prep.registry.ToolGroups))
	for name, tg := range prep.registry.ToolGroups {
		toolGroups[name] = toolkit.ToolGroupTools{Tools: tg.Tools}
	}
	toolset, err := p.toolReg.Expand(prep.settings.ToolGroups, toolGroups)
	if err != nil {
		p.notify.Error("chat: session %d: %v; message not persisted", sessionID, err)
		return prep, err
	}
	if prep.sess.EnableWebSearch != nil && !*prep.sess.EnableWebSearch {
		toolset = filterTools(toolset, map[string]struct{}{
			"web_search": {},
			"web_fetch":  {},
		})
	}
	prep.toolset = toolset

	proj, err := p.projects.Get(prep.sess.ProjectID)
	if err != nil {
		p.notify.Error("chat: session %d has missing project id %d; message not persisted", sessionID, prep.sess.ProjectID)
		return prep, fmt.Errorf("chat: project %d not found", prep.sess.ProjectID)
	}
	prep.workspace = p.projects.Path(*proj)
	prep.vars = registry.TimePromptVars(time.Now())
	prep.vars["project"] = proj.Name
	prep.vars["workspace"] = prep.workspace
	prep.vars["session_id"] = strconv.FormatUint(uint64(prep.sess.ID), 10)
	prep.vars["session_title"] = prep.sess.Title
	prep.identity, err = renderSessionIdentity(prep.registry, settings, prep.vars)
	if err != nil {
		return prep, fmt.Errorf("chat: %w", err)
	}

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

func renderSessionIdentity(reg *registry.Registry, settings store.SessionSettings, vars ...registry.PromptVars) (registry.Identity, error) {
	return reg.RenderIdentity(reg.Identities[settings.Identity], vars...)
}

// resolveSettings sanitizes a session's settings against the current
// registry, self-healing stale references: a missing identity falls back to
// the registry's default, and unknown impressions, tool groups and plugins
// are dropped. Repaired settings are persisted so the session converges to
// a healthy state instead of failing every turn.
func (p *Pipeline) resolveSettings(sess *store.Session) (store.SessionSettings, error) {
	settings := sess.Settings.Data()
	reg := p.registries.Current()
	dirty := false

	if _, ok := reg.Identities[settings.Identity]; !ok {
		fallback := reg.DefaultIdentityName()
		if fallback == "" {
			return settings, fmt.Errorf("chat: session %d references missing identity %q and no fallback identity exists", sess.ID, settings.Identity)
		}
		settings.Identity = fallback
		dirty = true
	}
	settings.Impressions = keepRegistered(settings.Impressions, reg.Impressions, &dirty)
	settings.ToolGroups = keepRegistered(settings.ToolGroups, reg.ToolGroups, &dirty)
	settings.Plugins = keepKnownPlugins(settings.Plugins, p.plugins, &dirty)
	if concierge, ok := reg.Concierges[sess.SourceConcierge]; ok {
		settings.ToolGroups = keepAllowed(settings.ToolGroups, concierge.ToolGroups, &dirty)
		settings.Plugins = keepAllowed(settings.Plugins, concierge.Plugins, &dirty)
	}

	if !dirty {
		return settings, nil
	}
	if err := p.db.Model(sess).Update("settings", datatypes.NewJSONType(settings)).Error; err != nil {
		return settings, fmt.Errorf("chat: repair session %d settings: %w", sess.ID, err)
	}
	p.notify.Warn("chat: session %d settings repaired: identity=%q impressions=%v tool_groups=%v plugins=%v",
		sess.ID, settings.Identity, settings.Impressions, settings.ToolGroups, settings.Plugins)
	sess.Settings = datatypes.NewJSONType(settings)
	return settings, nil
}

func keepAllowed(names, available []string, dirty *bool) []string {
	allowed := make(map[string]struct{}, len(available))
	for _, name := range available {
		allowed[name] = struct{}{}
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := allowed[name]; ok {
			out = append(out, name)
		} else {
			*dirty = true
		}
	}
	return out
}

func keepRegistered[T any](names []string, known map[string]T, dirty *bool) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := known[name]; ok {
			out = append(out, name)
		} else {
			*dirty = true
		}
	}
	return out
}

func keepKnownPlugins(names []string, reg *plugin.Registry, dirty *bool) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if reg.Has(name) {
			out = append(out, name)
		} else {
			*dirty = true
		}
	}
	return out
}

// TurnOptions carries optional per-turn settings, an optional branch to
// continue from, and a caller-provided optimistic concurrency guard. Branch
// selection is read-only until the turn is committed.
type TurnOptions struct {
	ExpectedLeaf    *uint
	SelectedLeaf    *uint
	SelectRoot      bool
	OnDelta         func(StreamEvent)
	ReasoningEffort string
	DisabledTools   []string
	NotificationIDs []uint
}

func applyTurnOptions(identity registry.Identity, toolset []toolkit.Tool, opts TurnOptions) (registry.Identity, []toolkit.Tool) {
	if opts.ReasoningEffort != "" {
		identity.ReasoningEffort = opts.ReasoningEffort
	}
	if len(opts.DisabledTools) == 0 {
		return identity, toolset
	}

	disabled := make(map[string]struct{}, len(opts.DisabledTools))
	for _, name := range opts.DisabledTools {
		disabled[name] = struct{}{}
	}
	return identity, filterTools(toolset, disabled)
}

func filterTools(toolset []toolkit.Tool, disabled map[string]struct{}) []toolkit.Tool {
	filtered := make([]toolkit.Tool, 0, len(toolset))
	for _, tool := range toolset {
		if _, blocked := disabled[tool.Name()]; !blocked {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// prepareTurn performs the shared per-turn setup every entry point needs:
// loading the session and its settings and binding the session's workspace.
// The caller then applies its own leaf checks and turn-specific context.
func (p *Pipeline) prepareTurn(ctx context.Context, sessionID uint, opts TurnOptions) (turnPrep, context.Context, error) {
	prep, err := p.prepare(sessionID)
	if err != nil {
		return prep, ctx, err
	}
	if prep.workspace != "" {
		ctx = toolkit.WithWorkspace(ctx, prep.workspace)
	}
	return prep, ctx, nil
}

// Run processes one incoming user message for sessionID: it assembles
// context (identity, impressions, compression, active-path history), calls
// the LLM (running the tool loop as needed), and persists the resulting
// user/assistant/tool messages as a single transaction. On an error or
// cancellation, the latest records that were obtained are persisted, with
// an incomplete assistant response marked accordingly. With
// with opts.OnDelta set, assistant content deltas are streamed as they arrive
// while persistence still only happens once the turn completes.
func (p *Pipeline) Run(ctx context.Context, sessionID uint, userText string, opts TurnOptions) (*TurnResult, error) {
	prep, ctx, err := p.prepareTurn(ctx, sessionID, opts)
	if err != nil {
		return nil, err
	}
	expectedLeaf := prep.sess.ActiveLeafMessageID
	if opts.ExpectedLeaf != nil {
		if !sameMessageID(prep.sess.ActiveLeafMessageID, opts.ExpectedLeaf) {
			return nil, session.ErrStaleActiveLeaf
		}
		expectedLeaf = opts.ExpectedLeaf
	}
	if opts.SelectRoot {
		prep.activePath = nil
	} else if opts.SelectedLeaf != nil {
		prep.activePath, err = p.sessions.PathAtLeaf(prep.sess, opts.SelectedLeaf)
		if err != nil {
			return nil, err
		}
	}
	prep.identity, prep.toolset = applyTurnOptions(prep.identity, prep.toolset, opts)

	llmContext, err := p.buildContext(prep.registry, prep.settings, prep.activePath, prep.compRow, prep.vars)
	if err != nil {
		return nil, err
	}

	pendingUser := store.ChatMessage{Role: ds4.RoleUser, Content: userText, Timestamp: time.Now()}
	turn := newTurnContext(sessionID, append(llmContext, pendingUser), len(prep.activePath) == 0, userText)
	turn.Identity = prep.identity
	turn.History = append(append([]store.ChatMessage(nil), prep.activePath...), pendingUser)
	turn = p.plugins.Run(ctx, prep.settings.Plugins, plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	incomingPersistMessages, err := incomingMessagesToPersist(turn.Messages, len(llmContext))
	if err != nil {
		return nil, err
	}

	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, prep.activePath, prep.compRow, turn)
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
	incomingPersistMessages = append(incomingPersistMessages, editedUser)
	result, err := p.runFrom(ctx, sessionID, prep.sess.ProjectID, prep.settings, prep.identity, prep.toolset, turn, parentID, expectedLeaf, incomingPersistMessages, opts.NotificationIDs, opts.OnDelta)
	if result != nil && err == nil {
		summaryDone := p.scheduleSessionSummary(ctx, prep.settings.Plugins, result.turn, opts.OnDelta)
		p.awaitSessionSummary(ctx, summaryDone, opts.OnDelta)
	}
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

// Continue resumes an incomplete assistant response at the active session
// leaf. The prefix remains in history and the generated suffix is persisted
// as its child, so the complete reply can be reconstructed without copying
// the already-generated content.
func (p *Pipeline) Continue(ctx context.Context, sessionID, messageID uint, opts TurnOptions) (*TurnResult, error) {
	prep, ctx, err := p.prepareTurn(ctx, sessionID, opts)
	if err != nil {
		return nil, err
	}
	if prep.sess.ActiveLeafMessageID == nil || *prep.sess.ActiveLeafMessageID != messageID {
		return nil, session.ErrStaleActiveLeaf
	}
	if len(prep.activePath) == 0 {
		return nil, fmt.Errorf("chat: session %d has no message to continue", sessionID)
	}
	prefix := prep.activePath[len(prep.activePath)-1]
	if prefix.ID != messageID || prefix.Role != ds4.RoleAssistant || prefix.Status != store.MessageStatusIncomplete || prefix.Content == "" {
		return nil, fmt.Errorf("chat: message %d is not a continuable incomplete assistant response", messageID)
	}

	contextMessages, err := p.buildContext(prep.registry, prep.settings, prep.activePath[:len(prep.activePath)-1], prep.compRow, prep.vars)
	if err != nil {
		return nil, err
	}
	response, err := p.continueResponse(ctx, prep.identity, contextMessages, prefix, opts.OnDelta)
	if err != nil {
		if response.Content == "" && response.ReasoningContent == "" && len(response.ToolCalls) == 0 {
			return nil, err
		}
		if _, saveErr := p.sessions.AppendMessagesAtLeaf(sessionID, &messageID, &messageID, []store.ChatMessage{response}); saveErr != nil {
			return nil, saveErr
		}
		return nil, err
	}
	response.Status = store.MessageStatusComplete
	saved, err := p.sessions.AppendMessagesAtLeaf(sessionID, &messageID, &messageID, []store.ChatMessage{response})
	if err != nil {
		return nil, err
	}
	final := saved[0]
	return &TurnResult{Message: &final, Metadata: map[string]any{}}, nil
}

func (p *Pipeline) continueResponse(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, prefix store.ChatMessage, onDelta func(StreamEvent)) (store.ChatMessage, error) {
	response, err := p.llm.ContinueStream(ctx, identity, messages, prefix, func(delta llm.StreamDelta) {
		if onDelta == nil {
			return
		}
		if delta.Content != "" {
			onDelta(StreamEvent{Type: "delta", Text: delta.Content})
		}
		if delta.ReasoningContent != "" {
			onDelta(StreamEvent{Type: "reasoning", Text: delta.ReasoningContent})
		}
	})
	if err != nil {
		var incomplete *llm.IncompleteResponseError
		if errors.As(err, &incomplete) && hasMessageContent(incomplete.Message) {
			partial, convertErr := agent.StoreMessageFromDS4(incomplete.Message)
			if convertErr == nil {
				partial.Status = store.MessageStatusIncomplete
				return partial, err
			}
		}
		return store.ChatMessage{}, err
	}
	return agent.StoreMessageFromDS4(*response.FirstMessage())
}

// Regenerate re-answers the nearest ancestor user message on sessionID's
// active path, creating a sibling assistant branch under it instead of
// persisting a new user message. If the active leaf is itself an
// unanswered user message, this is equivalent to answering it fresh. With
// opts.OnDelta set, assistant content deltas are streamed as they arrive.
func (p *Pipeline) Regenerate(ctx context.Context, sessionID uint, opts TurnOptions) (*TurnResult, error) {
	prep, ctx, err := p.prepareTurn(ctx, sessionID, opts)
	if err != nil {
		return nil, err
	}
	if len(prep.activePath) == 0 {
		return nil, fmt.Errorf("chat: session %d has no messages to regenerate", sessionID)
	}
	prep.identity, prep.toolset = applyTurnOptions(prep.identity, prep.toolset, opts)

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

	llmContext, err := p.buildContext(prep.registry, prep.settings, pathUpToUser, prep.compRow, prep.vars)
	if err != nil {
		return nil, err
	}

	turn := newTurnContext(sessionID, llmContext, userIdx == 0, userMsg.Content)
	turn.History = append([]store.ChatMessage(nil), pathUpToUser...)
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
	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, compressPath, prep.compRow, turn)
	if err != nil {
		return nil, err
	}

	return p.runFrom(ctx, sessionID, prep.sess.ProjectID, prep.settings, prep.identity, prep.toolset, turn, &userMsg.ID, prep.sess.ActiveLeafMessageID, nil, opts.NotificationIDs, opts.OnDelta)
}

// runFrom runs converse and persists its output as a single chain parented
// at parentID. newInputMessages, when non-empty, are prepended to that chain
// and persisted as the incoming turn's injected messages plus pending user
// message (Run's case); when empty, the chain is parented directly onto an
// already-persisted user message (Regenerate's case).
func (p *Pipeline) runFrom(ctx context.Context, sessionID, projectID uint, settings store.SessionSettings, identity registry.Identity, toolset []toolkit.Tool, turn plugin.TurnContext, parentID, expectedLeaf *uint, newInputMessages []store.ChatMessage, claimedNotificationIDs []uint, onDelta func(StreamEvent)) (*TurnResult, error) {
	turn.Identity = identity
	toPersist, deliveries, notificationIDs, turn, converseErr := p.converse(ctx, settings, identity, toolset, turn, onDelta)
	notificationIDs = append(claimedNotificationIDs, notificationIDs...)
	acknowledged := false
	defer func() {
		if !acknowledged && p.notifications != nil {
			_ = p.notifications.ReleaseNotifications(notificationIDs)
		}
	}()
	if converseErr != nil {
		toPersist = incompleteMessages(toPersist, converseErr)
	}

	persistMessages := toPersist
	if len(newInputMessages) > 0 {
		persistMessages = append(append([]store.ChatMessage(nil), newInputMessages...), toPersist...)
	}
	if len(persistMessages) == 0 {
		return nil, converseErr
	}

	commit := p.notificationCommit(notificationIDs)
	saved, err := p.sessions.AppendMessagesAtLeafWithDeliveries(sessionID, projectID, parentID, expectedLeaf, persistMessages, deliveries, commit)
	if errors.Is(err, session.ErrStaleActiveLeaf) {
		// The active branch moved under us mid-turn. Keep the already
		// generated output as a reachable-but-inactive branch instead of
		// discarding it, and tell the caller the branch was not activated.
		detached, detachErr := p.sessions.AppendMessagesDetachedWithDeliveries(sessionID, projectID, parentID, persistMessages, deliveries, commit)
		if detachErr != nil {
			return nil, detachErr
		}
		acknowledged = true
		final := detached[len(detached)-1]
		turn.Messages[len(turn.Messages)-1] = final
		if turn.Metadata == nil {
			turn.Metadata = map[string]any{}
		}
		turn.Metadata["stale_active_leaf"] = true
		go p.plugins.Run(context.WithoutCancel(ctx), settings.Plugins, plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)
		return &TurnResult{Message: &final, Metadata: turn.Metadata, turn: turn}, converseErr
	}
	if err != nil {
		return nil, err
	}
	acknowledged = true
	final := saved[len(saved)-1]
	if converseErr != nil {
		turn.Messages[len(turn.Messages)-1] = final
		if turn.Metadata == nil {
			turn.Metadata = map[string]any{}
		}
		turn.Metadata["incomplete"] = true
		go p.plugins.Run(context.WithoutCancel(ctx), settings.Plugins, plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)
		return &TurnResult{Message: &final, Metadata: turn.Metadata, turn: turn}, converseErr
	}

	turn.Messages[len(turn.Messages)-1] = final
	go p.plugins.Run(context.WithoutCancel(ctx), settings.Plugins, plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, turn)

	return &TurnResult{Message: &final, Metadata: turn.Metadata, turn: turn}, nil
}

func incompleteMessages(messages []store.ChatMessage, cause error) []store.ChatMessage {
	var streamErr *llm.IncompleteResponseError
	if errors.As(cause, &streamErr) && hasMessageContent(streamErr.Message) {
		if partial, err := agent.StoreMessageFromDS4(streamErr.Message); err == nil {
			messages = append(messages, partial)
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == ds4.RoleAssistant {
			messages[index].Status = store.MessageStatusIncomplete
			// A tool_calls message with no matching tool result would make
			// the next request to the provider fail outright, so an
			// interrupted turn can only keep the assistant's visible text.
			messages[index].ToolCalls = nil
			break
		}
	}
	return messages
}

func hasMessageContent(message ds4.Message) bool {
	return message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
}

func newTurnContext(sessionID uint, messages []store.ChatMessage, isFirstTurn bool, firstUserMessage string) plugin.TurnContext {
	return plugin.TurnContext{
		SessionID:        sessionID,
		Scope:            toolkit.ScopeSession,
		Messages:         messages,
		IsFirstTurn:      isFirstTurn,
		FirstUserMessage: firstUserMessage,
		Metadata:         map[string]any{},
	}
}

// buildContext assembles the messages sent to the LLM ahead of the pending
// user message: enabled impressions (in order), then either the unpacked
// compression plus history after its coverage, or the full active path.
func (p *Pipeline) buildContext(reg *registry.Registry, settings store.SessionSettings, activePath []store.ChatMessage, compRow *store.Compression, vars ...registry.PromptVars) ([]store.ChatMessage, error) {
	out, err := p.staticContext(reg, settings, vars...)
	if err != nil {
		return nil, err
	}

	if compRow == nil {
		return append(out, activePath...), nil
	}

	var unpacked []transform.Message
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

func (p *Pipeline) staticContext(reg *registry.Registry, settings store.SessionSettings, vars ...registry.PromptVars) ([]store.ChatMessage, error) {
	var out []store.ChatMessage
	for _, name := range settings.Impressions {
		imp, ok := reg.Impressions[name]
		if !ok || !imp.Enabled {
			continue
		}
		for index, m := range imp.Messages {
			content, err := reg.RenderPrompt(m.Content, vars...)
			if err != nil {
				return nil, fmt.Errorf("chat: render impression %q message %d: %w", name, index+1, err)
			}
			out = append(out, store.ChatMessage{Role: m.Role, Content: content})
		}
	}
	return out, nil
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
func (p *Pipeline) compressIfNeeded(ctx context.Context, pluginNames []string, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, turn plugin.TurnContext) (plugin.TurnContext, error) {
	turn = p.plugins.Run(ctx, pluginNames, plugin.HookContextCompression, plugin.PhaseBefore, turn)
	turn, err := p.maybeCompress(ctx, sess, identity, activePath, compRow, turn)
	if err != nil {
		return plugin.TurnContext{}, err
	}
	return p.plugins.Run(ctx, pluginNames, plugin.HookContextCompression, plugin.PhaseAfter, turn), nil
}

func (p *Pipeline) maybeCompress(ctx context.Context, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, turn plugin.TurnContext) (plugin.TurnContext, error) {
	// A missing context window makes the trigger threshold meaningless and
	// would fire compression on every turn; skip rather than pay for an LLM
	// compaction with no sane target.
	if identity.ContextWindowTokens <= 0 {
		return turn, nil
	}
	total := 0
	for _, m := range turn.Messages {
		total += estimateMessageLength(m)
	}
	if float64(total) <= compressionTriggerRatio*float64(identity.ContextWindowTokens) {
		return turn, nil
	}
	if len(activePath) == 0 || activePath[len(activePath)-1].Role != ds4.RoleAssistant {
		return turn, nil
	}

	// Compress only history not already covered by a previous compression.
	// The prior compacted block is kept verbatim and folded into the new
	// one, so repeated compressions never re-summarize an earlier summary.
	split := 0
	if compRow != nil {
		for split < len(activePath) && activePath[split].ID <= compRow.LastMessageID {
			split++
		}
	}
	toCompress := activePath[split:]
	if len(toCompress) == 0 {
		return turn, nil
	}
	keep := turn.Messages[len(turn.Messages)-1:]

	input := make([]transform.Message, len(toCompress))
	for i, m := range toCompress {
		input[i] = transform.Message{Role: m.Role, Content: m.Content}
	}

	compacted, err := transform.Compress(ctx, p.llm, input, int(float64(total)*0.3))
	if err != nil {
		// Compression failure behaves like /stop: abort the turn, leave
		// everything as-is.
		return plugin.TurnContext{}, fmt.Errorf("chat: compression failed, aborting turn: %w", err)
	}

	// The stored block must be self-contained: prior compacted history plus
	// the newly compacted tail, covering FirstMessageID..LastMessageID.
	sequence := compacted
	if compRow != nil {
		var unpacked []transform.Message
		if err := json.Unmarshal(compRow.Messages, &unpacked); err != nil {
			return plugin.TurnContext{}, fmt.Errorf("chat: unpack compression %d: %w", compRow.ID, err)
		}
		sequence = append(unpacked, compacted...)
	}

	newMessages := make([]store.ChatMessage, 0, len(sequence)+1)
	for _, m := range sequence {
		newMessages = append(newMessages, store.ChatMessage{Role: m.Role, Content: m.Content})
	}
	newMessages = append(newMessages, keep...)

	firstMessageID := activePath[0].ID
	if compRow != nil {
		firstMessageID = compRow.FirstMessageID
	}
	sequenceJSON, err := json.Marshal(sequence)
	if err != nil {
		return plugin.TurnContext{}, fmt.Errorf("chat: marshal compression: %w", err)
	}
	if _, err := p.sessions.StoreCompression(sess.ID, firstMessageID, activePath[len(activePath)-1].ID, sequenceJSON); err != nil {
		return plugin.TurnContext{}, fmt.Errorf("chat: persist compression: %w", err)
	}

	turn.Messages = newMessages
	return turn, nil
}

// estimateMessageLength approximates a message's context length, counting
// every field the model receives (content, reasoning, tool calls) rather
// than only Content.
func estimateMessageLength(m store.ChatMessage) int {
	return transform.EstimateLength(m.Content) +
		transform.EstimateLength(m.ReasoningContent) +
		transform.EstimateLength(string(m.ToolCalls))
}

// converse runs the first LLM call and, while the model requests tool
// calls, the tool-execution loop, wrapping each stage in the corresponding
// Plugin hooks. It returns every message generated along the way (assistant
// tool-call messages, tool results, final assistant message) in
// persistence order, plus the turn context as left by the last plugin that
// ran (carrying any Metadata plugins attached, and any content mutation the
// completion hook made to the final assistant message). The reusable loop
// lives in internal/agent; this wrapper adapts the session's turn state.
func (p *Pipeline) converse(ctx context.Context, settings store.SessionSettings, identity registry.Identity, toolset []toolkit.Tool, turn plugin.TurnContext, onDelta func(StreamEvent)) ([]store.ChatMessage, []toolkit.FileDelivery, []uint, plugin.TurnContext, error) {
	var claimNotifications func() ([]agent.Notification, error)
	if p.notifications != nil {
		claimNotifications = func() ([]agent.Notification, error) {
			return p.notifications.ClaimNotifications(turn.SessionID)
		}
	}
	result, err := p.agent.Run(ctx, agent.Request{
		Identity:           identity,
		Toolset:            toolset,
		Plugins:            settings.Plugins,
		Turn:               turn,
		Scope:              toolkit.ScopeSession,
		Audit:              agent.AuditOwner{SessionID: &turn.SessionID},
		OwnerID:            turn.SessionID,
		ClaimNotifications: claimNotifications,
		OnDelta: func(event StreamEvent) {
			if onDelta != nil {
				onDelta(event)
			}
		},
		OnInteraction: func(request *interaction.Request) {
			if onDelta != nil {
				onDelta(StreamEvent{Type: interaction.EventAskPermission, Interaction: request})
			}
		},
	})
	if err != nil {
		return result.Messages, result.Deliveries, result.NotificationIDs, result.Turn, err
	}
	return result.Messages, result.Deliveries, result.NotificationIDs, result.Turn, nil
}

func (p *Pipeline) notificationCommit(ids []uint) func(*gorm.DB) error {
	if p.notifications == nil || len(ids) == 0 {
		return nil
	}
	return func(tx *gorm.DB) error {
		return p.notifications.AcknowledgeNotificationsTx(tx, ids)
	}
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

func incomingMessagesToPersist(messages []store.ChatMessage, originalContextLen int) ([]store.ChatMessage, error) {
	if originalContextLen < 0 || originalContextLen > len(messages) {
		return nil, fmt.Errorf("chat: invalid original context length %d for %d messages", originalContextLen, len(messages))
	}
	if _, err := lastUserMessage(messages); err != nil {
		return nil, err
	}
	injectedEnd := len(messages) - 1
	if originalContextLen >= injectedEnd {
		return nil, nil
	}
	return append([]store.ChatMessage(nil), messages[originalContextLen:injectedEnd]...), nil
}

func sameMessageID(left, right *uint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
