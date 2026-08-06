// Package tools implements the platform's built-in tool registry. Actual
// tools are predefined in Go; ToolGroup config files only select among them
// by name, they never define new tools.
package tools

import "context"

// Tool is a single callable capability exposed to the LLM.
type Tool interface {
	// Name is the unique, stable tool identifier referenced by ToolGroup
	// config files.
	Name() string
	// Description is shown to the LLM to explain when to use this tool.
	Description() string
	// Parameters is a JSON Schema object describing the tool's arguments.
	Parameters() any
	// Execute runs the tool with JSON-encoded arguments and returns a
	// JSON-encodable (or plain text) result.
	Execute(ctx context.Context, argumentsJSON string) (string, error)
}
