package tools

import (
	"context"
	"encoding/json"
	"time"
)

// EchoTool trivially echoes its input; used to exercise the registry and
// tool-loop plumbing before real tools exist.
type EchoTool struct{}

func (EchoTool) Name() string        { return "echo" }
func (EchoTool) Description() string { return "Echoes back the provided text." }
func (EchoTool) Parameters() any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
		"required":   []string{"text"},
	}
}

func (EchoTool) Execute(_ context.Context, argumentsJSON string) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", err
	}
	return args.Text, nil
}

// CurrentTimeTool returns the current time on the host, in RFC3339.
type CurrentTimeTool struct{}

func (CurrentTimeTool) Name() string { return "current_time" }
func (CurrentTimeTool) Description() string {
	return "Returns the current host time in RFC3339 format."
}
func (CurrentTimeTool) Parameters() any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (CurrentTimeTool) Execute(_ context.Context, _ string) (string, error) {
	return time.Now().Format(time.RFC3339), nil
}

// RegisterBuiltins adds every built-in placeholder tool to r.
func RegisterBuiltins(r *Registry) {
	r.Register(EchoTool{})
	r.Register(CurrentTimeTool{})
}
