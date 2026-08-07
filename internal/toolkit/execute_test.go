package toolkit

import (
	"context"
	"testing"
)

type fixedSchemaTool struct {
	execute func(ctx context.Context, args map[string]any) *ToolResult
}

type unavailableTool struct{ fixedSchemaTool }

func (unavailableTool) Available() bool { return false }

func (fixedSchemaTool) Name() string        { return "fixed" }
func (fixedSchemaTool) Description() string { return "test tool" }
func (fixedSchemaTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
		"required":   []string{"text"},
	}
}
func (t fixedSchemaTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return t.execute(ctx, args)
}

func TestRunTool_RejectsMissingRequiredArgument(t *testing.T) {
	tool := fixedSchemaTool{execute: func(context.Context, map[string]any) *ToolResult {
		t.Fatal("execute must not run when validation fails")
		return nil
	}}
	result := RunTool(context.Background(), tool, map[string]any{})
	if !result.IsError {
		t.Fatalf("expected validation error, got %+v", result)
	}
}

func TestRunTool_RecoversPanic(t *testing.T) {
	tool := fixedSchemaTool{execute: func(context.Context, map[string]any) *ToolResult {
		panic("boom")
	}}
	result := RunTool(context.Background(), tool, map[string]any{"text": "hi"})
	if !result.IsError {
		t.Fatalf("expected panic to be converted into an error result, got %+v", result)
	}
}

func TestRunTool_NormalizesNilResult(t *testing.T) {
	tool := fixedSchemaTool{execute: func(context.Context, map[string]any) *ToolResult {
		return nil
	}}
	result := RunTool(context.Background(), tool, map[string]any{"text": "hi"})
	if result == nil || !result.IsError {
		t.Fatalf("expected nil result to be normalized into an error result, got %+v", result)
	}
}

func TestRunTool_PassesThroughSuccess(t *testing.T) {
	tool := fixedSchemaTool{execute: func(_ context.Context, args map[string]any) *ToolResult {
		return NewToolResult(args["text"].(string))
	}}
	result := RunTool(context.Background(), tool, map[string]any{"text": "hi"})
	if result.IsError || result.ForLLM != "hi" {
		t.Fatalf("expected successful passthrough, got %+v", result)
	}
}

type schemaTool struct {
	schema map[string]any
}

func (schemaTool) Name() string        { return "schema" }
func (schemaTool) Description() string { return "test tool" }
func (t schemaTool) Parameters() map[string]any {
	return t.schema
}
func (schemaTool) Execute(context.Context, map[string]any) *ToolResult {
	return NewToolResult("ok")
}

func TestRunTool_EnforcesNumericBounds(t *testing.T) {
	tool := schemaTool{schema: map[string]any{"type": "object", "properties": map[string]any{
		"count": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(10)},
	}, "required": []string{"count"}}}

	if result := RunTool(context.Background(), tool, map[string]any{"count": 0}); !result.IsError {
		t.Fatalf("expected minimum violation, got %+v", result)
	}
	if result := RunTool(context.Background(), tool, map[string]any{"count": 11}); !result.IsError {
		t.Fatalf("expected maximum violation, got %+v", result)
	}
	if result := RunTool(context.Background(), tool, map[string]any{"count": 5}); result.IsError {
		t.Fatalf("expected in-bounds success, got %+v", result)
	}
}

func TestRunTool_RejectsWrongTypeForNumber(t *testing.T) {
	tool := schemaTool{schema: map[string]any{"type": "object", "properties": map[string]any{
		"count": map[string]any{"type": "integer"},
	}}}
	if result := RunTool(context.Background(), tool, map[string]any{"count": "many"}); !result.IsError {
		t.Fatalf("expected type violation, got %+v", result)
	}
}

func TestRegistryExpandSkipsUnavailableTools(t *testing.T) {
	registry := NewRegistry()
	registry.Register(unavailableTool{})
	tools, err := registry.Expand([]string{"group"}, map[string]ToolGroupTools{"group": {Tools: []string{"fixed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected unavailable tool to be omitted, got %d", len(tools))
	}
}
