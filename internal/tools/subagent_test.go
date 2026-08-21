package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/store"
)

func TestSubagentToolDescriptionsRequireDelegation(t *testing.T) {
	for _, mode := range []store.SubagentMode{store.SubagentModeSpawn, store.SubagentModeFork} {
		description := (&SubagentTool{mode: mode}).Description()
		for _, required := range []string{"MANDATORY DELEGATION", "code development", "multi-step operations"} {
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
	for _, required := range []string{"code development", "multi-step operational work", "spawn or fork subagent"} {
		if !strings.Contains(description, required) {
			t.Errorf("shell description %q does not contain %q", description, required)
		}
	}
}
