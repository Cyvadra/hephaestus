package registry

import (
	"fmt"
)

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
			wf, ok := r.Workflows[binding.Workflow]
			if !ok {
				return fmt.Errorf("registry: job %q references unknown workflow %q", name, binding.Workflow)
			}
			if err := validateBindingInput(name, binding, wf); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateBindingInput checks that a Job binding's input satisfies its
// Workflow's input schema. Literal-only inputs are validated in full;
// templated inputs (containing ${...} placeholders) check the required key
// set now and defer value validation to runtime substitution.
func validateBindingInput(jobName string, binding JobWorkflowBinding, wf Workflow) error {
	compiled, err := CompileSchema(wf.InputSchema)
	if err != nil {
		return fmt.Errorf("registry: workflow %q input schema: %v", wf.Name, err)
	}
	if !compiled.HasConstraints() {
		return nil
	}
	input := binding.Input
	if input == nil {
		input = map[string]any{}
	}
	if hasPlaceholders(input) {
		for _, key := range requiredSchemaKeys(wf.InputSchema) {
			if _, ok := input[key]; !ok {
				return fmt.Errorf("registry: job %q binding workflow %q input is missing required key %q", jobName, binding.Workflow, key)
			}
		}
		return nil
	}
	if err := compiled.Validate(input); err != nil {
		return fmt.Errorf("registry: job %q binding workflow %q input does not satisfy input schema: %v", jobName, binding.Workflow, err)
	}
	return nil
}
