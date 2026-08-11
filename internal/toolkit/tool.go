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

// Example is an optional Tool capability. A tool that implements it provides
// a concrete usage example, including sample response data, that is attached
// to the tool's registration whenever it is offered to the LLM. This teaches
// the model the tool's input/output shape and, where relevant, host context.
type Example interface {
	Example() string
}

// Audited is an optional Tool capability. A tool that implements it marks
// externally visible side effects (shell execution, project creation) that
// must be recorded in the ToolAudit table even when the surrounding turn
// later aborts. Keeping this on the tool itself instead of a hard-coded
// list in the pipeline means adding a risky tool audits it by default.
type Audited interface {
	Audited() bool
}
