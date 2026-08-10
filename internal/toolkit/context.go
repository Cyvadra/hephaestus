package toolkit

import "context"

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
