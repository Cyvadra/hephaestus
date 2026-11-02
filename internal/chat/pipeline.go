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
	"strings"
	"sync"
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
	// compressionRetainRatio is the fraction of the context window kept
	// verbatim after a compression, so recent details survive unsummarized.
	compressionRetainRatio = 0.16
	// compressionMinOlderUnits skips a summarization whose input is too small
	// to shrink meaningfully.
	compressionMinOlderUnits = 512
	// compressionManualRetainRatio bounds the verbatim tail of an explicit
	// /compact to a fraction of the history being compacted. It is capped by
	// compressionRetainRatio of the window, so a manual compaction always
	// leaves less raw history behind than the automatic trigger allows.
	compressionManualRetainRatio = 0.1
	// compressionManualMinContextRatio makes /compact a no-op below this
	// fraction of the context window when a compaction already exists on the
	// active path: there is nothing meaningful to reclaim, and a fresh
	// summarization would only cost tokens.
	compressionManualMinContextRatio = 0.15
	continuationAttempts             = 3
	continuationBridge               = "\n\n"
	postSendWorkers                  = 4
	postSendQueueSize                = 32
)

// compactionMode selects the trigger rules for one compaction attempt.
type compactionMode uint8

const (
	compactionAuto compactionMode = iota
	compactionManual
)

// compactionResult describes one compaction attempt. A skipped attempt has
// Compacted false and Reason set; Reason is empty for routine auto skips.
type compactionResult struct {
	Compacted      bool
	Reason         string
	BeforeUnits    int
	AfterUnits     int
	FirstMessageID uint
	LastMessageID  uint
	checkpoint     store.ChatMessage
	tail           []store.ChatMessage
}

// CompactResult is the public outcome of an explicit /compact request.
type CompactResult struct {
	Compacted   bool
	BeforeUnits int
	AfterUnits  int
	Reason      string
}

// Message renders the result for a slash-command response.
func (r CompactResult) Message() string {
	if !r.Compacted {
		reason := r.Reason
		if reason == "" {
			reason = "nothing to compact"
		}
		return "Compaction skipped: " + reason + "."
	}
	return fmt.Sprintf("Context compacted: %d -> %d estimated units.", r.BeforeUnits, r.AfterUnits)
}

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
	steering      SteeringSource
	postSendCtx   context.Context
	postSendStop  context.CancelFunc
	postSendQueue chan postSendTask
	postSendWG    sync.WaitGroup
}

type postSendTask struct {
	settings store.SessionSettings
	turn     plugin.TurnContext
}

type NotificationSource interface {
	ClaimNotifications(uint) ([]agent.Notification, error)
	AcknowledgeNotificationsTx(*gorm.DB, []uint) error
	ReleaseNotifications([]uint) error
}

// SteeringSource owns runtime human instructions without making the chat
// pipeline depend on a concrete chat-run implementation.
type SteeringSource interface {
	ClaimSteering(uint) (*agent.Steering, error)
	ClaimNormalSteering(uint) (*agent.Steering, error)
	ClaimAggressiveSteering(uint) (*agent.Steering, error)
	AcknowledgeSteering(uint, agent.Steering)
	ReleaseSteering(uint, agent.Steering)
}

func (p *Pipeline) SetNotificationSource(source NotificationSource) { p.notifications = source }

func (p *Pipeline) SetSteeringSource(source SteeringSource) { p.steering = source }

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
	postSendCtx, postSendStop := context.WithCancel(context.Background())
	pipeline := &Pipeline{
		db:            db,
		registries:    registries,
		toolReg:       toolReg,
		plugins:       plugins,
		llm:           llmClient,
		agent:         runner,
		sessions:      sessions,
		notify:        notifier,
		projects:      projects,
		interactions:  interactions,
		postSendCtx:   postSendCtx,
		postSendStop:  postSendStop,
		postSendQueue: make(chan postSendTask, postSendQueueSize),
	}
	for range postSendWorkers {
		pipeline.postSendWG.Add(1)
		go pipeline.runPostSendWorker()
	}
	return pipeline
}

func (p *Pipeline) runPostSendWorker() {
	defer p.postSendWG.Done()
	for {
		select {
		case <-p.postSendCtx.Done():
			return
		case task := <-p.postSendQueue:
			if p.postSendCtx.Err() != nil {
				return
			}
			p.plugins.Run(p.postSendCtx, task.settings.Plugins, plugin.HookAssistantMessageSent2User, plugin.PhaseAfter, task.turn)
		}
	}
}

// Shutdown stops queued post-send work and waits for running handlers until
// ctx expires. A non-cooperative plugin may outlive the deadline, but it is
// still bounded by the plugin registry's handler budget.
func (p *Pipeline) Shutdown(ctx context.Context) error {
	p.postSendStop()
	done := make(chan struct{})
	go func() {
		p.postSendWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pipeline) enqueuePostSend(settings store.SessionSettings, turn plugin.TurnContext) {
	select {
	case <-p.postSendCtx.Done():
		p.notify.Warn("chat: skip post-send hook for session %d during shutdown", turn.SessionID)
	case p.postSendQueue <- postSendTask{settings: settings, turn: turn}:
	default:
		p.notify.Warn("chat: skip post-send hook for session %d because the queue is full", turn.SessionID)
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
	toolset = filterChildSessionTools(prep.sess, toolset)
	prep.toolset = toolset

	proj, err := p.projects.Get(prep.sess.ProjectID)
	if err != nil {
		p.notify.Error("chat: session %d has missing project id %d; message not persisted", sessionID, prep.sess.ProjectID)
		return prep, fmt.Errorf("chat: project %d not found", prep.sess.ProjectID)
	}
	prep.workspace = p.projects.Path(*proj)
	prep.vars = promptVars(proj, prep.workspace, prep.sess, time.Now())
	prep.identity, err = prep.registry.RenderIdentity(prep.registry.Identities[settings.Identity], prep.vars)
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

func filterChildSessionTools(sess store.Session, toolset []toolkit.Tool) []toolkit.Tool {
	if sess.ParentSubagentRunID == nil {
		return toolset
	}
	kept := make([]toolkit.Tool, 0, len(toolset))
	for _, tool := range toolset {
		if _, delegating := tool.(toolkit.Delegating); !delegating {
			kept = append(kept, tool)
		}
	}
	return kept
}

// promptVars builds the template variables shared by every turn and by
// ephemeral side questions.
func promptVars(proj *store.Project, workspace string, sess store.Session, now time.Time) registry.PromptVars {
	vars := registry.TimePromptVars(now)
	vars["project"] = proj.Name
	vars["workspace"] = workspace
	vars["session_id"] = strconv.FormatUint(uint64(sess.ID), 10)
	vars["session_title"] = sess.Title
	return vars
}

// sanitizeSettings repairs stale registry references in memory only. dirty
// reports whether the result differs from the session's stored settings.
func (p *Pipeline) sanitizeSettings(reg *registry.Registry, sess store.Session) (store.SessionSettings, bool, error) {
	settings := sess.Settings.Data()
	dirty := false

	if _, ok := reg.Identities[settings.Identity]; !ok {
		fallback := reg.DefaultIdentityName()
		if fallback == "" {
			return settings, false, fmt.Errorf("chat: session %d references missing identity %q and no fallback identity exists", sess.ID, settings.Identity)
		}
		settings.Identity = fallback
		dirty = true
	}
	settings.Impressions = keepRegistered(settings.Impressions, reg.Impressions, &dirty)
	settings.ToolGroups = keepRegistered(settings.ToolGroups, reg.ToolGroups, &dirty)
	settings.Plugins = keepIf(settings.Plugins, p.plugins.Has, &dirty)
	if concierge, ok := reg.Concierges[sess.SourceConcierge]; ok {
		settings.ToolGroups = keepAllowed(settings.ToolGroups, concierge.ToolGroups, &dirty)
		settings.Plugins = keepAllowed(settings.Plugins, concierge.Plugins, &dirty)
	}
	return settings, dirty, nil
}

// resolveSettings sanitizes a session's settings against the current
// registry, self-healing stale references: a missing identity falls back to
// the registry's default, and unknown impressions, tool groups and plugins
// are dropped. Repaired settings are persisted so the session converges to
// a healthy state instead of failing every turn.
func (p *Pipeline) resolveSettings(sess *store.Session) (store.SessionSettings, error) {
	settings, dirty, err := p.sanitizeSettings(p.registries.Current(), *sess)
	if err != nil {
		return settings, err
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

// keepIf returns the names accepted by keep, setting dirty when any were
// dropped.
func keepIf(names []string, keep func(string) bool, dirty *bool) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if keep(name) {
			out = append(out, name)
		} else {
			*dirty = true
		}
	}
	return out
}

func keepAllowed(names, available []string, dirty *bool) []string {
	allowed := make(map[string]struct{}, len(available))
	for _, name := range available {
		allowed[name] = struct{}{}
	}
	return keepIf(names, func(name string) bool { _, ok := allowed[name]; return ok }, dirty)
}

func keepRegistered[T any](names []string, known map[string]T, dirty *bool) []string {
	return keepIf(names, func(name string) bool { _, ok := known[name]; return ok }, dirty)
}

// TurnOptions carries optional per-turn settings, an optional branch to
// continue from, and a caller-provided optimistic concurrency guard. Branch
// selection is read-only until the turn is committed.
type TurnOptions struct {
	ExpectedLeaf      *uint
	SelectedLeaf      *uint
	SelectRoot        bool
	UploadAttachments []store.MessageAttachment
	OnDelta           func(StreamEvent)
	ReasoningEffort   string
	DisabledTools     []string
	NotificationIDs   []uint
}

// CompleteConfiguration generates a reference assistant response for a
// configuration editor without creating a session turn or running tools.
func (p *Pipeline) CompleteConfiguration(ctx context.Context, identity registry.Identity, messages []registry.Message, userText string, onDelta func(string)) (string, error) {
	if strings.TrimSpace(userText) == "" {
		return "", errors.New("chat: completion user message is empty")
	}
	rendered, err := p.registries.Current().RenderIdentity(identity, registry.TimePromptVars(time.Now()))
	if err != nil {
		return "", err
	}
	contextMessages := make([]store.ChatMessage, 0, len(messages)+1)
	for index, message := range messages {
		if !validConfigurationMessageRole(message.Role) {
			return "", fmt.Errorf("chat: completion message %d has invalid role %q", index+1, message.Role)
		}
		renderedContent, renderErr := p.registries.Current().RenderPrompt(message.Content, registry.TimePromptVars(time.Now()))
		if renderErr != nil {
			return "", fmt.Errorf("chat: render completion message %d: %w", index+1, renderErr)
		}
		contextMessages = append(contextMessages, store.ChatMessage{Role: message.Role, Content: renderedContent})
	}
	contextMessages = append(contextMessages, store.ChatMessage{Role: ds4.RoleUser, Content: userText})
	response, err := p.llm.CallStream(ctx, rendered, contextMessages, nil, func(delta llm.StreamDelta) {
		if onDelta != nil && delta.Content != "" {
			onDelta(delta.Content)
		}
	})
	if err != nil {
		return "", err
	}
	return response.Content(), nil
}

func validConfigurationMessageRole(role string) bool {
	switch role {
	case ds4.RoleSystem, ds4.RoleUser, ds4.RoleAssistant:
		return true
	default:
		return false
	}
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
func (p *Pipeline) prepareTurn(ctx context.Context, sessionID uint) (turnPrep, context.Context, error) {
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
// user/assistant/tool messages as a single transaction. When an interrupted
// LLM stream leaves a persistable assistant response, Run returns it with
// Metadata["incomplete"] set instead of an error. With opts.OnDelta set,
// assistant content deltas are streamed as they arrive
// while persistence still only happens once the turn completes.
func (p *Pipeline) Run(ctx context.Context, sessionID uint, userText string, opts TurnOptions) (*TurnResult, error) {
	prep, ctx, err := p.prepareTurn(ctx, sessionID)
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
	if opts.SelectRoot || opts.SelectedLeaf != nil {
		// prepareTurn resolved the compression against the old active path.
		// Resolve it again for the selected branch so buildContext never
		// replays another branch's checkpoint.
		prep.compRow, err = p.sessions.ResolveCompression(&prep.sess, prep.activePath)
		if err != nil {
			return nil, err
		}
	}
	prep.identity, prep.toolset = applyTurnOptions(prep.identity, prep.toolset, opts)

	static, llmContext, err := p.buildContextParts(prep.registry, prep.settings, prep.activePath, prep.compRow, prep.vars)
	if err != nil {
		return nil, err
	}

	pendingUser := store.ChatMessage{Role: ds4.RoleUser, Content: userText, Timestamp: time.Now(), Attachments: append([]store.MessageAttachment(nil), opts.UploadAttachments...)}
	turn := newTurnContext(sessionID, append(llmContext, pendingUser), len(prep.activePath) == 0, userText)
	turn.Identity = prep.identity
	turn.History = append(append([]store.ChatMessage(nil), prep.activePath...), pendingUser)
	turn = p.plugins.Run(ctx, prep.settings.Plugins, plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	incomingPersistMessages, err := incomingMessagesToPersist(turn.Messages, len(llmContext))
	if err != nil {
		return nil, err
	}

	// Replacing [len(static), len(llmContext)) keeps the impression prefix and
	// any messages HookUserMessageIncoming inserted before the pending user
	// message while swapping only the replayed history for the checkpoint.
	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, prep.activePath, prep.compRow, turn, len(static), len(llmContext))
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

// Continue expands the assistant response at the active session leaf. The
// result is persisted as a complete-content sibling of the prefix, preserving
// the original response as a selectable branch.
func (p *Pipeline) Continue(ctx context.Context, sessionID, messageID uint, opts TurnOptions) (*TurnResult, error) {
	prep, ctx, err := p.prepareTurn(ctx, sessionID)
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
	if prefix.ID != messageID || prefix.Role != ds4.RoleAssistant || strings.TrimSpace(prefix.Content) == "" || len(prefix.ToolCalls) > 0 {
		return nil, fmt.Errorf("chat: message %d is not a continuable assistant response", messageID)
	}

	contextMessages, err := p.buildContext(prep.registry, prep.settings, prep.activePath[:len(prep.activePath)-1], prep.compRow, prep.vars)
	if err != nil {
		return nil, err
	}
	response, err := p.continueResponse(ctx, prep.identity, contextMessages, prefix, opts.OnDelta)
	if err != nil {
		if response.Content == "" {
			return nil, err
		}
		if _, saveErr := p.sessions.ContinueAssistantAtLeaf(sessionID, messageID, messageID, prefix.Content+response.Content, store.MessageStatusIncomplete); saveErr != nil {
			return nil, saveErr
		}
		return nil, err
	}
	if response.Content == "" {
		return nil, errors.New("chat: continuation returned no content")
	}
	saved, err := p.sessions.ContinueAssistantAtLeaf(sessionID, messageID, messageID, prefix.Content+response.Content, store.MessageStatusComplete)
	if err != nil {
		return nil, err
	}
	return &TurnResult{Message: saved, Metadata: map[string]any{}}, nil
}

func (p *Pipeline) continueResponse(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, prefix store.ChatMessage, onDelta func(StreamEvent)) (store.ChatMessage, error) {
	for attempt := 0; attempt < continuationAttempts; attempt++ {
		requestPrefix := prefix
		requestPrefix.Content += continuationBridge
		emittedContent := false
		response, err := p.llm.ContinueStream(ctx, identity, messages, requestPrefix, func(delta llm.StreamDelta) {
			if onDelta != nil && delta.Content != "" {
				text := delta.Content
				if !emittedContent {
					text = continuationBridge + text
					emittedContent = true
				}
				onDelta(StreamEvent{Type: "delta", Text: text})
			}
		})
		if err != nil {
			var incomplete *llm.IncompleteResponseError
			if errors.As(err, &incomplete) && incomplete.Message.Content != "" {
				partial, convertErr := agent.StoreMessageFromDS4(incomplete.Message)
				if convertErr == nil {
					partial.Content = continuationBridge + partial.Content
					partial.ReasoningContent = ""
					partial.Status = store.MessageStatusIncomplete
					return partial, err
				}
			}
			return store.ChatMessage{}, err
		}
		message, convertErr := agent.StoreMessageFromDS4(*response.FirstMessage())
		if convertErr != nil {
			return store.ChatMessage{}, convertErr
		}
		if message.Content != "" {
			message.Content = continuationBridge + message.Content
			return message, nil
		}
	}
	return store.ChatMessage{}, errors.New("chat: continuation returned no content")
}

// Regenerate re-answers the nearest ancestor user message on sessionID's
// active path, creating a sibling assistant branch under it instead of
// persisting a new user message. If the active leaf is itself an
// unanswered user message, this is equivalent to answering it fresh. With
// opts.OnDelta set, assistant content deltas are streamed as they arrive.
func (p *Pipeline) Regenerate(ctx context.Context, sessionID uint, opts TurnOptions) (*TurnResult, error) {
	prep, ctx, err := p.prepareTurn(ctx, sessionID)
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

	// A checkpoint may cover the message being re-answered: compression
	// boundaries can land on any message, including that one. Keep the
	// checkpoint as prior context but clamp its boundary so buildContextParts
	// replays the raw prompt verbatim; otherwise the turn would ask the model
	// to answer the checkpoint itself.
	contextComp := compressionForRegenerate(prep.compRow, pathUpToUser)

	static, llmContext, err := p.buildContextParts(prep.registry, prep.settings, pathUpToUser, contextComp, prep.vars)
	if err != nil {
		return nil, err
	}

	turn := newTurnContext(sessionID, llmContext, userIdx == 0, userMsg.Content)
	turn.History = append([]store.ChatMessage(nil), pathUpToUser...)
	// Compression on the regenerate path covers history up to (but not
	// including) the user message being regenerated from: that message must
	// stay in context, and it is what the regenerated reply re-answers.
	// Passing the path without the user message keeps that message out of the
	// compressed span, matching maybeCompress's assumption that the kept last
	// message is what the reply answers. New coverage always starts at the
	// previous coverage boundary (or the path start), so LastMessageID moves
	// forward.
	compressPath := pathUpToUser[:userIdx]
	// The regenerated user message is the final turn message and stays raw.
	turn, err = p.compressIfNeeded(ctx, prep.settings.Plugins, prep.sess, prep.identity, compressPath, contextComp, turn, len(static), len(turn.Messages)-1)
	if err != nil {
		return nil, err
	}

	return p.runFrom(ctx, sessionID, prep.sess.ProjectID, prep.settings, prep.identity, prep.toolset, turn, &userMsg.ID, prep.sess.ActiveLeafMessageID, nil, opts.NotificationIDs, opts.OnDelta)
}

// compressionForRegenerate clamps a compression row so it never swallows the
// final message of pathUpToUser, the user message a regenerated reply
// answers. buildContextParts keeps only messages past LastMessageID, so an
// unclamped row whose coverage reaches that message (or a later one) would
// replay only the checkpoint. Clamping keeps the checkpoint's content while
// replaying the raw prompt verbatim. The copy is local to the turn and is
// never persisted.
func compressionForRegenerate(compRow *store.Compression, pathUpToUser []store.ChatMessage) *store.Compression {
	if compRow == nil || len(pathUpToUser) == 0 {
		return compRow
	}
	promptID := pathUpToUser[len(pathUpToUser)-1].ID
	if compRow.LastMessageID < promptID {
		return compRow
	}
	clamped := *compRow
	clamped.LastMessageID = 0
	if len(pathUpToUser) > 1 {
		clamped.LastMessageID = pathUpToUser[len(pathUpToUser)-2].ID
	}
	return &clamped
}

// runFrom runs converse and persists its output as a single chain parented
// at parentID. newInputMessages, when non-empty, are prepended to that chain
// and persisted as the incoming turn's injected messages plus pending user
// message (Run's case); when empty, the chain is parented directly onto an
// already-persisted user message (Regenerate's case).
func (p *Pipeline) runFrom(ctx context.Context, sessionID, projectID uint, settings store.SessionSettings, identity registry.Identity, toolset []toolkit.Tool, turn plugin.TurnContext, parentID, expectedLeaf *uint, newInputMessages []store.ChatMessage, claimedNotificationIDs []uint, onDelta func(StreamEvent)) (*TurnResult, error) {
	turn.Identity = identity
	toPersist, deliveries, notificationIDs, claimedSteering, turn, converseErr := p.converse(ctx, settings, identity, toolset, turn, onDelta)
	notificationIDs = append(claimedNotificationIDs, notificationIDs...)
	acknowledged := false
	defer func() {
		if !acknowledged && p.notifications != nil {
			_ = p.notifications.ReleaseNotifications(notificationIDs)
		}
		if !acknowledged && p.steering != nil {
			if runID, ok := toolkit.ChatRunIDFromContext(ctx); ok {
				for _, value := range claimedSteering {
					p.steering.ReleaseSteering(runID, value)
				}
			}
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
	staleLeaf := errors.Is(err, session.ErrStaleActiveLeaf)
	if staleLeaf {
		// The active branch moved under us mid-turn. Keep the already
		// generated output as a reachable-but-inactive branch instead of
		// discarding it, and tell the caller the branch was not activated.
		saved, err = p.sessions.AppendMessagesDetachedWithDeliveries(sessionID, projectID, parentID, persistMessages, deliveries, commit)
	}
	if err != nil {
		return nil, err
	}
	acknowledged = true
	p.acknowledgeSteering(ctx, claimedSteering)
	final := saved[len(saved)-1]
	markTurn := func(key string) {
		if turn.Metadata == nil {
			turn.Metadata = map[string]any{}
		}
		turn.Metadata[key] = true
	}
	returnErr := converseErr
	switch {
	case staleLeaf:
		markTurn("stale_active_leaf")
	case converseErr != nil:
		if final.Role != ds4.RoleAssistant {
			return &TurnResult{Message: &final, Metadata: turn.Metadata, turn: turn}, converseErr
		}
		// The partial assistant reply was persisted; surface it as a
		// completed-but-incomplete turn rather than as a failure.
		markTurn("incomplete")
		returnErr = nil
	}
	turn.Messages[len(turn.Messages)-1] = final
	p.enqueuePostSend(settings, turn)
	return &TurnResult{Message: &final, Metadata: turn.Metadata, turn: turn}, returnErr
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
			// Keep completed tool exchanges replayable. Only a tool_calls
			// message without all matching results is unsafe to retain.
			if !hasMatchingToolResults(messages[index], messages[index+1:]) {
				messages[index].ToolCalls = nil
			}
			break
		}
	}
	return messages
}

func hasMatchingToolResults(assistant store.ChatMessage, following []store.ChatMessage) bool {
	if len(assistant.ToolCalls) == 0 {
		return true
	}
	var calls []ds4.ToolCall
	if err := json.Unmarshal(assistant.ToolCalls, &calls); err != nil || len(calls) == 0 {
		return false
	}
	results := make(map[string]struct{}, len(following))
	for _, message := range following {
		if message.Role == ds4.RoleTool {
			results[message.ToolCallID] = struct{}{}
		}
	}
	for _, call := range calls {
		if _, ok := results[call.ID]; !ok {
			return false
		}
	}
	return true
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
	_, messages, err := p.buildContextParts(reg, settings, activePath, compRow, vars...)
	return messages, err
}

// buildContextParts is buildContext plus the impression prefix it rendered.
// Compression callers need the prefix length to replace only the history
// portion of a turn's messages.
func (p *Pipeline) buildContextParts(reg *registry.Registry, settings store.SessionSettings, activePath []store.ChatMessage, compRow *store.Compression, vars ...registry.PromptVars) ([]store.ChatMessage, []store.ChatMessage, error) {
	static, err := p.staticContext(reg, settings, vars...)
	if err != nil {
		return nil, nil, err
	}
	out := append([]store.ChatMessage(nil), static...)

	if compRow == nil {
		out = append(out, activePath...)
		return static, out, nil
	}

	var unpacked []transform.Message
	if err := json.Unmarshal(compRow.Messages, &unpacked); err != nil {
		return nil, nil, fmt.Errorf("chat: unpack compression %d: %w", compRow.ID, err)
	}
	for _, m := range unpacked {
		out = append(out, store.ChatMessage{Role: m.Role, Content: m.Content})
	}

	for _, m := range activePath {
		if m.ID > compRow.LastMessageID {
			out = append(out, m)
		}
	}
	return static, out, nil
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
// older part of the active path with one checkpoint while retaining its
// newest messages verbatim. Static context and the pending user message are
// deliberately kept out of the compression cache.
//
// historyStart and historyEnd bound the turn messages that compression may
// replace: everything before historyStart (the impression prefix) and from
// historyEnd onward (HookUserMessageIncoming insertions and the pending user
// message) is preserved verbatim.
//
// Unlike every other hook, the decision and the actual LLM-driven
// compaction stay pipeline-owned rather than delegated to a
// Registry-dispatched Plugin: per the design doc, a failed compression call
// must abort the whole turn exactly like /stop, which the generic "skip a
// failing plugin and continue" Plugin contract cannot express. A checkpoint
// that does not shrink its input is a harmless no-op skip, not a failure.
func (p *Pipeline) compressIfNeeded(ctx context.Context, pluginNames []string, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, turn plugin.TurnContext, historyStart, historyEnd int) (plugin.TurnContext, error) {
	turn = p.plugins.Run(ctx, pluginNames, plugin.HookContextCompression, plugin.PhaseBefore, turn)
	turn, err := p.maybeCompress(ctx, sess, identity, activePath, compRow, turn, historyStart, historyEnd)
	if err != nil {
		return plugin.TurnContext{}, err
	}
	return p.plugins.Run(ctx, pluginNames, plugin.HookContextCompression, plugin.PhaseAfter, turn), nil
}

func (p *Pipeline) maybeCompress(ctx context.Context, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, turn plugin.TurnContext, historyStart, historyEnd int) (plugin.TurnContext, error) {
	total := 0
	for _, m := range turn.Messages {
		total += estimateMessageLength(m)
	}
	result, err := p.compactHistory(ctx, sess, identity, activePath, compRow, total, compactionAuto)
	if err != nil {
		return plugin.TurnContext{}, err
	}
	if !result.Compacted {
		return turn, nil
	}
	if historyStart < 0 || historyEnd < historyStart || historyEnd > len(turn.Messages) {
		return plugin.TurnContext{}, fmt.Errorf("chat: invalid compression window %d..%d for %d messages", historyStart, historyEnd, len(turn.Messages))
	}

	newMessages := make([]store.ChatMessage, 0, historyStart+1+len(result.tail)+len(turn.Messages)-historyEnd)
	newMessages = append(newMessages, turn.Messages[:historyStart]...)
	newMessages = append(newMessages, result.checkpoint)
	newMessages = append(newMessages, result.tail...)
	newMessages = append(newMessages, turn.Messages[historyEnd:]...)
	turn.Messages = newMessages
	return turn, nil
}

// Compact runs one explicit compaction of sessionID's active history,
// independent of the automatic 80%-of-window trigger. It still skips when the
// context is small and a previous compaction covers it, and when the
// uncovered history cannot shrink meaningfully.
func (p *Pipeline) Compact(ctx context.Context, sessionID uint) (CompactResult, error) {
	prep, _, err := p.prepareTurn(ctx, sessionID)
	if err != nil {
		return CompactResult{}, err
	}
	_, current, err := p.buildContextParts(prep.registry, prep.settings, prep.activePath, prep.compRow, prep.vars)
	if err != nil {
		return CompactResult{}, err
	}
	total := 0
	for _, m := range current {
		total += estimateMessageLength(m)
	}
	result, err := p.compactHistory(ctx, prep.sess, prep.identity, prep.activePath, prep.compRow, total, compactionManual)
	if err != nil {
		return CompactResult{}, err
	}
	return CompactResult{
		Compacted:   result.Compacted,
		BeforeUnits: result.BeforeUnits,
		AfterUnits:  result.AfterUnits,
		Reason:      result.Reason,
	}, nil
}

// compactHistory applies the compression policy and, when it fires, replaces
// the older part of the active path with one checkpoint and persists the
// coverage. contextUnits is the estimated size of the complete request
// context, including static impressions and any trailing pending message.
func (p *Pipeline) compactHistory(ctx context.Context, sess store.Session, identity registry.Identity, activePath []store.ChatMessage, compRow *store.Compression, contextUnits int, mode compactionMode) (compactionResult, error) {
	result := compactionResult{BeforeUnits: contextUnits}
	// A missing context window makes the trigger threshold meaningless and
	// would fire compression on every turn; skip rather than pay for an LLM
	// compaction with no sane target.
	if identity.ContextWindowTokens <= 0 {
		result.Reason = "the current identity has no context window configured"
		return result, nil
	}
	switch mode {
	case compactionAuto:
		if float64(contextUnits) <= compressionTriggerRatio*float64(identity.ContextWindowTokens) {
			return result, nil
		}
		// Coverage boundaries are stable only when the turn ended on an
		// assistant message; a trailing user or tool message is left alone.
		if len(activePath) == 0 || activePath[len(activePath)-1].Role != ds4.RoleAssistant {
			return result, nil
		}
	case compactionManual:
		if len(activePath) == 0 {
			result.Reason = "this session has no history to compact"
			return result, nil
		}
		if compRow != nil && float64(contextUnits) < compressionManualMinContextRatio*float64(identity.ContextWindowTokens) {
			result.Reason = fmt.Sprintf("the context is %d estimated units and a recent compaction already covers it", contextUnits)
			return result, nil
		}
	}

	// Compress only history not already covered by a previous compression.
	// The prior checkpoint is merged into the new one, so repeated
	// compressions never accumulate blocks.
	split, priorApplies := 0, false
	if compRow != nil {
		if index, ok := compressionSplitIndex(activePath, compRow.LastMessageID); ok {
			split, priorApplies = index, true
		}
	}
	toCompress := activePath[split:]
	if len(toCompress) == 0 {
		result.Reason = "the latest compaction already covers all history"
		return result, nil
	}
	toCompressUnits := 0
	for _, m := range toCompress {
		toCompressUnits += estimateMessageLength(m)
	}

	// Keep the newest part of the uncovered history verbatim.
	retainUnits := int(compressionRetainRatio * float64(identity.ContextWindowTokens))
	if mode == compactionManual {
		if manual := int(compressionManualRetainRatio * float64(toCompressUnits)); manual < retainUnits {
			retainUnits = manual
		}
	}
	tailStart := selectRetainedTail(toCompress, retainUnits)
	tailStart = alignTailToToolBoundary(toCompress, tailStart)
	older, tail := toCompress[:tailStart], toCompress[tailStart:]
	if len(older) == 0 {
		result.Reason = "there is no older history beyond the retained tail"
		return result, nil
	}
	olderUnits := 0
	for _, m := range older {
		olderUnits += estimateMessageLength(m)
	}
	if olderUnits < compressionMinOlderUnits {
		result.Reason = fmt.Sprintf("only %d estimated units are eligible, below the %d-unit minimum", olderUnits, compressionMinOlderUnits)
		return result, nil
	}

	var prior []transform.Message
	priorLength := 0
	if priorApplies {
		if err := json.Unmarshal(compRow.Messages, &prior); err != nil {
			return compactionResult{}, fmt.Errorf("chat: unpack compression %d: %w", compRow.ID, err)
		}
		for _, m := range prior {
			priorLength += transform.EstimateLength(m.Content)
		}
	}

	input := make([]transform.CompressMessage, len(older))
	summarizableUnits := 0
	for i, m := range older {
		input[i] = transform.CompressMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  string(m.ToolCalls),
			ToolCallID: m.ToolCallID,
			Reasoning:  m.ReasoningContent,
		}
		summarizableUnits += transform.EstimateLength(m.Content) + transform.EstimateLength(string(m.ToolCalls))
	}

	// The checkpoint replaces the prior checkpoint and this span together, so
	// target one third of their combined size. The target counts only what the
	// summarizer sees (content and tool calls); the shrink guard separately
	// counts reasoning, which is replayed to the model and freed on replacement.
	checkpoint, err := transform.Compress(ctx, p.llm, prior, input, (priorLength+summarizableUnits)/3)
	if errors.Is(err, transform.ErrCompressionNotSmaller) {
		// The model could not beat the history it would replace. Keep the
		// turn uncompressed rather than aborting; the auto trigger fires at
		// 80% of the window, so the request still fits.
		result.Reason = "the model returned a checkpoint that does not shrink the history"
		return result, nil
	}
	if err != nil {
		// Compression failure behaves like /stop: abort the turn, leave
		// everything as-is.
		return compactionResult{}, fmt.Errorf("chat: compression failed: %w", err)
	}

	firstMessageID := activePath[0].ID
	if priorApplies && compRow.FirstMessageID != 0 {
		firstMessageID = compRow.FirstMessageID
	}
	sequenceJSON, err := json.Marshal([]transform.Message{checkpoint})
	if err != nil {
		return compactionResult{}, fmt.Errorf("chat: marshal compression: %w", err)
	}
	lastMessageID := older[len(older)-1].ID
	if _, err := p.sessions.StoreCompression(sess.ID, firstMessageID, lastMessageID, sequenceJSON); err != nil {
		return compactionResult{}, fmt.Errorf("chat: persist compression: %w", err)
	}

	tailUnits := 0
	for _, m := range tail {
		tailUnits += estimateMessageLength(m)
	}
	result.Compacted = true
	result.FirstMessageID = firstMessageID
	result.LastMessageID = lastMessageID
	result.checkpoint = store.ChatMessage{Role: checkpoint.Role, Content: checkpoint.Content}
	result.tail = tail
	result.AfterUnits = contextUnits - priorLength - toCompressUnits +
		transform.EstimateLength(checkpoint.Content) + tailUnits
	return result, nil
}

// compressionSplitIndex returns the index just past lastID in activePath, or
// false when lastID is not on the path.
func compressionSplitIndex(activePath []store.ChatMessage, lastID uint) (int, bool) {
	for i, m := range activePath {
		if m.ID == lastID {
			return i + 1, true
		}
	}
	return 0, false
}

// selectRetainedTail returns the index where the newest suffix of messages
// reaches retainUnits estimated units; len(messages) when the budget is
// non-positive.
func selectRetainedTail(messages []store.ChatMessage, retainUnits int) int {
	if retainUnits <= 0 {
		return len(messages)
	}
	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		accumulated += estimateMessageLength(messages[i])
		if accumulated >= retainUnits {
			return i
		}
	}
	return 0
}

// alignTailToToolBoundary moves a cut that starts on a tool result back over
// the consecutive tool-result run to the assistant that issued the calls.
// It returns 0 when that would leave tool results without their call.
func alignTailToToolBoundary(messages []store.ChatMessage, start int) int {
	if start <= 0 || start >= len(messages) || messages[start].Role != ds4.RoleTool {
		return start
	}
	firstResult := start
	for firstResult > 0 && messages[firstResult-1].Role == ds4.RoleTool {
		firstResult--
	}
	if firstResult == 0 || messages[firstResult-1].Role != ds4.RoleAssistant {
		return 0
	}
	return firstResult - 1
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
func (p *Pipeline) converse(ctx context.Context, settings store.SessionSettings, identity registry.Identity, toolset []toolkit.Tool, turn plugin.TurnContext, onDelta func(StreamEvent)) ([]store.ChatMessage, []toolkit.FileDelivery, []uint, []agent.Steering, plugin.TurnContext, error) {
	var claimNotifications func() ([]agent.Notification, error)
	if p.notifications != nil {
		claimNotifications = func() ([]agent.Notification, error) {
			return p.notifications.ClaimNotifications(turn.SessionID)
		}
	}
	var claimSteering func() (*agent.Steering, error)
	var claimNormalSteering func() (*agent.Steering, error)
	var claimAggressiveSteering func() (*agent.Steering, error)
	var releaseSteering func(agent.Steering)
	if p.steering != nil {
		if runID, ok := toolkit.ChatRunIDFromContext(ctx); ok {
			claimSteering = func() (*agent.Steering, error) { return p.steering.ClaimSteering(runID) }
			claimNormalSteering = func() (*agent.Steering, error) { return p.steering.ClaimNormalSteering(runID) }
			claimAggressiveSteering = func() (*agent.Steering, error) { return p.steering.ClaimAggressiveSteering(runID) }
			releaseSteering = func(value agent.Steering) { p.steering.ReleaseSteering(runID, value) }
		}
	}
	result, err := p.agent.Run(ctx, agent.Request{
		Identity:                identity,
		Toolset:                 toolset,
		Plugins:                 settings.Plugins,
		Turn:                    turn,
		Scope:                   toolkit.ScopeSession,
		Audit:                   agent.AuditOwner{SessionID: &turn.SessionID},
		OwnerID:                 turn.SessionID,
		ClaimNotifications:      claimNotifications,
		ClaimSteering:           claimSteering,
		ClaimNormalSteering:     claimNormalSteering,
		ClaimAggressiveSteering: claimAggressiveSteering,
		ReleaseSteering:         releaseSteering,
		// Always stream so the agent takes the CallStream path even when
		// no listener is attached.
		OnDelta: func(event StreamEvent) {
			if onDelta != nil {
				onDelta(event)
			}
		},
		OnInteraction: func(event interaction.Event) {
			if onDelta != nil {
				onDelta(StreamEvent{Type: event.Type, Interaction: &event.Request})
			}
		},
	})
	return result.Messages, result.Deliveries, result.NotificationIDs, result.Steering, result.Turn, err
}

// RespondQuestions delivers structured user answers to the pending session
// interaction. It is used by the Web-only response endpoint.
func (p *Pipeline) RespondQuestions(sessionID uint, requestID uint64, answers []interaction.Answer) error {
	if p.interactions == nil {
		return fmt.Errorf("questions are unavailable")
	}
	return p.interactions.RespondQuestions(sessionID, requestID, answers)
}

func (p *Pipeline) notificationCommit(ids []uint) func(*gorm.DB) error {
	if p.notifications == nil || len(ids) == 0 {
		return nil
	}
	return func(tx *gorm.DB) error {
		return p.notifications.AcknowledgeNotificationsTx(tx, ids)
	}
}

func (p *Pipeline) acknowledgeSteering(ctx context.Context, values []agent.Steering) {
	if p.steering == nil {
		return
	}
	runID, ok := toolkit.ChatRunIDFromContext(ctx)
	if !ok {
		return
	}
	for _, value := range values {
		p.steering.AcknowledgeSteering(runID, value)
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
