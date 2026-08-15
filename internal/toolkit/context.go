package toolkit

import (
	"context"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/store"
)

type outputReporterContextKey struct{}

// WithOutputReporter attaches an ephemeral output sink for tools that can
// expose incremental execution progress. Reported data is not persisted by
// the toolkit.
func WithOutputReporter(ctx context.Context, reporter func(string)) context.Context {
	return context.WithValue(ctx, outputReporterContextKey{}, reporter)
}

// ReportOutput forwards an incremental tool output chunk when the caller
// supplied a reporter.
func ReportOutput(ctx context.Context, chunk string) {
	if reporter, ok := ctx.Value(outputReporterContextKey{}).(func(string)); ok && reporter != nil {
		reporter(chunk)
	}
}

type sessionIDContextKey struct{}

// WithSessionID attaches sessionID to ctx so tools invoked mid-turn (like
// ChatHistorySearchTool) can scope themselves to the calling session
// without the platform-wide Tool interface needing a session parameter.
func WithSessionID(ctx context.Context, sessionID uint) context.Context {
	return context.WithValue(ctx, sessionIDContextKey{}, sessionID)
}

// SessionIDFromContext retrieves the session id attached by WithSessionID.
func SessionIDFromContext(ctx context.Context) (uint, bool) {
	id, ok := ctx.Value(sessionIDContextKey{}).(uint)
	return id, ok
}

type workspaceContextKey struct{}

// WithWorkspace attaches the resolved on-disk workspace root (a bound
// Project's directory) to ctx, for tools that operate on the filesystem.
func WithWorkspace(ctx context.Context, workspace string) context.Context {
	return context.WithValue(ctx, workspaceContextKey{}, workspace)
}

// WorkspaceFromContext retrieves the workspace attached by WithWorkspace.
// ok is false when the calling session has no Project bound.
func WorkspaceFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(workspaceContextKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

type subagentContextKey struct{}

// SubagentContext identifies the delegated run currently executing. RunID is
// zero for a root chat turn; Depth is zero at the root.
type SubagentContext struct {
	RunID           uint
	ParentSessionID uint
	Depth           int
}

func WithSubagentContext(ctx context.Context, value SubagentContext) context.Context {
	return context.WithValue(ctx, subagentContextKey{}, value)
}

func SubagentContextFromContext(ctx context.Context) SubagentContext {
	value, _ := ctx.Value(subagentContextKey{}).(SubagentContext)
	return value
}

type turnMessagesContextKey struct{}

// WithTurnMessages attaches a defensive snapshot of the messages visible at
// the current tool-call boundary.
func WithTurnMessages(ctx context.Context, messages []store.ChatMessage) context.Context {
	return context.WithValue(ctx, turnMessagesContextKey{}, append([]store.ChatMessage(nil), messages...))
}

func TurnMessagesFromContext(ctx context.Context) ([]store.ChatMessage, bool) {
	messages, ok := ctx.Value(turnMessagesContextKey{}).([]store.ChatMessage)
	return append([]store.ChatMessage(nil), messages...), ok
}

type toolCallContextKey struct{}

func WithToolCall(ctx context.Context, call ds4.ToolCall) context.Context {
	return context.WithValue(ctx, toolCallContextKey{}, call)
}

func ToolCallFromContext(ctx context.Context) (ds4.ToolCall, bool) {
	call, ok := ctx.Value(toolCallContextKey{}).(ds4.ToolCall)
	return call, ok
}
