package tools

import (
	"context"
	"testing"
)

type fixedSchemaTool struct {
	execute func(ctx context.Context, args map[string]any) *ToolResult
}

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
