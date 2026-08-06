// Package plugin implements the platform's side-channel extension points.
// Plugins are Go code only (no config files), always block the pipeline,
// and always run under a per-plugin timeout; a failing or timed-out plugin
// is skipped silently (with a WeCom notification) rather than aborting the
// session.
package plugin

import (
	"context"
	"time"

	"github.com/Cyvadra/hephaestus/internal/store"
)

// Hook identifies a point in the session pipeline a Plugin can observe.
type Hook string

const (
	HookUserMessageIncoming        Hook = "user_message_incoming" // after only
	HookToolCall                   Hook = "tool_call"
	HookContextCompression         Hook = "context_compression"
	HookAssistantFirstCallLLM      Hook = "assistant_first_call_llm"
	HookAssistantContinuousCallLLM Hook = "assistant_continuous_call_llm"
	HookAssistantMessageCompletion Hook = "assistant_message_completion"
	HookAssistantMessageSent2User  Hook = "assistant_message_sent_to_user"
)

// Phase marks whether a hook fires before or after the stage it names.
type Phase string

const (
	PhaseBefore Phase = "before"
	PhaseAfter  Phase = "after"
)

// TurnContext is the mutable state passed through the plugin chain for a
// single hook invocation. Plugins receive it by value and return a
// (possibly modified) copy; they never see a pointer into the pipeline's
// own state.
type TurnContext struct {
	SessionID uint
	Messages  []store.ChatMessage
	// Metadata carries hook-specific or plugin-specific data (e.g. the
	// pending tool call, the outbound text about to be sent) without
	// forcing every hook to share one rigid schema.
	Metadata map[string]any
}

// Plugin is a single side-channel extension. Implementations register
// themselves in Go code; Plugin has no static config file.
type Plugin interface {
	// Name uniquely identifies the plugin for /list, /activate, /deactivate
	// and Concierge.Plugins references.
	Name() string
	// Timeout bounds how long Handle may run before it is cancelled and
	// treated as a failure.
	Timeout() time.Duration
	// Handle observes or modifies turn at the given hook/phase. Returning
	// an error (or letting ctx expire) causes this plugin's changes to be
	// discarded for this invocation; the chain continues unaffected.
	Handle(ctx context.Context, hook Hook, phase Phase, turn TurnContext) (TurnContext, error)
}
