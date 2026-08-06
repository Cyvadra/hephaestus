package registry

import "fmt"

// Validate cross-checks references between loaded configs and against the
// externally-known sets of registered tool names and plugin names. It must
// run once at startup, after tools/plugins have registered themselves in Go
// code, and before any Concierge is used.
func (r *Registry) Validate(knownTools, knownPlugins map[string]bool) error {
	for name, tg := range r.ToolGroups {
		for _, tool := range tg.Tools {
			if !knownTools[tool] {
				return fmt.Errorf("registry: tool group %q references unknown tool %q", name, tool)
			}
		}
	}

	for name, c := range r.Concierges {
		if _, ok := r.Identities[c.Identity]; !ok {
			return fmt.Errorf("registry: concierge %q references unknown identity %q", name, c.Identity)
		}
		for _, imp := range c.Impressions {
			if _, ok := r.Impressions[imp]; !ok {
				return fmt.Errorf("registry: concierge %q references unknown impression %q", name, imp)
			}
		}
		for _, tg := range c.ToolGroups {
			if _, ok := r.ToolGroups[tg]; !ok {
				return fmt.Errorf("registry: concierge %q references unknown tool group %q", name, tg)
			}
		}
		for _, p := range c.Plugins {
			if !knownPlugins[p] {
				return fmt.Errorf("registry: concierge %q references unknown plugin %q", name, p)
			}
		}
	}

	for name, wf := range r.Workflows {
		if _, ok := r.Concierges[wf.Concierge]; !ok {
			return fmt.Errorf("registry: workflow %q references unknown concierge %q", name, wf.Concierge)
		}
	}

	for name, job := range r.Jobs {
		for _, binding := range job.Workflows {
			if _, ok := r.Workflows[binding.Workflow]; !ok {
				return fmt.Errorf("registry: job %q references unknown workflow %q", name, binding.Workflow)
			}
		}
	}

	return nil
}
