package tools

import "context"

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
