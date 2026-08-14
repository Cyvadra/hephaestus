package agent

import (
	"fmt"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// ConciergeConfig is the expanded runtime configuration for one Concierge.
type ConciergeConfig struct {
	Identity registry.Identity
	Toolset  []toolkit.Tool
	Plugins  []string
	// Static holds enabled Impression messages, in order, to prepend to a
	// turn's context.
	Static []store.ChatMessage
}

// ResolveConcierge expands a named Concierge from an immutable registry
// snapshot into the identity, toolset, plugins, and static context messages
// needed to run one turn.
func ResolveConcierge(reg *registry.Registry, name string, toolReg *toolkit.Registry, vars ...registry.PromptVars) (ConciergeConfig, error) {
	concierge, ok := reg.Concierges[name]
	if !ok {
		return ConciergeConfig{}, fmt.Errorf("agent: concierge %q not found", name)
	}
	identity, ok := reg.Identities[concierge.Identity]
	if !ok {
		return ConciergeConfig{}, fmt.Errorf("agent: concierge %q references missing identity %q", name, concierge.Identity)
	}
	identity, err := reg.RenderIdentity(identity, vars...)
	if err != nil {
		return ConciergeConfig{}, fmt.Errorf("agent: %w", err)
	}

	toolGroups := make(map[string]toolkit.ToolGroupTools, len(reg.ToolGroups))
	for groupName, group := range reg.ToolGroups {
		toolGroups[groupName] = toolkit.ToolGroupTools{Tools: group.Tools}
	}
	toolset, err := toolReg.Expand(concierge.ToolGroups, toolGroups)
	if err != nil {
		return ConciergeConfig{}, err
	}

	var static []store.ChatMessage
	for _, impressionName := range concierge.Impressions {
		impression, ok := reg.Impressions[impressionName]
		if !ok || !impression.Enabled {
			continue
		}
		for index, message := range impression.Messages {
			content, err := reg.RenderPrompt(message.Content, vars...)
			if err != nil {
				return ConciergeConfig{}, fmt.Errorf("agent: render impression %q message %d: %w", impressionName, index+1, err)
			}
			static = append(static, store.ChatMessage{Role: message.Role, Content: content})
		}
	}

	return ConciergeConfig{
		Identity: identity,
		Toolset:  toolset,
		Plugins:  append([]string(nil), concierge.Plugins...),
		Static:   static,
	}, nil
}
