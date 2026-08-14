package registry

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsUndefinedPromptConstants(t *testing.T) {
	tests := []struct {
		name       string
		registry   *Registry
		wantDetail string
	}{
		{
			name: "identity system prompt",
			registry: &Registry{
				Identities: map[string]Identity{"rose": {Name: "rose", SystemPrompt: "Hello {{user_name}}"}},
				Constants:  map[string]Constant{},
			},
			wantDetail: `identity "rose" system prompt references undefined constant "user_name"`,
		},
		{
			name: "identity injected message",
			registry: &Registry{
				Identities: map[string]Identity{"rose": {Name: "rose", InjectedMessages: []Message{{Role: "system", Content: "Hello {{user_name}}"}}}},
				Constants:  map[string]Constant{},
			},
			wantDetail: `identity "rose" injected message 1 references undefined constant "user_name"`,
		},
		{
			name: "impression message",
			registry: &Registry{
				Impressions: map[string]Impression{"tone": {Name: "tone", Messages: []Message{{Role: "system", Content: "Be {{tone}}"}}}},
				Constants:   map[string]Constant{},
			},
			wantDetail: `impression "tone" message 1 references undefined constant "tone"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.registry.Validate(map[string]bool{}, map[string]bool{})
			if err == nil || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("Validate() error = %v, want detail %q", err, test.wantDetail)
			}
		})
	}
}

func TestRenderPromptReplacesEveryDefinedConstant(t *testing.T) {
	reg := &Registry{Constants: map[string]Constant{
		"name": {Name: "name", Value: "Rose"},
		"tone": {Name: "tone", Value: "warm"},
	}}

	got, err := reg.RenderPrompt("{{name}} is {{tone}}; {{name}} listens.")
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if want := "Rose is warm; Rose listens."; got != want {
		t.Fatalf("RenderPrompt() = %q, want %q", got, want)
	}
}

func TestRenderPromptUsesDynamicBuiltins(t *testing.T) {
	reg := &Registry{Constants: map[string]Constant{}}
	when := time.Date(2026, time.August, 14, 9, 8, 7, 0, time.FixedZone("CST", 8*60*60))
	vars := TimePromptVars(when)
	vars["project"] = "default-workspace"

	got, err := reg.RenderPrompt("{{project}} at {{now}} ({{date}} {{time}})", vars)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if want := "default-workspace at 2026-08-14T09:08:07+08:00 (2026-08-14 09:08:07+08:00)"; got != want {
		t.Fatalf("RenderPrompt() = %q, want %q", got, want)
	}
}

func TestRenderPromptConstantOverridesBuiltin(t *testing.T) {
	reg := &Registry{Constants: map[string]Constant{"project": {Name: "project", Value: "configured-project"}}}
	got, err := reg.RenderPrompt("{{project}}", PromptVars{"project": "runtime-project"})
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if got != "configured-project" {
		t.Fatalf("RenderPrompt() = %q, want configured constant", got)
	}
}

func TestRenderPromptRejectsMissingBuiltinValue(t *testing.T) {
	reg := &Registry{Constants: map[string]Constant{}}
	_, err := reg.RenderPrompt("{{project}}")
	if err == nil || !strings.Contains(err.Error(), `undefined constant "project"`) {
		t.Fatalf("RenderPrompt() error = %v, want undefined project", err)
	}
}

func TestValidateAllowsBuiltinPromptVariables(t *testing.T) {
	reg := &Registry{Identities: map[string]Identity{
		"rose": {Name: "rose", SystemPrompt: "Today is {{date}}."},
	}, Constants: map[string]Constant{}}
	if err := reg.Validate(map[string]bool{}, map[string]bool{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRenderPromptRejectsInvalidPlaceholder(t *testing.T) {
	reg := &Registry{Constants: map[string]Constant{}}

	_, err := reg.RenderPrompt("Hello {{bad-name}}")
	if err == nil || !strings.Contains(err.Error(), `invalid placeholder "{{bad-name}}"`) {
		t.Fatalf("RenderPrompt() error = %v, want invalid placeholder", err)
	}
}

func TestRenderPromptRejectsPlaceholderIntroducedByConstant(t *testing.T) {
	reg := &Registry{Constants: map[string]Constant{
		"name": {Name: "name", Value: "{{other_name}}"},
	}}

	_, err := reg.RenderPrompt("Hello {{name}}")
	if err == nil || !strings.Contains(err.Error(), `constant value contains placeholder "{{other_name}}"`) {
		t.Fatalf("RenderPrompt() error = %v, want placeholder introduced by constant", err)
	}
}
