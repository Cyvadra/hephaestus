package tools

import (
	"context"
	"time"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// EchoTool trivially echoes its input; used to exercise the registry and
// tool-loop plumbing before real tools exist.
type EchoTool struct{}

func (EchoTool) Name() string        { return "echo" }
func (EchoTool) Description() string { return "Echoes back the provided text." }
func (EchoTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
		"required":   []string{"text"},
	}
}

func (EchoTool) Execute(_ context.Context, args map[string]any) *toolkit.ToolResult {
	text, _ := args["text"].(string)
	return toolkit.NewToolResult(text)
}

// CurrentTimeTool returns the current time on the host, in RFC3339.
type CurrentTimeTool struct{}

func (CurrentTimeTool) Name() string { return "current_time" }
func (CurrentTimeTool) Description() string {
	return "Returns the current host time in RFC3339 format."
}
func (CurrentTimeTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (CurrentTimeTool) Execute(_ context.Context, _ map[string]any) *toolkit.ToolResult {
	return toolkit.NewToolResult(time.Now().Format(time.RFC3339))
}

// RegisterPlaceholderTools adds the placeholder tools (echo, current_time)
// that exercise the registry and tool-loop plumbing. Real tools are wired
// in at application startup by cmd/hephaestus.
func RegisterPlaceholderTools(r *toolkit.Registry) {
	r.Register(EchoTool{})
	r.Register(CurrentTimeTool{})
}
