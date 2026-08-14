package agent

import (
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

func TestResolveConciergeRendersConstants(t *testing.T) {
	reg := &registry.Registry{
		Identities: map[string]registry.Identity{
			"rose": {
				Name:         "rose",
				SystemPrompt: "You are {{role}} in {{location}}.",
				InjectedMessages: []registry.Message{
					{Role: "system", Content: "Address the user as {{user_name}}."},
				},
			},
		},
		Impressions: map[string]registry.Impression{
			"tone": {Name: "tone", Enabled: true, Messages: []registry.Message{{Role: "system", Content: "Use a {{tone}} tone."}}},
		},
		ToolGroups: map[string]registry.ToolGroup{},
		Concierges: map[string]registry.Concierge{
			"rose": {Name: "rose", Identity: "rose", Impressions: []string{"tone"}},
		},
		Constants: map[string]registry.Constant{
			"role":      {Name: "role", Value: "helpful assistant"},
			"user_name": {Name: "user_name", Value: "Jason"},
			"tone":      {Name: "tone", Value: "warm"},
			"location":  {Name: "location", Value: "production"},
		},
	}

	resolved, err := ResolveConcierge(reg, "rose", toolkit.NewRegistry())
	if err != nil {
		t.Fatalf("ResolveConcierge: %v", err)
	}
	if got, want := resolved.Identity.SystemPrompt, "You are helpful assistant in production."; got != want {
		t.Fatalf("system prompt = %q, want %q", got, want)
	}
	if got, want := resolved.Identity.InjectedMessages[0].Content, "Address the user as Jason."; got != want {
		t.Fatalf("injected message = %q, want %q", got, want)
	}
	if got, want := resolved.Static[0].Content, "Use a warm tone."; got != want {
		t.Fatalf("impression message = %q, want %q", got, want)
	}
}

func TestResolveConciergeRejectsUndefinedConstant(t *testing.T) {
	reg := &registry.Registry{
		Identities: map[string]registry.Identity{
			"rose": {Name: "rose", SystemPrompt: "Hello {{missing}}"},
		},
		ToolGroups: map[string]registry.ToolGroup{},
		Concierges: map[string]registry.Concierge{
			"rose": {Name: "rose", Identity: "rose"},
		},
		Constants: map[string]registry.Constant{},
	}

	_, err := ResolveConcierge(reg, "rose", toolkit.NewRegistry())
	if err == nil || !strings.Contains(err.Error(), `undefined constant "missing"`) {
		t.Fatalf("ResolveConcierge() error = %v, want undefined constant", err)
	}
}
