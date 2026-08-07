// Package toolkit defines the platform's tool runtime: the Tool capability
// contract, the ToolResult value, the Registry that holds tools, and the
// validation/execution helpers that run a single tool call. It has no
// knowledge of any concrete tool; concrete tools live in internal/tools and
// depend on this package.
package toolkit

import "context"

// Tool is a single callable capability exposed to the LLM.
type Tool interface {
	// Name is the unique, stable tool identifier referenced by ToolGroup
	// config files.
	Name() string
	// Description is shown to the LLM to explain when to use this tool.
	Description() string
	// Parameters is a JSON Schema object describing the tool's arguments.
	Parameters() map[string]any
	// Execute runs the tool with already JSON-decoded arguments and
	// returns a structured result. Execute must not return nil; use
	// ErrorResult for failures rather than a Go error, since callers
	// need a ToolResult even on failure.
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// AsyncCallback is invoked (possibly from another goroutine) once an
// AsyncExecutor's background work completes.
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncExecutor is an optional capability for tools whose work continues
// past the current turn (e.g. a spawned subagent). ExecuteAsync must
// return immediately with an Async ToolResult and later invoke cb exactly
// once with the final result.
type AsyncExecutor interface {
	Tool
	ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult
}
