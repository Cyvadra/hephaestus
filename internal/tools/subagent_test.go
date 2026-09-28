package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

func TestSubagentToolDescriptionsPreferDelegation(t *testing.T) {
	for _, mode := range []store.SubagentMode{store.SubagentModeSpawn, store.SubagentModeFork} {
		description := (&SubagentTool{mode: mode}).Description()
		for _, required := range []string{"Prefer a subagent", "code development", "multi-step operations", "Do trivial tasks directly"} {
			if !strings.Contains(description, required) {
				t.Errorf("%s description %q does not contain %q", mode, description, required)
			}
		}
	}
}

func TestSubagentToolSchemaRequiresCategory(t *testing.T) {
	parameters := (SubagentTool{}).Parameters()
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", parameters["properties"])
	}
	category, ok := properties["category"].(map[string]any)
	if !ok {
		t.Fatalf("category schema = %#v", properties["category"])
	}
	if got, want := category["enum"], []string{"coding", "operations", "research", "background", "general"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("category enum = %#v, want %#v", got, want)
	}
	if got, want := parameters["required"], []string{"description", "category", "prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %#v, want %#v", got, want)
	}
}

func TestSubagentToolRejectsMissingOrInvalidCategory(t *testing.T) {
	tool := &SubagentTool{mode: store.SubagentModeSpawn}
	for name, args := range map[string]map[string]any{
		"missing": {"description": "test task", "prompt": "do work"},
		"invalid": {"description": "test task", "category": "other", "prompt": "do work"},
	} {
		t.Run(name, func(t *testing.T) {
			result := tool.Execute(context.Background(), args)
			if !result.IsError || !strings.Contains(result.ForLLM, "category") {
				t.Fatalf("result = %#v, want category error", result)
			}
		})
	}
}

func TestShellToolDescriptionPrefersSubagentForSubstantialWork(t *testing.T) {
	description := (ShellTool{}).Description()
	for _, required := range []string{
		"code development",
		"multi-step operational work",
		"spawn or fork subagent",
		"Never run low-value expansive filesystem scans such as `find /`",
		"ask the user for clarification",
	} {
		if !strings.Contains(description, required) {
			t.Errorf("shell description %q does not contain %q", description, required)
		}
	}
}

func TestSubagentControlToolSchemas(t *testing.T) {
	cases := []struct {
		tool     toolkit.Tool
		required []string
	}{
		{NewSubagentListTool(nil), nil},
		{NewSubagentStatusTool(nil), []string{"run_id"}},
		{NewSubagentSteerTool(nil), []string{"run_id", "message"}},
		{NewSubagentStopTool(nil), []string{"run_id"}},
	}
	for _, c := range cases {
		required, _ := c.tool.Parameters()["required"].([]string)
		if !reflect.DeepEqual(required, c.required) {
			t.Errorf("%s required = %v, want %v", c.tool.Name(), required, c.required)
		}
	}
}

func TestSubagentControlToolsRejectBadRunID(t *testing.T) {
	ctx := toolkit.WithSessionID(context.Background(), 1)
	for _, tool := range []toolkit.Tool{NewSubagentStatusTool(nil), NewSubagentSteerTool(nil), NewSubagentStopTool(nil)} {
		for _, args := range []map[string]any{{}, {"run_id": 0.0}, {"run_id": 1.5}, {"run_id": "3"}} {
			if result := tool.Execute(ctx, args); !result.IsError {
				t.Errorf("%s accepted run_id %v", tool.Name(), args["run_id"])
			}
		}
	}
}

func TestSubagentToolsAreDelegating(t *testing.T) {
	for _, tool := range []toolkit.Tool{
		NewSpawnTool(nil, nil), NewForkTool(nil, nil),
		NewSubagentAwaitTool(nil), NewSubagentListTool(nil), NewSubagentStatusTool(nil),
		NewSubagentSteerTool(nil), NewSubagentStopTool(nil),
	} {
		if _, ok := tool.(toolkit.Delegating); !ok {
			t.Errorf("%s does not implement toolkit.Delegating; child sessions would receive it", tool.Name())
		}
	}
}

func TestSubagentAwaitToolRejectsBadArguments(t *testing.T) {
	ctx := toolkit.WithSessionID(context.Background(), 1)
	tool := NewSubagentAwaitTool(nil)
	for _, args := range []map[string]any{
		{"run_ids": "5"}, {"run_ids": []any{"5"}}, {"run_ids": []any{0.0}},
		{"timeout_seconds": "10"}, {"timeout_seconds": -1.0},
	} {
		if result := tool.Execute(ctx, args); !result.IsError {
			t.Errorf("await accepted %v", args)
		}
	}
}

func TestAwaitTimeoutArgIsBounded(t *testing.T) {
	timeout, failure := awaitTimeoutArg(map[string]any{"timeout_seconds": 1e11}, "await")
	if failure != nil || timeout != maxAwaitTimeout {
		t.Fatalf("timeout = %v, failure = %v, want %v", timeout, failure, maxAwaitTimeout)
	}
}
