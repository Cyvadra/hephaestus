package tools

import "fmt"

// Registry holds every Tool the platform knows how to execute.
type Registry struct {
	byName map[string]Tool
}

// NewRegistry creates an empty tool Registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Tool{}}
}

// Register adds a Tool, panicking on duplicate names since that indicates a
// programming error in this platform's own tool definitions.
func (r *Registry) Register(t Tool) {
	if _, dup := r.byName[t.Name()]; dup {
		panic(fmt.Sprintf("tools: duplicate tool name %q", t.Name()))
	}
	r.byName[t.Name()] = t
}

// Get returns a Tool by name, or false if it isn't registered.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// KnownNames returns the set of registered tool names, for use by
// registry.Registry.Validate.
func (r *Registry) KnownNames() map[string]bool {
	out := make(map[string]bool, len(r.byName))
	for name := range r.byName {
		out[name] = true
	}
	return out
}

// Expand resolves an ordered list of tool group names into the ordered,
// de-duplicated list of actual Tools they contain.
func (r *Registry) Expand(groupNames []string, groups map[string]ToolGroupTools) ([]Tool, error) {
	seen := map[string]bool{}
	var out []Tool
	for _, groupName := range groupNames {
		group, ok := groups[groupName]
		if !ok {
			return nil, fmt.Errorf("tools: unknown tool group %q", groupName)
		}
		for _, toolName := range group.Tools {
			if seen[toolName] {
				continue
			}
			t, ok := r.Get(toolName)
			if !ok {
				return nil, fmt.Errorf("tools: tool group %q references unregistered tool %q", groupName, toolName)
			}
			seen[toolName] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// ToolGroupTools is the minimal shape Expand needs from a registry.ToolGroup,
// kept separate here to avoid an import cycle with package registry.
type ToolGroupTools struct {
	Tools []string
}
